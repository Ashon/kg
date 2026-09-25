// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package render turns a kgenesis.yaml into the Cluster API objects that
// describe the target cluster, plus the Host pool and SSH secrets the kgenesis
// infrastructure provider draws from.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/config"
)

// APIGroups referenced by the rendered objects.
const (
	infraAPIGroup        = "infrastructure.kgenesis.io"
	bootstrapAPIGroup    = "bootstrap.cluster.x-k8s.io"
	controlPlaneAPIGroup = "controlplane.cluster.x-k8s.io"
)

// Objects is the rendered cluster, split so callers can apply the pool before
// the cluster: a Machine that finds no Available host just waits, but applying
// in order means the first reconcile can already claim.
type Objects struct {
	// Namespace holds everything belonging to this cluster. One per cluster is
	// what keeps a second cluster's machines out of this one's host pool, and
	// what lets this one be ejected on its own.
	Namespace *corev1.Namespace

	// Secrets hold SSH credentials, deduplicated across hosts.
	Secrets []*corev1.Secret
	// Hosts is the pool.
	Hosts []*infrav1.Host
	// Cluster and below describe the workload cluster.
	Cluster *clusterv1.Cluster
	Infra   []client.Object
}

// All returns every object in apply order.
func (o *Objects) All() []client.Object {
	out := make([]client.Object, 0, len(o.Secrets)+len(o.Hosts)+len(o.Infra)+2)
	out = append(out, o.Namespace)
	for _, s := range o.Secrets {
		out = append(out, s)
	}
	for _, h := range o.Hosts {
		out = append(out, h)
	}
	out = append(out, o.Cluster)
	out = append(out, o.Infra...)
	return out
}

// Render builds every object described by cfg. SSH private keys are read from
// disk here, on the genesis node, so the caller does not need filesystem access
// to the operator's keys.
func Render(cfg *config.Config) (*Objects, error) {
	secrets, secretForHost, err := renderSecrets(cfg)
	if err != nil {
		return nil, err
	}

	objs := &Objects{
		Namespace: &corev1.Namespace{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{
				Name:   cfg.Cluster.Namespace,
				Labels: poolLabels(cfg),
			},
		},
		Secrets: secrets,
	}

	for _, h := range cfg.Hosts {
		objs.Hosts = append(objs.Hosts, renderHost(cfg, h, secretForHost[h.Name]))
	}

	objs.Cluster = renderCluster(cfg)
	objs.Infra = append(objs.Infra, renderHostCluster(cfg))

	cpTemplate := renderControlPlaneMachineTemplate(cfg)
	objs.Infra = append(objs.Infra, cpTemplate, renderControlPlane(cfg, cpTemplate.Name))

	for _, pool := range cfg.Workers {
		template, bootstrapTemplate, deployment := renderWorkerPool(cfg, pool)
		objs.Infra = append(objs.Infra, template, bootstrapTemplate, deployment)
	}

	return objs, nil
}

// clusterctlMoveLabel marks an object that clusterctl must move even though no
// Cluster owns it.
const clusterctlMoveLabel = "clusterctl.cluster.x-k8s.io/move"

// renderSecrets deduplicates credentials: a fleet that shares one key gets one
// Secret rather than one per host.
func renderSecrets(cfg *config.Config) ([]*corev1.Secret, map[string]string, error) {
	type credential struct {
		privateKey []byte
		passphrase string
		password   string
	}

	byFingerprint := map[string]credential{}
	secretForHost := map[string]string{}

	for _, h := range cfg.Hosts {
		cred := credential{passphrase: h.Passphrase, password: h.Password}
		if h.PrivateKeyPath != "" {
			key, err := os.ReadFile(h.PrivateKeyPath)
			if err != nil {
				return nil, nil, fmt.Errorf("host %s: read private key: %w", h.Name, err)
			}
			cred.privateKey = key
		}

		sum := sha256.New()
		sum.Write(cred.privateKey)
		sum.Write([]byte{0})
		sum.Write([]byte(cred.passphrase))
		sum.Write([]byte{0})
		sum.Write([]byte(cred.password))
		fingerprint := hex.EncodeToString(sum.Sum(nil))[:12]

		byFingerprint[fingerprint] = cred
		secretForHost[h.Name] = SSHSecretName(fingerprint)
	}

	fingerprints := make([]string, 0, len(byFingerprint))
	for f := range byFingerprint {
		fingerprints = append(fingerprints, f)
	}
	sort.Strings(fingerprints)

	secrets := make([]*corev1.Secret, 0, len(fingerprints))
	for _, f := range fingerprints {
		cred := byFingerprint[f]
		data := map[string][]byte{}
		if len(cred.privateKey) > 0 {
			data["private_key"] = cred.privateKey
		}
		if cred.passphrase != "" {
			data["passphrase"] = []byte(cred.passphrase)
		}
		if cred.password != "" {
			data["password"] = []byte(cred.password)
		}

		labels := poolLabels(cfg)
		// One Secret can serve several Hosts, so it belongs to none of them and
		// nothing owns it. clusterctl would leave it on the genesis node, and the
		// ejected cluster could no longer reach a single host.
		labels[clusterctlMoveLabel] = ""

		secrets = append(secrets, &corev1.Secret{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{
				Name:      SSHSecretName(f),
				Namespace: cfg.Cluster.Namespace,
				Labels:    labels,
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		})
	}

	return secrets, secretForHost, nil
}

// SSHSecretName is the Secret holding one distinct SSH credential.
func SSHSecretName(fingerprint string) string { return "kgenesis-ssh-" + fingerprint }

func renderHost(cfg *config.Config, h config.HostConfig, secretName string) *infrav1.Host {
	labels := map[string]string{infrav1.RoleLabel: h.Role}
	for k, v := range h.Labels {
		labels[k] = v
	}

	return &infrav1.Host{
		TypeMeta: metav1.TypeMeta{APIVersion: infrav1.GroupVersion.String(), Kind: "Host"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      h.Name,
			Namespace: cfg.Cluster.Namespace,
			Labels:    labels,
		},
		Spec: infrav1.HostSpec{
			Address:       h.Address,
			Port:          h.Port,
			User:          h.User,
			SSHSecretRef:  corev1.LocalObjectReference{Name: secretName},
			HostKeyPolicy: infrav1.HostKeyPolicy(h.HostKeyPolicy),
			PublicKey:     h.PublicKey,
		},
	}
}

func poolLabels(cfg *config.Config) map[string]string {
	return map[string]string{clusterv1.ClusterNameLabel: cfg.Cluster.Name}
}

func renderCluster(cfg *config.Config) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{APIVersion: clusterv1.GroupVersion.String(), Kind: "Cluster"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Cluster.Name,
			Namespace: cfg.Cluster.Namespace,
		},
		Spec: clusterv1.ClusterSpec{
			ClusterNetwork: clusterv1.ClusterNetwork{
				Pods:          clusterv1.NetworkRanges{CIDRBlocks: []string{cfg.Cluster.Network.PodCIDR}},
				Services:      clusterv1.NetworkRanges{CIDRBlocks: []string{cfg.Cluster.Network.ServiceCIDR}},
				ServiceDomain: cfg.Cluster.Network.ServiceDomain,
			},
			ControlPlaneRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: controlPlaneAPIGroup,
				Kind:     "KubeadmControlPlane",
				Name:     ControlPlaneName(cfg.Cluster.Name),
			},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infraAPIGroup,
				Kind:     "HostCluster",
				Name:     cfg.Cluster.Name,
			},
		},
	}
}

func renderHostCluster(cfg *config.Config) *infrav1.HostCluster {
	return &infrav1.HostCluster{
		TypeMeta: metav1.TypeMeta{APIVersion: infrav1.GroupVersion.String(), Kind: "HostCluster"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.Cluster.Name,
			Namespace: cfg.Cluster.Namespace,
		},
		Spec: infrav1.HostClusterSpec{
			ControlPlaneEndpoint: clusterv1.APIEndpoint{
				Host: cfg.Cluster.ControlPlaneEndpoint.Host,
				Port: cfg.Cluster.ControlPlaneEndpoint.Port,
			},
		},
	}
}

// Names of the rendered objects, exported so the CLI can report on them.
func ControlPlaneName(cluster string) string          { return cluster + "-control-plane" }
func ControlPlaneTemplateName(cluster string) string  { return cluster + "-control-plane" }
func WorkerTemplateName(cluster, pool string) string  { return cluster + "-" + pool }
func WorkerBootstrapName(cluster, pool string) string { return cluster + "-" + pool }
func WorkerDeploymentName(cluster, pool string) string {
	return cluster + "-" + pool
}

func renderControlPlaneMachineTemplate(cfg *config.Config) *infrav1.HostMachineTemplate {
	return &infrav1.HostMachineTemplate{
		TypeMeta: metav1.TypeMeta{APIVersion: infrav1.GroupVersion.String(), Kind: "HostMachineTemplate"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControlPlaneTemplateName(cfg.Cluster.Name),
			Namespace: cfg.Cluster.Namespace,
		},
		Spec: infrav1.HostMachineTemplateSpec{
			Template: infrav1.HostMachineTemplateResource{
				Spec: infrav1.HostMachineSpec{
					HostSelector: &infrav1.HostSelector{
						MatchLabels: map[string]string{infrav1.RoleLabel: infrav1.RoleControlPlane},
					},
				},
			},
		},
	}
}

func renderWorkerPool(cfg *config.Config, pool config.WorkerPool) (
	*infrav1.HostMachineTemplate, *bootstrapv1.KubeadmConfigTemplate, *clusterv1.MachineDeployment,
) {
	machineTemplate := &infrav1.HostMachineTemplate{
		TypeMeta: metav1.TypeMeta{APIVersion: infrav1.GroupVersion.String(), Kind: "HostMachineTemplate"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkerTemplateName(cfg.Cluster.Name, pool.Name),
			Namespace: cfg.Cluster.Namespace,
		},
		Spec: infrav1.HostMachineTemplateSpec{
			Template: infrav1.HostMachineTemplateResource{
				Spec: infrav1.HostMachineSpec{
					HostSelector: &infrav1.HostSelector{MatchLabels: pool.HostSelector},
				},
			},
		},
	}

	bootstrapTemplate := &bootstrapv1.KubeadmConfigTemplate{
		TypeMeta: metav1.TypeMeta{APIVersion: bootstrapv1.GroupVersion.String(), Kind: "KubeadmConfigTemplate"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkerBootstrapName(cfg.Cluster.Name, pool.Name),
			Namespace: cfg.Cluster.Namespace,
		},
		Spec: bootstrapv1.KubeadmConfigTemplateSpec{
			Template: bootstrapv1.KubeadmConfigTemplateResource{
				Spec: bootstrapv1.KubeadmConfigSpec{
					JoinConfiguration: bootstrapv1.JoinConfiguration{
						NodeRegistration: bootstrapv1.NodeRegistrationOptions{
							KubeletExtraArgs:      nodeLabelArgs(pool.NodeLabels),
							Taints:                nodeTaints(pool.NodeTaints),
							IgnorePreflightErrors: cfg.Cluster.IgnorePreflightErrors,
						},
					},
					PreKubeadmCommands:  withUserCommands(nodePreflightCommands(), cfg.Cluster.PreKubeadmCommands),
					PostKubeadmCommands: withUserCommands(nil, cfg.Cluster.PostKubeadmCommands),
				},
			},
		},
	}

	replicas := pool.Replicas
	deployment := &clusterv1.MachineDeployment{
		TypeMeta: metav1.TypeMeta{APIVersion: clusterv1.GroupVersion.String(), Kind: "MachineDeployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkerDeploymentName(cfg.Cluster.Name, pool.Name),
			Namespace: cfg.Cluster.Namespace,
			Labels:    poolLabels(cfg),
		},
		Spec: clusterv1.MachineDeploymentSpec{
			ClusterName: cfg.Cluster.Name,
			Replicas:    &replicas,
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{clusterv1.ClusterNameLabel: cfg.Cluster.Name},
			},
			// Oldest first, which is the order KubeadmControlPlane already
			// replaces control plane machines in. Cluster API would otherwise
			// pick a worker at random, and an upgrade nobody can predict the
			// next step of is one nobody can stage or stop halfway.
			Deletion: clusterv1.MachineDeploymentDeletionSpec{
				Order: clusterv1.OldestMachineSetDeletionOrder,
			},
			Template: clusterv1.MachineTemplateSpec{
				ObjectMeta: clusterv1.ObjectMeta{Labels: poolLabels(cfg)},
				Spec: clusterv1.MachineSpec{
					ClusterName: cfg.Cluster.Name,
					Version:     cfg.Cluster.KubernetesVersion,
					Bootstrap: clusterv1.Bootstrap{
						ConfigRef: clusterv1.ContractVersionedObjectReference{
							APIGroup: bootstrapAPIGroup,
							Kind:     "KubeadmConfigTemplate",
							Name:     bootstrapTemplate.Name,
						},
					},
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infraAPIGroup,
						Kind:     "HostMachineTemplate",
						Name:     machineTemplate.Name,
					},
				},
			},
		},
	}

	return machineTemplate, bootstrapTemplate, deployment
}

func nodeLabelArgs(labels map[string]string) []bootstrapv1.Arg {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := ""
	for i, k := range keys {
		if i > 0 {
			pairs += ","
		}
		pairs += k + "=" + labels[k]
	}
	return []bootstrapv1.Arg{{Name: "node-labels", Value: &pairs}}
}

func nodeTaints(taints []config.Taint) *[]corev1.Taint {
	if len(taints) == 0 {
		return nil
	}
	out := make([]corev1.Taint, 0, len(taints))
	for _, t := range taints {
		out = append(out, corev1.Taint{
			Key:    t.Key,
			Value:  t.Value,
			Effect: corev1.TaintEffect(t.Effect),
		})
	}
	return &out
}

// nodePreflightCommands cover what a stock host image usually lacks. kubeadm's
// own preflight checks fail on any of these, and the failure text is far enough
// from the cause that setting them up here saves real debugging time.
//
// They run under `set -e`, so anything that is merely unnecessary on some hosts
// must not be written as a hard failure.
// withUserCommands appends the operator's commands to kgenesis's own, returning
// a fresh slice. The copy matters: kube-vip appends to the result afterwards,
// and it must not reach back into the parsed configuration.
func withUserCommands(ours, theirs []string) []string {
	out := make([]string, 0, len(ours)+len(theirs))
	out = append(out, ours...)
	return append(out, theirs...)
}

func nodePreflightCommands() []string {
	return []string{
		// kubelet refuses to start while swap is on, and `swapoff -a` only covers
		// what /etc/fstab lists. A swapfile enabled by hand, zram or systemd-swap
		// survives it, and nothing says so: the failure turns up minutes later as
		// a kubelet that never becomes healthy, which reads like a cgroup problem.
		"swapoff -a || true",
		"awk 'NR > 1 { print $1 }' /proc/swaps | while read -r area; do swapoff \"$area\" || true; done",
		"if [ -f /etc/fstab ]; then sed -ri 's/^([^#].*\\sswap\\s)/#\\1/' /etc/fstab; fi",
		"if [ \"$(awk 'NR > 1' /proc/swaps | wc -l)\" -ne 0 ]; then " +
			"echo 'kgenesis: swap is still active, and kubelet refuses to start with it on. " +
			"Disable it on this host and retry.' >&2; cat /proc/swaps >&2; exit 1; fi",

		// Both may be built into the kernel or already loaded, in which case
		// modprobe has no module file to find and fails despite the feature being
		// present. Availability is checked below rather than inferred from this.
		"modprobe overlay 2>/dev/null || true",
		"modprobe br_netfilter 2>/dev/null || true",
		"printf 'overlay\\nbr_netfilter\\n' > /etc/modules-load.d/kgenesis.conf",

		// sysctl --system exits 0 even for keys that do not exist, so without an
		// explicit check a host missing bridge netfilter comes up looking healthy
		// while kube-proxy's rules never see pod traffic.
		"test -e /proc/sys/net/bridge/bridge-nf-call-iptables || " +
			"{ echo 'kgenesis: br_netfilter is unavailable, so bridged traffic would bypass kube-proxy. " +
			"Load the module on this host and retry.' >&2; exit 1; }",

		"printf 'net.bridge.bridge-nf-call-iptables=1\\nnet.bridge.bridge-nf-call-ip6tables=1\\nnet.ipv4.ip_forward=1\\n' > /etc/sysctl.d/99-kgenesis.conf",
		"sysctl --system",
	}
}
