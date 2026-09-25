// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
)

// HostClusterReconciler satisfies the Cluster API infrastructure cluster contract.
//
// With pre-provisioned hosts there is nothing to create: no VPC, no load
// balancer, no machines to allocate. The control plane endpoint is supplied by
// the operator, either as a VIP that kube-vip raises on the control plane hosts
// or as an external load balancer. So this reconciler's whole job is to confirm
// the endpoint is set and report the cluster provisioned, which unblocks the
// Machine controllers.
type HostClusterReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hostclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.kgenesis.io,resources=hostclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch

func (r *HostClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	hostCluster := &infrav1.HostCluster{}
	if err := r.Get(ctx, req.NamespacedName, hostCluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cluster, err := util.GetOwnerCluster(ctx, r.Client, hostCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		logger.V(4).Info("Waiting for the Cluster owner reference to be set")
		return ctrl.Result{}, nil
	}

	if annotations.IsPaused(cluster, hostCluster) {
		logger.V(4).Info("Reconciliation is paused")
		return ctrl.Result{}, nil
	}

	helper, err := patch.NewHelper(hostCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := helper.Patch(ctx, hostCluster); err != nil && reterr == nil {
			reterr = err
		}
	}()

	if !hostCluster.DeletionTimestamp.IsZero() {
		// The hosts outlive the cluster by definition; releasing them is the
		// HostMachine reconciler's job, one machine at a time.
		controllerutil.RemoveFinalizer(hostCluster, infrav1.ClusterFinalizer)
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(hostCluster, infrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(hostCluster, infrav1.ClusterFinalizer)
	}

	endpoint := hostCluster.Spec.ControlPlaneEndpoint
	if endpoint.Host == "" || endpoint.Port == 0 {
		hostCluster.Status.Initialization.Provisioned = ptr(false)
		setCondition(&hostCluster.Status.Conditions, "Ready", metav1.ConditionFalse,
			"MissingControlPlaneEndpoint",
			"spec.controlPlaneEndpoint must be set: kgenesis does not allocate one for pre-provisioned hosts",
			hostCluster.Generation)
		return ctrl.Result{}, nil
	}

	hostCluster.Status.Initialization.Provisioned = ptr(true)
	setCondition(&hostCluster.Status.Conditions, "Ready", metav1.ConditionTrue,
		"ControlPlaneEndpointSet", "Control plane endpoint is set", hostCluster.Generation)

	return ctrl.Result{}, nil
}

// SetupWithManager registers the controller.
// SetupWithManager also watches the owning Cluster.
//
// Reconciliation is skipped while a Cluster is paused, and clusterctl pauses one
// for the length of a move. Without this the unpause at the end reaches the
// Cluster and nothing else: every object arrives in its new home and sits there,
// because the event that said to look again was never delivered.
func (r *HostClusterReconciler) SetupWithManager(mgr ctrl.Manager, opts controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.HostCluster{}).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterToHostCluster),
		).
		WithOptions(opts).
		Named("hostcluster").
		Complete(r)
}

func clusterToHostCluster(_ context.Context, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*clusterv1.Cluster)
	if !ok || cluster.Spec.InfrastructureRef.Kind != "HostCluster" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Namespace: cluster.Namespace,
			Name:      cluster.Spec.InfrastructureRef.Name,
		},
	}}
}
