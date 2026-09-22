package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// HostClusterSpec defines the desired state of a HostCluster.
//
// There is no infrastructure to create: the hosts already exist. The only thing
// this resource carries is the control plane endpoint, which on bare metal is a
// VIP served by kube-vip or an external load balancer.
type HostClusterSpec struct {
	// controlPlaneEndpoint is the address the workload cluster's API server is
	// reachable at. kgenesis does not allocate it; it must already be routable to
	// the control plane hosts (typically a kube-vip managed VIP).
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty,omitzero"`
}

// HostClusterInitializationStatus satisfies the Cluster API v1beta2 infrastructure
// cluster contract.
type HostClusterInitializationStatus struct {
	// provisioned is true when the cluster infrastructure is ready. For this
	// provider that means the control plane endpoint is set.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// HostClusterStatus reports the observed state of a HostCluster.
type HostClusterStatus struct {
	// initialization reports the contract-mandated provisioning state.
	// +optional
	Initialization HostClusterInitializationStatus `json:"initialization,omitempty,omitzero"`

	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta2=v1alpha1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=infrastructure-kgenesis"
// +kubebuilder:resource:path=hostclusters,scope=Namespaced,shortName=hcl,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Provisioned",type=boolean,JSONPath=`.status.initialization.provisioned`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.controlPlaneEndpoint.host`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HostCluster is the infrastructure cluster for a pool of pre-provisioned hosts.
type HostCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HostClusterSpec   `json:"spec,omitempty"`
	Status HostClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// HostClusterList contains a list of HostCluster.
type HostClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HostCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HostCluster{}, &HostClusterList{})
}
