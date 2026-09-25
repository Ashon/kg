// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HostPhase is the lifecycle phase of a Host in the pool.
type HostPhase string

const (
	// HostPhasePending means the Host has not been probed yet.
	HostPhasePending HostPhase = "Pending"
	// HostPhaseAvailable means the Host is reachable and free to be claimed.
	HostPhaseAvailable HostPhase = "Available"
	// HostPhaseClaimed means a HostMachine has claimed the Host but bootstrap is not finished.
	HostPhaseClaimed HostPhase = "Claimed"
	// HostPhaseProvisioned means the Host has been bootstrapped and joined its cluster.
	HostPhaseProvisioned HostPhase = "Provisioned"
	// HostPhaseUnreachable means the SSH probe failed.
	HostPhaseUnreachable HostPhase = "Unreachable"
	// HostPhaseReleasing means the Host is being reset before returning to the pool.
	HostPhaseReleasing HostPhase = "Releasing"
)

// HostKeyPolicy controls how the SSH host key is verified.
type HostKeyPolicy string

const (
	// HostKeyPolicyStrict requires spec.publicKey to match the key presented by the host.
	HostKeyPolicyStrict HostKeyPolicy = "Strict"
	// HostKeyPolicyTOFU pins the key seen on the first successful connection into
	// status.observedPublicKey and requires every later connection to match it.
	HostKeyPolicyTOFU HostKeyPolicy = "TOFU"
	// HostKeyPolicyInsecure accepts any host key. Lab use only.
	HostKeyPolicyInsecure HostKeyPolicy = "Insecure"
)

// HostSpec describes how to reach a pre-provisioned machine.
type HostSpec struct {
	// address is the IP address or DNS name kg connects to over SSH. It is
	// also used as the node's advertised address unless the bootstrap data
	// overrides it.
	// +required
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`

	// port is the SSH port.
	// +optional
	// +kubebuilder:default=22
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port,omitempty"`

	// user is the SSH login user. It must be root or hold passwordless sudo.
	// +required
	// +kubebuilder:validation:MinLength=1
	User string `json:"user"`

	// sshSecretRef references a Secret in the same namespace holding the SSH
	// credentials. Recognised keys: "private_key" (PEM), "passphrase" (optional,
	// for an encrypted key) and "password" (used when no private key is given).
	// +required
	SSHSecretRef corev1.LocalObjectReference `json:"sshSecretRef"`

	// hostKeyPolicy selects how the SSH host key is verified.
	// +optional
	// +kubebuilder:default=TOFU
	// +kubebuilder:validation:Enum=Strict;TOFU;Insecure
	HostKeyPolicy HostKeyPolicy `json:"hostKeyPolicy,omitempty"`

	// publicKey is the expected SSH host key in authorized_keys format. Required
	// when hostKeyPolicy is Strict.
	// +optional
	PublicKey string `json:"publicKey,omitempty"`

	// unhealthy takes the Host out of the pool without deleting it. Existing
	// claims are left alone.
	// +optional
	Unhealthy bool `json:"unhealthy,omitempty"`
}

// HostSystemInfo is what the probe learned about the machine.
type HostSystemInfo struct {
	// +optional
	Hostname string `json:"hostname,omitempty"`
	// +optional
	OSImage string `json:"osImage,omitempty"`
	// +optional
	KernelVersion string `json:"kernelVersion,omitempty"`
	// +optional
	Architecture string `json:"architecture,omitempty"`
	// +optional
	CPUCores int32 `json:"cpuCores,omitempty"`
	// +optional
	MemoryMB int64 `json:"memoryMB,omitempty"`
	// +optional
	ContainerRuntime string `json:"containerRuntime,omitempty"`
}

// HostStatus reports the observed state of a Host.
type HostStatus struct {
	// phase is a one-word summary for humans and for `kg inventory list`.
	// +optional
	Phase HostPhase `json:"phase,omitempty"`

	// claimRef points at the HostMachine currently holding this Host.
	// +optional
	ClaimRef *corev1.ObjectReference `json:"claimRef,omitempty"`

	// observedPublicKey is the host key pinned by the TOFU policy.
	// +optional
	ObservedPublicKey string `json:"observedPublicKey,omitempty"`

	// lastProbeTime is when the SSH probe last ran.
	// +optional
	LastProbeTime *metav1.Time `json:"lastProbeTime,omitempty"`

	// systemInfo is what the last successful probe reported.
	// +optional
	SystemInfo *HostSystemInfo `json:"systemInfo,omitempty"`

	// conditions holds the Reachable condition.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=infrastructure-kgenesis"
// clusterctl discovers only the CRDs carrying its own label, and moves only the
// objects it can reach from a Cluster. A Host hangs off no Cluster - it is the
// pool a Cluster draws from - so it is marked for a forced move as well.
// Without both, an ejected cluster arrives with HostMachines whose Hosts stayed
// behind on a genesis node that is about to be deleted.
// +kubebuilder:metadata:labels="clusterctl.cluster.x-k8s.io="
// +kubebuilder:metadata:labels="clusterctl.cluster.x-k8s.io/move="
// +kubebuilder:resource:path=hosts,scope=Namespaced,shortName=hst,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.spec.address`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Claimed By",type=string,JSONPath=`.status.claimRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Host is one pre-provisioned physical or virtual machine in the pool.
type Host struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostSpec   `json:"spec,omitempty"`
	Status HostStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HostList contains a list of Host.
type HostList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Host `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Host{}, &HostList{})
}
