// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/config"
)

func TestImageRecipientsIncludeSparesButExcludeOtherClusters(t *testing.T) {
	claimed := poolHost("cp-1", infrav1.RoleControlPlane, false)
	claimed.Labels[infrav1.ClusterNameLabel] = "lab"
	spare := poolHost("cp-4", infrav1.RoleControlPlane, true)
	other := poolHost("other", infrav1.RoleWorker, false)
	other.Labels[infrav1.ClusterNameLabel] = "another"
	unknown := poolHost("unknown", infrav1.RoleWorker, false)
	unknown.Status.ClaimRef = &corev1.ObjectReference{Name: "unidentified-owner"}
	unhealthy := poolHost("unhealthy", infrav1.RoleWorker, true)
	unhealthy.Spec.Unhealthy = true
	outside := poolHost("outside", infrav1.RoleWorker, true)
	outside.Namespace = "another"
	deleting := poolHost("deleting", infrav1.RoleWorker, true)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{infrav1.HostFinalizer}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(claimed, spare, other, unknown, unhealthy, outside, deleting).Build()
	cfg := &config.Config{Cluster: config.ClusterConfig{Name: "lab", Namespace: "lab"}}
	for _, name := range []string{"cp-1", "cp-4", "other", "unknown", "unhealthy", "outside", "deleting", "not-loaded"} {
		cfg.Hosts = append(cfg.Hosts, config.HostConfig{Name: name})
	}
	got, err := imageHosts(context.Background(), c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "cp-1" || got[1].Name != "cp-4" {
		t.Fatalf("unexpected recipients: %+v", got)
	}
}
