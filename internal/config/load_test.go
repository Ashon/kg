// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write drops a config file plus a dummy key next to it and returns its path.
func write(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("not a real key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	path := filepath.Join(dir, "kg.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const minimal = `apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig
cluster:
  name: lab
  kubernetesVersion: v1.34.1
  controlPlaneEndpoint:
    host: 10.10.0.100
ssh:
  privateKeyPath: ./id_ed25519
hosts:
  - {name: cp-1, address: 10.10.0.11, role: control-plane}
  - {name: cp-2, address: 10.10.0.12, role: control-plane}
  - {name: cp-3, address: 10.10.0.13, role: control-plane}
  - {name: w-1, address: 10.10.0.21, role: worker}
`

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := map[string]struct{ got, want any }{
		"namespace":             {cfg.Cluster.Namespace, "lab"},
		"api server port":       {cfg.Cluster.ControlPlaneEndpoint.Port, DefaultAPIServerPort},
		"pod CIDR":              {cfg.Cluster.Network.PodCIDR, DefaultPodCIDR},
		"service domain":        {cfg.Cluster.Network.ServiceDomain, DefaultServiceDomain},
		"ssh user":              {cfg.Hosts[0].User, DefaultSSHUser},
		"ssh port":              {cfg.Hosts[0].Port, DefaultSSHPort},
		"host key policy":       {cfg.Hosts[0].HostKeyPolicy, DefaultHostKeyPolicy},
		"bootstrap name":        {cfg.Bootstrap.ClusterName, DefaultBootstrapName},
		"control plane replica": {cfg.Cluster.ControlPlaneReplicas, int32(3)},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", name, c.got, c.want)
		}
	}

	// A worker host with no explicit pool lands in one default pool.
	if len(cfg.Workers) != 1 {
		t.Fatalf("expected one default worker pool, got %d", len(cfg.Workers))
	}
	if cfg.Workers[0].Name != DefaultWorkerPoolName || cfg.Workers[0].Replicas != 1 {
		t.Errorf("default pool: got %+v", cfg.Workers[0])
	}
	if got := cfg.Workers[0].HostSelector[RoleLabel]; got != RoleWorker {
		t.Errorf("default pool selector: got %q, want %q", got, RoleWorker)
	}
}

func TestLoadResolvesPathsRelativeToTheConfigFile(t *testing.T) {
	path := write(t, minimal)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := filepath.Join(filepath.Dir(path), "id_ed25519")
	if cfg.Hosts[0].PrivateKeyPath != want {
		t.Errorf("private key path: got %q, want %q", cfg.Hosts[0].PrivateKeyPath, want)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	const broken = `apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig
cluster:
  name: Lab
  kubernetesVersion: "1.34.1"
  controlPlaneEndpoint: {}
  network:
    podCIDR: not-a-cidr
  cni:
    manifests: [./no-such-manifest.yaml]
ssh:
  privateKeyPath: ./id_ed25519
hosts:
  - {name: cp-1, address: 10.10.0.11, role: control-plane}
  - {name: cp-1, address: 10.10.0.12, role: gateway}
`
	_, err := Load(write(t, broken))
	if err == nil {
		t.Fatal("expected validation to fail")
	}

	for _, want := range []string{
		"cluster.name",
		"kubernetesVersion",
		"controlPlaneEndpoint.host",
		"podCIDR",
		"cni.manifests[0]",
		"is used more than once",
		"role",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestValidateRejectsAnEvenControlPlaneCount(t *testing.T) {
	body := strings.Replace(minimal,
		"  - {name: cp-3, address: 10.10.0.13, role: control-plane}\n", "", 1)

	_, err := Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "etcd quorum") {
		t.Fatalf("expected an etcd quorum error, got %v", err)
	}
}

// The pool has to be large enough: every machine claims exactly one host.
func TestValidateRejectsAnUndersizedInventory(t *testing.T) {
	body := minimal + `workers:
  - name: big
    replicas: 10
`
	_, err := Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "inventory has only") {
		t.Fatalf("expected an inventory size error, got %v", err)
	}
}

func TestValidateRequiresACredential(t *testing.T) {
	body := strings.Replace(minimal, "ssh:\n  privateKeyPath: ./id_ed25519\n", "", 1)

	_, err := Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("expected a missing credential error, got %v", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	body := minimal + "unexpectedKey: true\n"

	_, err := Load(write(t, body))
	if err == nil {
		t.Fatal("expected strict parsing to reject an unknown field")
	}
}

func TestLoadResolvesLocalCNIManifestsButNotURLs(t *testing.T) {
	body := strings.Replace(minimal,
		"  controlPlaneEndpoint:\n    host: 10.10.0.100\n",
		"  controlPlaneEndpoint:\n    host: 10.10.0.100\n"+
			"  cni:\n    manifests:\n      - ./cilium.yaml\n      - https://example.invalid/calico.yaml\n",
		1)

	path := write(t, body)
	manifest := filepath.Join(filepath.Dir(path), "cilium.yaml")
	if err := os.WriteFile(manifest, []byte("kind: ConfigMap\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Cluster.CNI.Manifests[0] != manifest {
		t.Errorf("local manifest: got %q, want %q", cfg.Cluster.CNI.Manifests[0], manifest)
	}
	// A URL must survive path resolution untouched.
	if cfg.Cluster.CNI.Manifests[1] != "https://example.invalid/calico.yaml" {
		t.Errorf("URL was rewritten: got %q", cfg.Cluster.CNI.Manifests[1])
	}
}

// Most fleets share one key. Repeating the same line for every host buries
// whatever else is wrong, and points at the hosts rather than at the field that
// has to be edited.
func TestValidateReportsASharedKeyOnce(t *testing.T) {
	body := strings.Replace(minimal,
		"ssh:\n  privateKeyPath: ./id_ed25519\n",
		"ssh:\n  privateKeyPath: ./missing_key\n", 1)

	_, err := Load(write(t, body))
	if err == nil {
		t.Fatal("expected a missing key to fail validation")
	}

	message := err.Error()
	if got := strings.Count(message, "is not readable"); got != 1 {
		t.Errorf("the key error appears %d times, want 1:\n%s", got, message)
	}
	if !strings.Contains(message, "ssh.privateKeyPath") {
		t.Errorf("the error does not name the field to edit:\n%s", message)
	}
	if strings.Contains(message, "hosts[0].privateKeyPath") {
		t.Errorf("the error blames a host for a shared setting:\n%s", message)
	}
}

// A key set on one host is that host's problem, and has to say so.
func TestValidateReportsAPerHostKeyAgainstThatHost(t *testing.T) {
	body := strings.Replace(minimal,
		"  - {name: w-1, address: 10.10.0.21, role: worker}\n",
		"  - {name: w-1, address: 10.10.0.21, role: worker, privateKeyPath: ./only-here}\n", 1)

	_, err := Load(write(t, body))
	if err == nil {
		t.Fatal("expected a missing per-host key to fail validation")
	}
	if !strings.Contains(err.Error(), "hosts[3].privateKeyPath") {
		t.Errorf("the error does not name the host that set it:\n%v", err)
	}
}

// A genesis node manages several clusters at once. clusterctl moves a namespace
// rather than a cluster, so one namespace per cluster is what makes ejecting a
// single cluster possible, and it is also the only thing keeping one cluster's
// machines out of another's host pool.
func TestNamespaceDefaultsToTheClusterName(t *testing.T) {
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cluster.Namespace != cfg.Cluster.Name {
		t.Errorf("namespace is %q, want the cluster name %q",
			cfg.Cluster.Namespace, cfg.Cluster.Name)
	}
}

func TestNamespaceCanBeSetExplicitly(t *testing.T) {
	body := strings.Replace(minimal, "  name: lab\n", "  name: lab\n  namespace: fleet-a\n", 1)

	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Cluster.Namespace != "fleet-a" {
		t.Errorf("namespace is %q, want fleet-a", cfg.Cluster.Namespace)
	}
}
