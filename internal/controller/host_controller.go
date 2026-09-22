// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/provisioner"
	"github.com/Ashon/kgenesis/internal/ssh"
)

// How often an unclaimed Host is re-probed. Reachability changes slowly, and each
// probe costs an SSH handshake against a machine that may be powered off, so the
// healthy interval is deliberately long.
const (
	probeIntervalHealthy   = 5 * time.Minute
	probeIntervalUnhealthy = 1 * time.Minute
	probeTimeout           = 30 * time.Second
)

// HostReconciler keeps the pool's view of each machine current.
//
// It owns a Host's reachability and system facts. Claim state is owned by the
// HostMachine reconciler, so this controller leaves the phase alone once a Host
// has been claimed.
type HostReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hosts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hosts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *HostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	host := &infrav1.Host{}
	if err := r.Get(ctx, req.NamespacedName, host); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	helper, err := patch.NewHelper(host, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if patchErr := helper.Patch(ctx, host); patchErr != nil && err == nil {
			err = patchErr
		}
	}()

	if !host.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, host)
	}

	if !controllerutil.ContainsFinalizer(host, infrav1.HostFinalizer) {
		controllerutil.AddFinalizer(host, infrav1.HostFinalizer)
		return ctrl.Result{}, nil
	}

	// A claimed Host belongs to a HostMachine; probing it here would race that
	// controller over the phase and add SSH load for no new information.
	if host.Status.ClaimRef != nil {
		return ctrl.Result{}, nil
	}

	if host.Spec.Unhealthy {
		host.Status.Phase = infrav1.HostPhaseUnreachable
		setCondition(&host.Status.Conditions, infrav1.HostReachableCondition, metav1.ConditionFalse,
			infrav1.ReasonProbeFailed, "Host is marked unhealthy in its spec", host.Generation)
		return ctrl.Result{}, nil
	}

	return r.probe(ctx, logger, host)
}

func (r *HostReconciler) probe(ctx context.Context, logger logr.Logger, host *infrav1.Host) (ctrl.Result, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	host.Status.LastProbeTime = ptr(metav1.Now())

	conn, err := connect(probeCtx, r.Client, host)
	if err != nil {
		host.Status.Phase = infrav1.HostPhaseUnreachable
		setCondition(&host.Status.Conditions, infrav1.HostReachableCondition, metav1.ConditionFalse,
			infrav1.ReasonProbeFailed, truncate(err.Error(), conditionMessageLimit), host.Generation)

		// A changed host key is an operator decision, not something to retry into.
		if errors.Is(err, ssh.ErrHostKeyMismatch) {
			logger.Info("host key mismatch, not retrying until the Host is corrected", "host", host.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: probeIntervalUnhealthy}, nil
	}
	defer func() { _ = conn.Close() }()

	info, err := provisioner.Probe(probeCtx, conn)
	if err != nil {
		host.Status.Phase = infrav1.HostPhaseUnreachable
		setCondition(&host.Status.Conditions, infrav1.HostReachableCondition, metav1.ConditionFalse,
			infrav1.ReasonProbeFailed, truncate(err.Error(), conditionMessageLimit), host.Generation)
		return ctrl.Result{RequeueAfter: probeIntervalUnhealthy}, nil
	}

	host.Status.SystemInfo = &infrav1.HostSystemInfo{
		Hostname:         info.Hostname,
		OSImage:          info.OSImage,
		KernelVersion:    info.KernelVersion,
		Architecture:     info.Architecture,
		CPUCores:         info.CPUCores,
		MemoryMB:         info.MemoryMB,
		ContainerRuntime: info.ContainerRuntime,
	}
	host.Status.Phase = infrav1.HostPhaseAvailable
	setCondition(&host.Status.Conditions, infrav1.HostReachableCondition, metav1.ConditionTrue,
		infrav1.ReasonProbeSucceeded, "SSH probe succeeded", host.Generation)

	return ctrl.Result{RequeueAfter: probeIntervalHealthy}, nil
}

func (r *HostReconciler) reconcileDelete(ctx context.Context, host *infrav1.Host) (ctrl.Result, error) {
	// Releasing a Host out from under a running Machine would strand a cluster
	// node, so deletion waits for the claim to be dropped.
	if host.Status.ClaimRef != nil {
		claim := &infrav1.HostMachine{}
		key := client.ObjectKey{Namespace: host.Status.ClaimRef.Namespace, Name: host.Status.ClaimRef.Name}

		switch err := r.Get(ctx, key, claim); {
		case err == nil:
			setCondition(&host.Status.Conditions, infrav1.HostReachableCondition, metav1.ConditionFalse,
				infrav1.ReasonDeleting,
				fmt.Sprintf("Waiting for HostMachine %s to release this host", key), host.Generation)
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil

		case apierrors.IsNotFound(err):
			// The claimant is gone; the claim is stale and can be cleared.
			host.Status.ClaimRef = nil

		default:
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(host, infrav1.HostFinalizer)
	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller.
func (r *HostReconciler) SetupWithManager(mgr ctrl.Manager, opts controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.Host{}).
		WithOptions(opts).
		Named("host").
		Complete(r)
}
