package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// HostSelector narrows which Hosts a HostMachine may claim.
type HostSelector struct {
	// matchLabels requires the Host's labels to contain all of these entries.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// matchExpressions is a list of label selector requirements evaluated against
	// the Host's labels.
	// +optional
	MatchExpressions []metav1.LabelSelectorRequirement `json:"matchExpressions,omitempty"`
}

// HostMachineSpec defines the desired state of a HostMachine.
type HostMachineSpec struct {
	// providerID is set by the controller once the Host is claimed, in the form
	// kgenesis://<host-namespace>/<host-name>. The same value is written into the
	// kubelet's --provider-id so the Node and the Machine can be correlated.
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// hostSelector restricts the pool this machine draws from. An empty selector
	// matches any Available Host in the namespace.
	// +optional
	HostSelector *HostSelector `json:"hostSelector,omitempty"`
}

// HostMachineInitializationStatus satisfies the Cluster API v1beta2 infrastructure
// machine contract.
type HostMachineInitializationStatus struct {
	// provisioned is true once the bootstrap data has been applied on the host and
	// kubeadm reported success.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// HostMachineStatus reports the observed state of a HostMachine.
type HostMachineStatus struct {
	// initialization reports the contract-mandated provisioning state.
	// +optional
	Initialization HostMachineInitializationStatus `json:"initialization,omitempty,omitzero"`

	// hostRef points at the claimed Host.
	// +optional
	HostRef *corev1.LocalObjectReference `json:"hostRef,omitempty"`

	// addresses are the addresses of the claimed Host, surfaced on the Machine.
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// bootstrapDataChecksum is the SHA-256 of the bootstrap data that was applied.
	// It makes the SSH bootstrap step idempotent across controller restarts.
	// +optional
	BootstrapDataChecksum string `json:"bootstrapDataChecksum,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta2=v1alpha1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=infrastructure-kgenesis"
// +kubebuilder:resource:path=hostmachines,scope=Namespaced,shortName=hma,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provisioned",type=boolean,JSONPath=`.status.initialization.provisioned`
// +kubebuilder:printcolumn:name="Host",type=string,JSONPath=`.status.hostRef.name`
// +kubebuilder:printcolumn:name="ProviderID",type=string,JSONPath=`.spec.providerID`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HostMachine is a Cluster API Machine backed by a claimed Host.
type HostMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostMachineSpec   `json:"spec,omitempty"`
	Status HostMachineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HostMachineList contains a list of HostMachine.
type HostMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostMachine{}, &HostMachineList{})
}
