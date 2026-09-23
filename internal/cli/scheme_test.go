// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
)

// testScheme knows every type the commands read, which is what a fake client
// needs to stand in for a management cluster.
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
