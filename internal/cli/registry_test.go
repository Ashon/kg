// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/kube"
	"github.com/Ashon/kg/internal/registry"
)

func rememberTestCluster(t *testing.T, opts *Options, ns, name string, mode registry.Mode) *registry.Record {
	t.Helper()
	cfg := &config.Config{Cluster: config.ClusterConfig{Name: name, Namespace: ns, ControlPlaneEndpoint: config.Endpoint{Host: "192.0.2.1", Port: 6443}}}
	if err := opts.remember(cfg, mode); err != nil {
		t.Fatal(err)
	}
	r, err := opts.registry().Get(ns, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClustersRetainAllModesWithoutGenesis(t *testing.T) {
	opts := &Options{StateDir: t.TempDir(), ConfigPath: "missing-config"}
	for _, mode := range []registry.Mode{registry.Managed, registry.SelfManaged, registry.Released} {
		rememberTestCluster(t, opts, string(mode), string(mode), mode)
	}
	var out bytes.Buffer
	cmd := newClustersCommand(opts)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--offline"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"MANAGEMENT", "managed", "self-managed", "released"} {
		if !strings.Contains(out.String(), word) {
			t.Fatalf("missing %s: %s", word, out.String())
		}
	}
}

func TestSelectionAndKubeconfigPathsAreNamespaceScoped(t *testing.T) {
	opts := &Options{StateDir: t.TempDir(), ConfigPath: "missing-config"}
	a := rememberTestCluster(t, opts, "alpha", "lab", registry.Released)
	b := rememberTestCluster(t, opts, "beta", "lab", registry.SelfManaged)
	if a.WorkloadKubeconfig == b.WorkloadKubeconfig {
		t.Fatal("kubeconfig paths collide")
	}
	if _, err := opts.selectRecord("lab"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	got, err := opts.selectRecord("beta/lab")
	if err != nil || got.Mode != registry.SelfManaged {
		t.Fatalf("selection = %+v %v", got, err)
	}
}

func TestReleasedKubeconfigIgnoresAnotherClustersGenesis(t *testing.T) {
	opts := &Options{StateDir: t.TempDir(), ConfigPath: "missing-config"}
	r := rememberTestCluster(t, opts, "lab", "lab", registry.Released)
	if err := os.WriteFile(r.WorkloadKubeconfig, []byte("saved workload credentials\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.BootstrapKubeconfig(), []byte("invalid bootstrap kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newKubeconfigCommand(opts)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--cluster", "lab", "--stdout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "saved workload credentials\n" {
		t.Fatal(out.String())
	}
	cfg := &config.Config{Cluster: config.ClusterConfig{Name: "lab", Namespace: "lab"}}
	path, err := opts.requireWorkloadKubeconfig(context.Background(), cfg)
	if err != nil || path != r.WorkloadKubeconfig {
		t.Fatalf("workload path: %q %v", path, err)
	}
}

func TestDiscoveryDoesNotReclassifyReleasedOrPausedClusters(t *testing.T) {
	opts := &Options{StateDir: t.TempDir()}
	r := rememberTestCluster(t, opts, "released", "released", registry.Released)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		&clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "released", Namespace: "released"}, Spec: clusterv1.ClusterSpec{Paused: ptr.To(true)}},
		&clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "maintenance", Namespace: "maintenance"}, Spec: clusterv1.ClusterSpec{Paused: ptr.To(true)}},
	).Build()
	rows := map[string]clusterRow{registry.Key(r.Namespace, r.Name): {record: *r}}
	if err := discoverClusters(context.Background(), c, opts, rows); err != nil {
		t.Fatal(err)
	}
	released, _ := opts.registry().Get("released", "released")
	paused, _ := opts.registry().Get("maintenance", "maintenance")
	if released.Mode != registry.Released || paused.Mode != registry.Managed {
		t.Fatalf("modes changed: %+v %+v", released, paused)
	}
	if rows[registry.Key("maintenance", "maintenance")].phase != "Paused" {
		t.Fatal("paused health not represented")
	}
}

func TestGenesisMutationsRefuseEjectedIdentities(t *testing.T) {
	opts := &Options{StateDir: t.TempDir()}
	cfg := &config.Config{Cluster: config.ClusterConfig{Name: "lab", Namespace: "lab"}}
	for _, mode := range []registry.Mode{registry.Managed, registry.SelfManaged, registry.Released} {
		rememberTestCluster(t, opts, "lab", "lab", mode)
		err := opts.requireGenesisManaged(cfg)
		if (err == nil) != (mode == registry.Managed) {
			t.Fatalf("mode %s: %v", mode, err)
		}
	}
}

func TestForgetKeepsKubeconfigAndOtherRecords(t *testing.T) {
	opts := &Options{StateDir: t.TempDir()}
	r := rememberTestCluster(t, opts, "alpha", "alpha", registry.Released)
	rememberTestCluster(t, opts, "beta", "beta", registry.Managed)
	if err := os.WriteFile(r.WorkloadKubeconfig, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newClusterForgetCommand(opts)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--cluster", "alpha", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if r, err := opts.registry().Get("alpha", "alpha"); err != nil || r != nil {
		t.Fatalf("record remains: %+v %v", r, err)
	}
	if _, err := os.Stat(r.WorkloadKubeconfig); err != nil {
		t.Fatal(err)
	}
	if r, err := opts.registry().Get("beta", "beta"); err != nil || r == nil {
		t.Fatalf("other record gone: %+v %v", r, err)
	}
}

func TestStatusReadsCurrentReplicaTargets(t *testing.T) {
	scheme, err := kube.Scheme()
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{clusterv1.ClusterNameLabel: "lab"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&controlplanev1.KubeadmControlPlane{ObjectMeta: metav1.ObjectMeta{Name: "cp", Namespace: "lab", Labels: labels}, Spec: controlplanev1.KubeadmControlPlaneSpec{Replicas: ptr.To(int32(3))}},
		&clusterv1.MachineDeployment{ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: "lab", Labels: labels}, Spec: clusterv1.MachineDeploymentSpec{Replicas: ptr.To(int32(7))}},
	).Build()
	cfg, err := liveClusterConfig(context.Background(), c, &registry.Record{Name: "lab", Namespace: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cluster.ControlPlaneReplicas != 3 || totalWorkers(cfg) != 7 {
		t.Fatalf("stale targets: %+v", cfg)
	}
}

// A real HTTP client verifies that a released cluster needs only the core API,
// and that a missing or unrelated genesis kubeconfig is never consulted.
func TestReleasedStatusUsesWorkloadAPI(t *testing.T) {
	opts := &Options{StateDir: t.TempDir(), ConfigPath: "missing-config"}
	r := rememberTestCluster(t, opts, "lab", "lab", registry.Released)
	var nodeRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/api":
			fmt.Fprint(w, `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`)
		case "/apis":
			fmt.Fprint(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`)
		case "/api/v1":
			fmt.Fprint(w, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"v1","resources":[{"name":"nodes","kind":"Node","namespaced":false,"verbs":["list"]}]}`)
		case "/api/v1/nodes":
			nodeRequests++
			json.NewEncoder(w).Encode(&corev1.NodeList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}, Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.33.1"}}}}})
		default:
			t.Errorf("unexpected API request: %s", req.URL.Path)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	writeTestKubeconfig(t, r.WorkloadKubeconfig, server.URL)
	if err := os.WriteFile(opts.BootstrapKubeconfig(), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newClusterStatusCommand(opts)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--cluster", "lab"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if nodeRequests != 1 || !strings.Contains(out.String(), "released") || !strings.Contains(out.String(), "1/1 ready") {
		t.Fatal(out.String())
	}
}

func writeTestKubeconfig(t *testing.T, path, server string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: %s
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user: {}
`, server)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSelfManagedStatusIgnoresUnrelatedGenesis(t *testing.T) {
	opts := &Options{StateDir: t.TempDir(), ConfigPath: "missing-config"}
	r := rememberTestCluster(t, opts, "lab", "lab", registry.SelfManaged)
	resourceGroups := map[string][]metav1.APIResource{
		clusterv1.GroupVersion.String():       {{Name: "clusters", Kind: "Cluster", Namespaced: true}, {Name: "machines", Kind: "Machine", Namespaced: true}, {Name: "machinedeployments", Kind: "MachineDeployment", Namespaced: true}},
		controlplanev1.GroupVersion.String():  {{Name: "kubeadmcontrolplanes", Kind: "KubeadmControlPlane", Namespaced: true}},
		"infrastructure.kgenesis.io/v1alpha1": {{Name: "hosts", Kind: "Host", Namespaced: true}, {Name: "hostmachines", Kind: "HostMachine", Namespaced: true}},
	}
	groups := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "APIGroupList"}}
	for gv := range resourceGroups {
		parts := strings.Split(gv, "/")
		version := metav1.GroupVersionForDiscovery{GroupVersion: gv, Version: parts[1]}
		groups.Groups = append(groups.Groups, metav1.APIGroup{Name: parts[0], Versions: []metav1.GroupVersionForDiscovery{version}, PreferredVersion: version})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		encode := func(v any) {
			if err := json.NewEncoder(w).Encode(v); err != nil {
				t.Error(err)
			}
		}
		switch req.URL.Path {
		case "/api":
			fmt.Fprint(w, `{"kind":"APIVersions","apiVersion":"v1","versions":["v1"]}`)
			return
		case "/api/v1":
			encode(metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{}})
			return
		case "/apis":
			encode(groups)
			return
		}
		for gv, resources := range resourceGroups {
			if req.URL.Path == "/apis/"+gv {
				encode(metav1.APIResourceList{GroupVersion: gv, APIResources: resources})
				return
			}
			for _, resource := range resources {
				prefix := "/apis/" + gv + "/namespaces/lab/" + resource.Name
				if req.URL.Path == prefix+"/lab" && resource.Name == "clusters" {
					fmt.Fprintf(w, `{"apiVersion":%q,"kind":"Cluster","metadata":{"name":"lab","namespace":"lab"},"status":{"phase":"Provisioned"}}`, gv)
					return
				}
				if req.URL.Path != prefix {
					continue
				}
				items := []any{}
				switch resource.Name {
				case "kubeadmcontrolplanes":
					items = append(items, map[string]any{"metadata": map[string]any{"name": "cp", "namespace": "lab"}, "spec": map[string]any{"replicas": 1}})
				case "machines":
					items = append(items, map[string]any{"metadata": map[string]any{"name": "cp", "namespace": "lab", "labels": map[string]string{clusterv1.MachineControlPlaneLabel: ""}}, "status": map[string]any{"phase": "Running"}})
				}
				encode(map[string]any{"apiVersion": gv, "kind": resource.Kind + "List", "items": items})
				return
			}
		}
		t.Errorf("unexpected API request: %s", req.URL.Path)
		http.NotFound(w, req)
	}))
	defer server.Close()
	writeTestKubeconfig(t, r.WorkloadKubeconfig, server.URL)
	if err := os.WriteFile(opts.BootstrapKubeconfig(), []byte("unrelated invalid genesis"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newClusterStatusCommand(opts)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--cluster", "lab"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "self-managed") || !strings.Contains(out.String(), "1/1 ready") {
		t.Fatal(out.String())
	}
}

func TestClustersKeepRecordsWhenGenesisCannotBeRead(t *testing.T) {
	opts := &Options{StateDir: t.TempDir()}
	rememberTestCluster(t, opts, "lab", "lab", registry.Managed)
	if err := os.WriteFile(opts.BootstrapKubeconfig(), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, warnings bytes.Buffer
	cmd := newClustersCommand(opts)
	cmd.SetOut(&out)
	cmd.SetErr(&warnings)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "managed") || !strings.Contains(warnings.String(), "Could not refresh") {
		t.Fatalf("%s\n%s", out.String(), warnings.String())
	}
	saved, err := opts.registry().Get("lab", "lab")
	if err != nil || saved.Mode != registry.Managed {
		t.Fatalf("mode changed on connection error: %+v %v", saved, err)
	}
}
