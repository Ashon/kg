// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		clusterv1.AddToScheme,
		infrav1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("build scheme: %v", err)
		}
	}
	return scheme
}

func availableHost(name, address, role string) *infrav1.Host {
	return &infrav1.Host{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{infrav1.RoleLabel: role},
		},
		Spec:   infrav1.HostSpec{Address: address, User: "root"},
		Status: infrav1.HostStatus{Phase: infrav1.HostPhaseAvailable},
	}
}

func controlPlaneMachine(name string, uid types.UID) *infrav1.HostMachine {
	return &infrav1.HostMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: uid},
		Spec: infrav1.HostMachineSpec{
			HostSelector: &infrav1.HostSelector{
				MatchLabels: map[string]string{infrav1.RoleLabel: infrav1.RoleControlPlane},
			},
		},
	}
}

func newReconciler(t *testing.T, objects ...client.Object) *HostMachineReconciler {
	t.Helper()

	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(objects...).
		WithStatusSubresource(&infrav1.Host{}, &infrav1.HostMachine{}).
		Build()
	return &HostMachineReconciler{Client: c}
}

// Claiming writes to two objects: the Host records who took it, and the
// HostMachine records what it took. If the second write is lost - a conflict, a
// restart - the machine must find its way back to the host it already has.
// Claiming a second one would strand the first outside the pool for good.
func TestClaimHostRecoversAClaimTheMachineForgot(t *testing.T) {
	const machineUID = types.UID("machine-uid")

	claimed := availableHost("cp-1", "10.0.0.11", infrav1.RoleControlPlane)
	claimed.Status.Phase = infrav1.HostPhaseClaimed
	claimed.Status.ClaimRef = &corev1.ObjectReference{
		APIVersion: infrav1.GroupVersion.String(),
		Kind:       "HostMachine",
		Namespace:  "default",
		Name:       "cp-machine",
		UID:        machineUID,
	}

	spare := availableHost("cp-2", "10.0.0.12", infrav1.RoleControlPlane)

	// The machine has no hostRef: exactly the state a lost status write leaves.
	machine := controlPlaneMachine("cp-machine", machineUID)
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "lab", Namespace: "default"}}

	r := newReconciler(t, claimed, spare, machine, cluster)

	host, err := r.claimHost(t.Context(), log.Log, cluster, machine)
	if err != nil {
		t.Fatalf("claimHost: %v", err)
	}
	if host == nil {
		t.Fatal("expected the existing claim to be recovered, got no host")
	}
	if host.Name != "cp-1" {
		t.Errorf("recovered %s, want the host it already had, cp-1", host.Name)
	}
	if machine.Status.HostRef == nil || machine.Status.HostRef.Name != "cp-1" {
		t.Errorf("hostRef is %v, want cp-1", machine.Status.HostRef)
	}

	// The spare must be untouched.
	var after infrav1.Host
	if err := r.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "cp-2"}, &after); err != nil {
		t.Fatalf("get cp-2: %v", err)
	}
	if after.Status.ClaimRef != nil {
		t.Errorf("cp-2 was claimed as well: %v", after.Status.ClaimRef)
	}
}

// A claim by a different machine is not this machine's to recover.
func TestClaimHostIgnoresAnotherMachinesClaim(t *testing.T) {
	taken := availableHost("cp-1", "10.0.0.11", infrav1.RoleControlPlane)
	taken.Status.Phase = infrav1.HostPhaseClaimed
	taken.Status.ClaimRef = &corev1.ObjectReference{
		Kind: "HostMachine", Namespace: "default", Name: "other", UID: types.UID("other-uid"),
	}

	spare := availableHost("cp-2", "10.0.0.12", infrav1.RoleControlPlane)
	machine := controlPlaneMachine("cp-machine", types.UID("machine-uid"))
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "lab", Namespace: "default"}}

	r := newReconciler(t, taken, spare, machine, cluster)

	host, err := r.claimHost(t.Context(), log.Log, cluster, machine)
	if err != nil {
		t.Fatalf("claimHost: %v", err)
	}
	if host == nil || host.Name != "cp-2" {
		t.Fatalf("claimed %v, want the free host cp-2", host)
	}
	if machine.Status.HostRef == nil || machine.Status.HostRef.Name != "cp-2" {
		t.Errorf("hostRef is %v, want cp-2", machine.Status.HostRef)
	}
}

// The reference has to be recorded before anything else can fail, so the claim
// and the machine's memory of it cannot drift apart.
func TestClaimHostRecordsTheReferenceOnTheMachine(t *testing.T) {
	free := availableHost("cp-1", "10.0.0.11", infrav1.RoleControlPlane)
	machine := controlPlaneMachine("cp-machine", types.UID("machine-uid"))
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "lab", Namespace: "default"}}

	r := newReconciler(t, free, machine, cluster)

	host, err := r.claimHost(t.Context(), log.Log, cluster, machine)
	if err != nil {
		t.Fatalf("claimHost: %v", err)
	}
	if host == nil {
		t.Fatal("expected a host to be claimed")
	}
	if machine.Status.HostRef == nil || machine.Status.HostRef.Name != host.Name {
		t.Errorf("hostRef is %v, want %s", machine.Status.HostRef, host.Name)
	}

	var after infrav1.Host
	if err := r.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "cp-1"}, &after); err != nil {
		t.Fatalf("get cp-1: %v", err)
	}
	if after.Status.ClaimRef == nil || after.Status.ClaimRef.UID != machine.UID {
		t.Errorf("the host does not record this machine: %v", after.Status.ClaimRef)
	}
	if after.Status.Phase != infrav1.HostPhaseClaimed {
		t.Errorf("host phase is %s, want %s", after.Status.Phase, infrav1.HostPhaseClaimed)
	}
}

// Only hosts a probe has confirmed may be handed out.
func TestClaimHostSkipsHostsThatAreNotAvailable(t *testing.T) {
	unreachable := availableHost("cp-1", "10.0.0.11", infrav1.RoleControlPlane)
	unreachable.Status.Phase = infrav1.HostPhaseUnreachable

	unhealthy := availableHost("cp-2", "10.0.0.12", infrav1.RoleControlPlane)
	unhealthy.Spec.Unhealthy = true

	wrongRole := availableHost("w-1", "10.0.0.21", infrav1.RoleWorker)

	machine := controlPlaneMachine("cp-machine", types.UID("machine-uid"))
	cluster := &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "lab", Namespace: "default"}}

	r := newReconciler(t, unreachable, unhealthy, wrongRole, machine, cluster)

	host, err := r.claimHost(t.Context(), log.Log, cluster, machine)
	if err != nil {
		t.Fatalf("claimHost: %v", err)
	}
	if host != nil {
		t.Errorf("claimed %s, but nothing in the pool is usable", host.Name)
	}
}
