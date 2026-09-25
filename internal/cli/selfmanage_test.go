// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/config"
)

func poolHost(name, role string, free bool) *infrav1.Host {
	host := &infrav1.Host{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "lab",
			Labels:    map[string]string{infrav1.RoleLabel: role},
		},
		Status: infrav1.HostStatus{Phase: infrav1.HostPhaseAvailable},
	}
	if !free {
		host.Status.Phase = infrav1.HostPhaseProvisioned
		host.Status.ClaimRef = &corev1.ObjectReference{Name: name + "-machine"}
	}
	return host
}

func selfManageConfig(replicas int32, workerPools int) *config.Config {
	cfg := &config.Config{}
	cfg.Cluster.Name = "lab"
	cfg.Cluster.Namespace = "lab"
	cfg.Cluster.ControlPlaneReplicas = replicas
	for i := 0; i < workerPools; i++ {
		cfg.Workers = append(cfg.Workers, config.WorkerPool{Name: "default", Replicas: 1})
	}
	return cfg
}

// A cluster that manages itself has to replace the machines its own controllers
// run on. Letting it take that on without the room to do it produces the worst
// state available: controllers that can see what is wrong and cannot act.
func TestCheckSelfManageable(t *testing.T) {
	cases := map[string]struct {
		replicas    int32
		workerPools int
		hosts       []client.Object
		wantIn      string
	}{
		"three replicas and a spare of each role": {
			replicas:    3,
			workerPools: 1,
			hosts: []client.Object{
				poolHost("cp-1", infrav1.RoleControlPlane, false),
				poolHost("cp-4", infrav1.RoleControlPlane, true),
				poolHost("w-1", infrav1.RoleWorker, false),
				poolHost("w-2", infrav1.RoleWorker, true),
			},
		},
		"one control plane replica": {
			replicas:    1,
			workerPools: 1,
			hosts: []client.Object{
				poolHost("cp-2", infrav1.RoleControlPlane, true),
				poolHost("w-2", infrav1.RoleWorker, true),
			},
			wantIn: "etcd needs 3 to keep quorum",
		},
		"no free control plane host": {
			replicas:    3,
			workerPools: 1,
			hosts: []client.Object{
				poolHost("cp-1", infrav1.RoleControlPlane, false),
				poolHost("w-2", infrav1.RoleWorker, true),
			},
			wantIn: "no control plane host is free",
		},
		"no free worker host": {
			replicas:    3,
			workerPools: 1,
			hosts: []client.Object{
				poolHost("cp-4", infrav1.RoleControlPlane, true),
				poolHost("w-1", infrav1.RoleWorker, false),
			},
			wantIn: "no worker host is free",
		},
		// Nothing rolls a pool that does not exist.
		"no worker pools at all": {
			replicas:    3,
			workerPools: 0,
			hosts: []client.Object{
				poolHost("cp-4", infrav1.RoleControlPlane, true),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().
				WithScheme(testScheme(t)).
				WithObjects(tc.hosts...).
				Build()

			err := checkSelfManageable(context.Background(), c, selfManageConfig(tc.replicas, tc.workerPools))

			if tc.wantIn == "" {
				if err != nil {
					t.Fatalf("expected the cluster to qualify, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected the handover to be refused")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("expected %q in the refusal, got:\n%v", tc.wantIn, err)
			}
		})
	}
}
