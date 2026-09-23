// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package v1alpha1

const (
	// ClusterFinalizer blocks HostCluster deletion until its resources are released.
	ClusterFinalizer = "hostcluster.infrastructure.kgenesis.io/finalizer"

	// MachineFinalizer blocks HostMachine deletion until its Host is reset and released.
	MachineFinalizer = "hostmachine.infrastructure.kgenesis.io/finalizer"

	// HostFinalizer blocks Host deletion while it is claimed by a HostMachine.
	HostFinalizer = "host.infrastructure.kgenesis.io/finalizer"
)

const (
	// RoleLabel marks the intended role of a Host. Used by the default host
	// selectors that kgenesis renders: "control-plane" or "worker".
	RoleLabel = "kgenesis.io/role"

	// ClusterNameLabel records which cluster a claimed Host belongs to.
	ClusterNameLabel = "kgenesis.io/cluster-name"
)

// Host roles recognised by the rendered templates.
const (
	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"
)

// Condition types reported by this provider.
const (
	// HostReachableCondition reports whether the SSH probe against the Host succeeds.
	HostReachableCondition = "Reachable"

	// HostMachineProvisionedCondition reports whether the bootstrap data has been
	// applied on the claimed Host.
	HostMachineProvisionedCondition = "Provisioned"

	// HostMachineHostClaimedCondition reports whether a Host was matched and claimed.
	HostMachineHostClaimedCondition = "HostClaimed"

	// HostResetCondition reports whether the kubeadm state was actually removed
	// when the Host was returned to the pool.
	//
	// A host is released whether or not it could be reached, because a Machine
	// that cannot be deleted because its hardware is powered off helps nobody.
	// Without this the two outcomes are indistinguishable, and a pool that looks
	// clean hands a machine still carrying another cluster's certificates and
	// etcd data to the next one.
	HostResetCondition = "Reset"
)

// Condition reasons.
const (
	ReasonWaitingForCluster       = "WaitingForCluster"
	ReasonWaitingForBootstrapData = "WaitingForBootstrapData"
	ReasonNoMatchingHost          = "NoMatchingHost"
	ReasonHostClaimed             = "HostClaimed"
	ReasonBootstrapFailed         = "BootstrapFailed"
	ReasonBootstrapSucceeded      = "BootstrapSucceeded"
	ReasonProbeFailed             = "ProbeFailed"
	ReasonProbeSucceeded          = "ProbeSucceeded"
	ReasonDeleting                = "Deleting"
	ReasonResetSucceeded          = "ResetSucceeded"
	ReasonResetIncomplete         = "ResetIncomplete"
	ReasonHostUnreachable         = "HostUnreachable"
)
