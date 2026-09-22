// Package v1alpha1 contains the kgenesis infrastructure provider API types.
//
// The provider implements the Cluster API v1beta2 infrastructure contract for
// pre-provisioned hosts: machines are not created on demand, they are claimed
// from a pool of Host objects and bootstrapped over SSH.
//
// +kubebuilder:object:generate=true
// +groupName=infrastructure.kgenesis.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "infrastructure.kgenesis.io", Version: "v1alpha1"}

	// SchemeBuilder registers the Go types with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group version to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
