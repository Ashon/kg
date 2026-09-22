package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("shared key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cfg := &config.Config{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		Cluster: config.ClusterConfig{
			Name:                 "lab",
			KubernetesVersion:    "v1.34.1",
			ControlPlaneEndpoint: config.Endpoint{Host: "10.10.0.100"},
			VirtualIP:            config.VirtualIPConfig{Enabled: true},
		},
		SSH: config.SSHConfig{PrivateKeyPath: keyPath},
		Hosts: []config.HostConfig{
			{Name: "cp-1", Address: "10.10.0.11", Role: config.RoleControlPlane},
			{Name: "cp-2", Address: "10.10.0.12", Role: config.RoleControlPlane},
			{Name: "cp-3", Address: "10.10.0.13", Role: config.RoleControlPlane},
			{Name: "w-1", Address: "10.10.0.21", Role: config.RoleWorker},
			{Name: "w-2", Address: "10.10.0.22", Role: config.RoleWorker},
		},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config is invalid: %v", err)
	}
	return cfg
}

func find[T client.Object](t *testing.T, objects []client.Object, name string) T {
	t.Helper()

	for _, obj := range objects {
		typed, ok := obj.(T)
		if ok && obj.GetName() == name {
			return typed
		}
	}

	var zero T
	t.Fatalf("no %T named %q in the rendered objects", zero, name)
	return zero
}

// Hosts sharing one credential must produce one Secret, not one each.
func TestRenderDeduplicatesSSHSecrets(t *testing.T) {
	objects, err := Render(testConfig(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if len(objects.Secrets) != 1 {
		t.Fatalf("expected 1 secret for 5 hosts sharing a key, got %d", len(objects.Secrets))
	}
	if got := string(objects.Secrets[0].Data["private_key"]); got != "shared key" {
		t.Errorf("secret contents: got %q", got)
	}

	for _, h := range objects.Hosts {
		if h.Spec.SSHSecretRef.Name != objects.Secrets[0].Name {
			t.Errorf("host %s references %q, want %q",
				h.Name, h.Spec.SSHSecretRef.Name, objects.Secrets[0].Name)
		}
	}
}

func TestRenderSeparatesSecretsPerCredential(t *testing.T) {
	cfg := testConfig(t)

	other := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(other, []byte("a different key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	cfg.Hosts[4].PrivateKeyPath = other

	objects, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(objects.Secrets) != 2 {
		t.Fatalf("expected 2 secrets for 2 distinct credentials, got %d", len(objects.Secrets))
	}
}

func TestRenderLabelsHostsByRole(t *testing.T) {
	objects, err := Render(testConfig(t))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	roles := map[string]string{}
	for _, h := range objects.Hosts {
		roles[h.Name] = h.Labels[infrav1.RoleLabel]
	}

	for name, want := range map[string]string{
		"cp-1": infrav1.RoleControlPlane,
		"w-1":  infrav1.RoleWorker,
	} {
		if roles[name] != want {
			t.Errorf("host %s role label: got %q, want %q", name, roles[name], want)
		}
	}
}

func TestRenderWiresTheClusterReferences(t *testing.T) {
	cfg := testConfig(t)
	objects, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	cluster := objects.Cluster
	if cluster.Spec.InfrastructureRef.Kind != "HostCluster" ||
		cluster.Spec.InfrastructureRef.Name != cfg.Cluster.Name {
		t.Errorf("infrastructureRef: got %+v", cluster.Spec.InfrastructureRef)
	}
	if cluster.Spec.ControlPlaneRef.Kind != "KubeadmControlPlane" ||
		cluster.Spec.ControlPlaneRef.Name != ControlPlaneName(cfg.Cluster.Name) {
		t.Errorf("controlPlaneRef: got %+v", cluster.Spec.ControlPlaneRef)
	}

	hostCluster := find[*infrav1.HostCluster](t, objects.Infra, cfg.Cluster.Name)
	if hostCluster.Spec.ControlPlaneEndpoint.Host != "10.10.0.100" ||
		hostCluster.Spec.ControlPlaneEndpoint.Port != 6443 {
		t.Errorf("control plane endpoint: got %+v", hostCluster.Spec.ControlPlaneEndpoint)
	}
}

func TestRenderControlPlaneSelectsControlPlaneHosts(t *testing.T) {
	cfg := testConfig(t)
	objects, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	template := find[*infrav1.HostMachineTemplate](t, objects.Infra, ControlPlaneTemplateName(cfg.Cluster.Name))
	got := template.Spec.Template.Spec.HostSelector.MatchLabels[infrav1.RoleLabel]
	if got != infrav1.RoleControlPlane {
		t.Errorf("control plane host selector: got %q, want %q", got, infrav1.RoleControlPlane)
	}
}

func TestRenderIncludesKubeVIPWhenEnabled(t *testing.T) {
	cfg := testConfig(t)
	objects, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	kcp := findControlPlane(t, objects)

	var manifest string
	for _, f := range kcp.Spec.KubeadmConfigSpec.Files {
		if f.Path == kubeVIPManifestPath {
			manifest = f.Content
		}
	}
	if manifest == "" {
		t.Fatal("kube-vip static pod was not rendered")
	}
	if !strings.Contains(manifest, `value: "10.10.0.100"`) {
		t.Errorf("kube-vip manifest does not carry the VIP:\n%s", manifest)
	}

	// kubeadm only writes admin.conf at the end of init, so kube-vip has to run
	// against super-admin.conf until then and be switched back afterwards.
	joined := strings.Join(kcp.Spec.KubeadmConfigSpec.PreKubeadmCommands, "\n")
	if !strings.Contains(joined, "super-admin.conf") {
		t.Errorf("preKubeadmCommands do not switch kube-vip to super-admin.conf:\n%s", joined)
	}
	joined = strings.Join(kcp.Spec.KubeadmConfigSpec.PostKubeadmCommands, "\n")
	if !strings.Contains(joined, "admin.conf") {
		t.Errorf("postKubeadmCommands do not switch kube-vip back:\n%s", joined)
	}

	if !containsString(kcp.Spec.KubeadmConfigSpec.ClusterConfiguration.APIServer.CertSANs, "10.10.0.100") {
		t.Error("the VIP is missing from the API server cert SANs")
	}
}

func TestRenderOmitsKubeVIPWhenDisabled(t *testing.T) {
	cfg := testConfig(t)
	cfg.Cluster.VirtualIP.Enabled = false

	objects, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, f := range findControlPlane(t, objects).Spec.KubeadmConfigSpec.Files {
		if f.Path == kubeVIPManifestPath {
			t.Error("kube-vip was rendered even though virtualIP is disabled")
		}
	}
}

func TestRenderWorkerPoolTaintsAndLabels(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workers = []config.WorkerPool{{
		Name:         "gpu",
		Replicas:     2,
		HostSelector: map[string]string{infrav1.RoleLabel: infrav1.RoleWorker},
		NodeLabels:   map[string]string{"accelerator": "gpu"},
		NodeTaints:   []config.Taint{{Key: "accelerator", Value: "gpu", Effect: "NoSchedule"}},
	}}

	objects, err := Render(cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	deployment := find[*clusterv1.MachineDeployment](t, objects.Infra, WorkerDeploymentName("lab", "gpu"))
	if *deployment.Spec.Replicas != 2 {
		t.Errorf("replicas: got %d, want 2", *deployment.Spec.Replicas)
	}

	bootstrapTemplate := findBootstrapTemplate(t, objects, WorkerBootstrapName("lab", "gpu"))
	registration := bootstrapTemplate.Spec.Template.Spec.JoinConfiguration.NodeRegistration

	if registration.Taints == nil || len(*registration.Taints) != 1 {
		t.Fatalf("taints: got %v", registration.Taints)
	}
	if (*registration.Taints)[0].Key != "accelerator" {
		t.Errorf("taint key: got %q", (*registration.Taints)[0].Key)
	}

	var found bool
	for _, arg := range registration.KubeletExtraArgs {
		if arg.Name == "node-labels" && arg.Value != nil && *arg.Value == "accelerator=gpu" {
			found = true
		}
	}
	if !found {
		t.Errorf("node-labels arg is missing: %+v", registration.KubeletExtraArgs)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func findControlPlane(t *testing.T, objects *Objects) *controlplanev1.KubeadmControlPlane {
	t.Helper()

	for _, obj := range objects.Infra {
		if kcp, ok := obj.(*controlplanev1.KubeadmControlPlane); ok {
			return kcp
		}
	}
	t.Fatal("no KubeadmControlPlane in the rendered objects")
	return nil
}

func findBootstrapTemplate(t *testing.T, objects *Objects, name string) *bootstrapv1.KubeadmConfigTemplate {
	t.Helper()

	for _, obj := range objects.Infra {
		if tpl, ok := obj.(*bootstrapv1.KubeadmConfigTemplate); ok && tpl.Name == name {
			return tpl
		}
	}
	t.Fatalf("no KubeadmConfigTemplate named %q in the rendered objects", name)
	return nil
}
