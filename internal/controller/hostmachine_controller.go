// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/cloudinit"
	"github.com/Ashon/kgenesis/internal/provisioner"
	"github.com/Ashon/kgenesis/internal/ssh"
)

// Requeue intervals. Bootstrapping is slow and mostly spent waiting on image
// pulls and kubeadm, so polling is paced to stay cheap on the SSH side.
const (
	requeueWaitingForDependency = 20 * time.Second
	requeueBootstrapRunning     = 20 * time.Second
	requeueBootstrapFailed      = 2 * time.Minute
	requeueAfterStart           = 30 * time.Second

	sshOperationTimeout = 60 * time.Second
)

// ProviderIDPrefix namespaces the provider IDs this provider issues.
const ProviderIDPrefix = "kgenesis://"

// HostMachineReconciler turns a Cluster API Machine into a bootstrapped host.
type HostMachineReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hostmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hostmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hostmachinetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch

func (r *HostMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	hostMachine := &infrav1.HostMachine{}
	if err := r.Get(ctx, req.NamespacedName, hostMachine); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, hostMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.V(4).Info("Waiting for the Machine owner reference to be set")
		return ctrl.Result{}, nil
	}

	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		logger.V(4).Info("Waiting for the Machine to be associated with a Cluster")
		return ctrl.Result{}, nil
	}

	if annotations.IsPaused(cluster, hostMachine) {
		logger.V(4).Info("Reconciliation is paused")
		return ctrl.Result{}, nil
	}

	helper, err := patch.NewHelper(hostMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := helper.Patch(ctx, hostMachine); err != nil && reterr == nil {
			reterr = err
		}
	}()

	if !hostMachine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, logger, hostMachine)
	}

	if !controllerutil.ContainsFinalizer(hostMachine, infrav1.MachineFinalizer) {
		controllerutil.AddFinalizer(hostMachine, infrav1.MachineFinalizer)
	}

	return r.reconcileNormal(ctx, logger, cluster, machine, hostMachine)
}

func (r *HostMachineReconciler) reconcileNormal(
	ctx context.Context,
	logger logr.Logger,
	cluster *clusterv1.Cluster,
	machine *clusterv1.Machine,
	hostMachine *infrav1.HostMachine,
) (ctrl.Result, error) {
	if !ptrTo(cluster.Status.Initialization.InfrastructureProvisioned) {
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonWaitingForCluster,
			"Waiting for the cluster infrastructure to be provisioned", hostMachine.Generation)
		return ctrl.Result{}, nil
	}

	// Without bootstrap data there is nothing to push, so the host is not claimed
	// yet either: holding a host hostage while CABPK works would shrink the pool
	// for no reason.
	if machine.Spec.Bootstrap.DataSecretName == nil {
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonWaitingForBootstrapData,
			"Waiting for the bootstrap provider to publish its data secret", hostMachine.Generation)
		return ctrl.Result{RequeueAfter: requeueWaitingForDependency}, nil
	}

	host, err := r.claimHost(ctx, logger, cluster, hostMachine)
	if err != nil {
		return ctrl.Result{}, err
	}
	if host == nil {
		const message = "No Available host matches the selector; " +
			"add hosts to the inventory, or check `kgenesis inventory list` for unreachable ones"
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineHostClaimedCondition,
			metav1.ConditionFalse, infrav1.ReasonNoMatchingHost, message, hostMachine.Generation)
		// Mirror the reason onto Provisioned: leaving the earlier
		// WaitingForBootstrapData message there would point at the wrong thing.
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonNoMatchingHost, message, hostMachine.Generation)
		return ctrl.Result{RequeueAfter: requeueWaitingForDependency}, nil
	}

	setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineHostClaimedCondition,
		metav1.ConditionTrue, infrav1.ReasonHostClaimed,
		fmt.Sprintf("Claimed host %s (%s)", host.Name, host.Spec.Address), hostMachine.Generation)

	bootstrapData, err := r.bootstrapData(ctx, machine)
	if err != nil {
		return ctrl.Result{}, err
	}

	providerID := ProviderID(host)
	hostMachine.Spec.ProviderID = ptr(providerID)
	hostMachine.Status.Addresses = addressesFor(host)

	script, err := cloudinit.Render(bootstrapData, cloudinit.Options{ProviderID: providerID})
	if err != nil {
		// Unsupported bootstrap data will never render, so this is not retried:
		// it needs the KubeadmConfigSpec changed.
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonBootstrapFailed,
			truncate(err.Error(), conditionMessageLimit), hostMachine.Generation)
		logger.Error(err, "Bootstrap data cannot be rendered for this host")
		return ctrl.Result{}, nil
	}
	checksum := provisioner.Checksum(script)

	return r.runBootstrap(ctx, logger, hostMachine, host, script, checksum)
}

// runBootstrap drives the detached run on the host through its states.
func (r *HostMachineReconciler) runBootstrap(
	ctx context.Context,
	logger logr.Logger,
	hostMachine *infrav1.HostMachine,
	host *infrav1.Host,
	script []byte,
	checksum string,
) (ctrl.Result, error) {
	sshCtx, cancel := context.WithTimeout(ctx, sshOperationTimeout)
	defer cancel()

	conn, err := connect(sshCtx, r.Client, host)
	if err != nil {
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonProbeFailed,
			truncate(err.Error(), conditionMessageLimit), hostMachine.Generation)

		if errors.Is(err, ssh.ErrHostKeyMismatch) {
			logger.Error(err, "Host key mismatch; not retrying until the Host is corrected", "host", host.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueWaitingForDependency}, nil
	}
	defer func() { _ = conn.Close() }()

	state, err := provisioner.Status(sshCtx, conn, checksum)
	if err != nil {
		return ctrl.Result{}, err
	}

	switch state.Phase {
	case provisioner.PhaseIdle:
		logger.Info("Starting bootstrap", "host", host.Name, "address", host.Spec.Address, "checksum", checksum[:12])
		if err := provisioner.Start(sshCtx, conn, script, checksum); err != nil {
			setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
				metav1.ConditionFalse, infrav1.ReasonBootstrapFailed,
				truncate(err.Error(), conditionMessageLimit), hostMachine.Generation)
			return ctrl.Result{RequeueAfter: requeueBootstrapFailed}, nil
		}
		hostMachine.Status.BootstrapDataChecksum = checksum
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonWaitingForBootstrapData,
			"Bootstrap script is running on the host", hostMachine.Generation)
		return ctrl.Result{RequeueAfter: requeueAfterStart}, nil

	case provisioner.PhaseRunning:
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonWaitingForBootstrapData,
			"Bootstrap script is running on the host", hostMachine.Generation)
		return ctrl.Result{RequeueAfter: requeueBootstrapRunning}, nil

	case provisioner.PhaseFailed:
		message := fmt.Sprintf("Bootstrap script exited %d. Last lines of %s:\n%s",
			state.ExitCode, provisioner.LogPath, state.LogTail)
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionFalse, infrav1.ReasonBootstrapFailed,
			truncate(message, conditionMessageLimit), hostMachine.Generation)
		logger.Info("Bootstrap failed", "host", host.Name, "exitCode", state.ExitCode)
		// Not retried automatically: re-running kubeadm over a half-configured host
		// rarely helps and obscures the original failure. Deleting the Machine
		// resets the host and starts clean.
		return ctrl.Result{RequeueAfter: requeueBootstrapFailed}, nil

	case provisioner.PhaseSucceeded:
		hostMachine.Status.BootstrapDataChecksum = checksum
		hostMachine.Status.Initialization.Provisioned = ptr(true)
		setCondition(&hostMachine.Status.Conditions, infrav1.HostMachineProvisionedCondition,
			metav1.ConditionTrue, infrav1.ReasonBootstrapSucceeded,
			"Host is bootstrapped", hostMachine.Generation)

		if err := r.markHostProvisioned(ctx, host); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Host is bootstrapped", "host", host.Name, "providerID", *hostMachine.Spec.ProviderID)
		return ctrl.Result{}, nil

	default:
		return ctrl.Result{}, fmt.Errorf("unknown provisioner phase %q", state.Phase)
	}
}

// claimHost returns the Host backing this machine, claiming a free one if needed.
// A nil Host with a nil error means the pool has nothing that matches right now.
func (r *HostMachineReconciler) claimHost(
	ctx context.Context,
	logger logr.Logger,
	cluster *clusterv1.Cluster,
	hostMachine *infrav1.HostMachine,
) (*infrav1.Host, error) {
	if ref := hostMachine.Status.HostRef; ref != nil {
		host := &infrav1.Host{}
		key := types.NamespacedName{Namespace: hostMachine.Namespace, Name: ref.Name}
		if err := r.Get(ctx, key, host); err != nil {
			if apierrors.IsNotFound(err) {
				// The host was removed from the inventory underneath us. Drop the
				// stale reference so a different one can be claimed.
				logger.Info("Claimed host no longer exists, releasing the reference", "host", ref.Name)
				hostMachine.Status.HostRef = nil
				return nil, nil
			}
			return nil, err
		}
		return host, nil
	}

	selector, err := selectorFor(hostMachine.Spec.HostSelector)
	if err != nil {
		return nil, err
	}

	hosts := &infrav1.HostList{}
	if err := r.List(ctx, hosts, client.InNamespace(hostMachine.Namespace)); err != nil {
		return nil, err
	}

	for i := range hosts.Items {
		host := &hosts.Items[i]

		if host.Status.ClaimRef != nil || host.Spec.Unhealthy || !host.DeletionTimestamp.IsZero() {
			continue
		}
		// Only hand out hosts a probe has confirmed: claiming an unreachable host
		// would stall the Machine on a connection that was never going to work.
		if host.Status.Phase != infrav1.HostPhaseAvailable {
			continue
		}
		if !selector.Matches(labels.Set(host.Labels)) {
			continue
		}

		host.Status.ClaimRef = &corev1.ObjectReference{
			APIVersion: infrav1.GroupVersion.String(),
			Kind:       "HostMachine",
			Namespace:  hostMachine.Namespace,
			Name:       hostMachine.Name,
			UID:        hostMachine.UID,
		}
		host.Status.Phase = infrav1.HostPhaseClaimed

		// A plain Update, not a patch: the resourceVersion check is what stops two
		// HostMachines from claiming the same host concurrently.
		if err := r.Status().Update(ctx, host); err != nil {
			if apierrors.IsConflict(err) {
				logger.V(4).Info("Lost the race to claim a host, trying the next one", "host", host.Name)
				continue
			}
			return nil, err
		}

		if err := r.labelClaimedHost(ctx, host, cluster.Name); err != nil {
			return nil, err
		}

		logger.Info("Claimed host", "host", host.Name, "address", host.Spec.Address)
		hostMachine.Status.HostRef = &corev1.LocalObjectReference{Name: host.Name}
		return host, nil
	}

	return nil, nil
}

// labelClaimedHost records cluster ownership on the Host so `kubectl get hosts
// -l kgenesis.io/cluster-name=...` answers "which hosts belong to this cluster".
func (r *HostMachineReconciler) labelClaimedHost(ctx context.Context, host *infrav1.Host, clusterName string) error {
	if host.Labels[infrav1.ClusterNameLabel] == clusterName {
		return nil
	}
	patchHelper, err := patch.NewHelper(host, r.Client)
	if err != nil {
		return err
	}
	if host.Labels == nil {
		host.Labels = map[string]string{}
	}
	host.Labels[infrav1.ClusterNameLabel] = clusterName
	return patchHelper.Patch(ctx, host)
}

func (r *HostMachineReconciler) markHostProvisioned(ctx context.Context, host *infrav1.Host) error {
	if host.Status.Phase == infrav1.HostPhaseProvisioned {
		return nil
	}
	host.Status.Phase = infrav1.HostPhaseProvisioned
	return r.Status().Update(ctx, host)
}

func (r *HostMachineReconciler) bootstrapData(ctx context.Context, machine *clusterv1.Machine) ([]byte, error) {
	key := types.NamespacedName{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("get bootstrap secret %s: %w", key, err)
	}

	data, ok := secret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("bootstrap secret %s has no %q key", key, "value")
	}

	// Ignition would need a different renderer; failing here is clearer than
	// feeding JSON to a cloud-config parser.
	if format, ok := secret.Data["format"]; ok && string(format) != "" && string(format) != "cloud-config" {
		return nil, fmt.Errorf("bootstrap secret %s has format %q; kgenesis renders cloud-config",
			key, string(format))
	}
	return data, nil
}

// reconcileDelete resets the host and returns it to the pool.
func (r *HostMachineReconciler) reconcileDelete(
	ctx context.Context,
	logger logr.Logger,
	hostMachine *infrav1.HostMachine,
) (ctrl.Result, error) {
	ref := hostMachine.Status.HostRef
	if ref == nil {
		controllerutil.RemoveFinalizer(hostMachine, infrav1.MachineFinalizer)
		return ctrl.Result{}, nil
	}

	host := &infrav1.Host{}
	key := types.NamespacedName{Namespace: hostMachine.Namespace, Name: ref.Name}
	switch err := r.Get(ctx, key, host); {
	case apierrors.IsNotFound(err):
		// Nothing left to release.
		hostMachine.Status.HostRef = nil
		controllerutil.RemoveFinalizer(hostMachine, infrav1.MachineFinalizer)
		return ctrl.Result{}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	sshCtx, cancel := context.WithTimeout(ctx, sshOperationTimeout)
	defer cancel()

	if conn, err := connect(sshCtx, r.Client, host); err != nil {
		// An unreachable host cannot be reset, and blocking deletion on that would
		// leave the Machine stuck forever on hardware that may simply be powered
		// off. The host is returned to the pool as Pending so the next probe
		// decides whether it is usable, and its state is left for an operator.
		logger.Info("Could not reach the host to reset it; releasing it as unverified",
			"host", host.Name, "error", err.Error())
	} else {
		err := provisioner.Reset(sshCtx, conn)
		_ = conn.Close()
		if err != nil {
			logger.Error(err, "Reset did not complete cleanly; releasing the host anyway", "host", host.Name)
		}
	}

	if err := r.releaseHost(ctx, host); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Released host back to the pool", "host", host.Name)
	hostMachine.Status.HostRef = nil
	controllerutil.RemoveFinalizer(hostMachine, infrav1.MachineFinalizer)
	return ctrl.Result{}, nil
}

func (r *HostMachineReconciler) releaseHost(ctx context.Context, host *infrav1.Host) error {
	delete(host.Labels, infrav1.ClusterNameLabel)
	if err := r.Update(ctx, host); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	host.Status.ClaimRef = nil
	// Pending rather than Available: only a fresh probe may declare it usable.
	host.Status.Phase = infrav1.HostPhasePending
	if err := r.Status().Update(ctx, host); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// ProviderID is the stable identity kgenesis gives a host. The same value goes
// into the kubelet's --provider-id, which is what lets Cluster API match a Node
// back to its Machine.
func ProviderID(host *infrav1.Host) string {
	return ProviderIDPrefix + host.Namespace + "/" + host.Name
}

func selectorFor(hostSelector *infrav1.HostSelector) (labels.Selector, error) {
	if hostSelector == nil {
		return labels.Everything(), nil
	}
	return metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels:      hostSelector.MatchLabels,
		MatchExpressions: hostSelector.MatchExpressions,
	})
}

func addressesFor(host *infrav1.Host) []clusterv1.MachineAddress {
	addresses := []clusterv1.MachineAddress{
		{Type: clusterv1.MachineInternalIP, Address: host.Spec.Address},
	}
	if host.Status.SystemInfo != nil && host.Status.SystemInfo.Hostname != "" {
		addresses = append(addresses, clusterv1.MachineAddress{
			Type:    clusterv1.MachineHostName,
			Address: host.Status.SystemInfo.Hostname,
		})
	}
	return addresses
}

func ptrTo(b *bool) bool { return b != nil && *b }

// SetupWithManager registers the controller. HostMachines are also reconciled
// when their owning Machine changes, which is how bootstrap data becoming
// available wakes the machine up instead of waiting out a requeue.
func (r *HostMachineReconciler) SetupWithManager(mgr ctrl.Manager, opts controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.HostMachine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(machineToHostMachine),
		).
		WithOptions(opts).
		Named("hostmachine").
		Complete(r)
}

func machineToHostMachine(_ context.Context, obj client.Object) []reconcile.Request {
	machine, ok := obj.(*clusterv1.Machine)
	if !ok {
		return nil
	}
	if machine.Spec.InfrastructureRef.Kind != "HostMachine" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: machine.Namespace,
			Name:      machine.Spec.InfrastructureRef.Name,
		},
	}}
}
