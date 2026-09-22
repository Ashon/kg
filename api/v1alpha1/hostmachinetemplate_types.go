package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HostMachineTemplateResource is the template body.
type HostMachineTemplateResource struct {
	// +optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec HostMachineSpec `json:"spec"`
}

// HostMachineTemplateSpec defines the desired state of a HostMachineTemplate.
type HostMachineTemplateSpec struct {
	// +required
	Template HostMachineTemplateResource `json:"template"`
}

// +kubebuilder:object:root=true
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta2=v1alpha1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=infrastructure-kgenesis"
// +kubebuilder:resource:path=hostmachinetemplates,scope=Namespaced,shortName=hmt,categories=cluster-api
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HostMachineTemplate is the template KubeadmControlPlane and MachineDeployment
// stamp HostMachines from.
type HostMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec HostMachineTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// HostMachineTemplateList contains a list of HostMachineTemplate.
type HostMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostMachineTemplate{}, &HostMachineTemplateList{})
}
