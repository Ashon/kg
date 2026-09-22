package render

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	bootstrapv1 "sigs.k8s.io/cluster-api/api/bootstrap/kubeadm/v1beta2"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/Ashon/kgenesis/internal/config"
)

// DefaultKubeVIPVersion is used when cluster.virtualIP.version is unset. It is
// pinned rather than floating so a rebuild of an existing cluster does not pull
// in a different kube-vip than the one already running.
const DefaultKubeVIPVersion = "v0.8.7"

const kubeVIPManifestPath = "/etc/kubernetes/manifests/kube-vip.yaml"

func renderControlPlane(cfg *config.Config, machineTemplateName string) *controlplanev1.KubeadmControlPlane {
	replicas := cfg.Cluster.ControlPlaneReplicas

	kubeadmSpec := bootstrapv1.KubeadmConfigSpec{
		ClusterConfiguration: bootstrapv1.ClusterConfiguration{
			ImageRepository: cfg.Cluster.ImageRepository,
			APIServer: bootstrapv1.APIServer{
				CertSANs: []string{cfg.Cluster.ControlPlaneEndpoint.Host},
			},
		},
		InitConfiguration: bootstrapv1.InitConfiguration{
			NodeRegistration: bootstrapv1.NodeRegistrationOptions{},
		},
		JoinConfiguration: bootstrapv1.JoinConfiguration{
			NodeRegistration: bootstrapv1.NodeRegistrationOptions{},
		},
		PreKubeadmCommands: containerdPreflightCommands(),
	}

	if cfg.Cluster.VirtualIP.Enabled {
		applyKubeVIP(&kubeadmSpec, cfg)
	}

	return &controlplanev1.KubeadmControlPlane{
		TypeMeta: metav1.TypeMeta{
			APIVersion: controlplanev1.GroupVersion.String(),
			Kind:       "KubeadmControlPlane",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ControlPlaneName(cfg.Cluster.Name),
			Namespace: cfg.Cluster.Namespace,
			Labels:    poolLabels(cfg),
		},
		Spec: controlplanev1.KubeadmControlPlaneSpec{
			Replicas: &replicas,
			Version:  cfg.Cluster.KubernetesVersion,
			MachineTemplate: controlplanev1.KubeadmControlPlaneMachineTemplate{
				Spec: controlplanev1.KubeadmControlPlaneMachineTemplateSpec{
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infraAPIGroup,
						Kind:     "HostMachineTemplate",
						Name:     machineTemplateName,
					},
				},
			},
			KubeadmConfigSpec: kubeadmSpec,
		},
	}
}

// applyKubeVIP adds the static pod that raises the control plane VIP.
//
// The static pod has to exist before `kubeadm init` so the endpoint every node
// joins through is reachable from the first moment. That creates an ordering
// problem: kube-vip authenticates with /etc/kubernetes/admin.conf, which kubeadm
// only writes at the end of init, and since Kubernetes 1.29 the file kubeadm
// writes first is super-admin.conf. The commands below point kube-vip at
// super-admin.conf for the duration of init and switch it back afterwards, which
// is the workaround kube-vip documents for Cluster API.
func applyKubeVIP(spec *bootstrapv1.KubeadmConfigSpec, cfg *config.Config) {
	version := cfg.Cluster.VirtualIP.Version
	if version == "" {
		version = DefaultKubeVIPVersion
	}

	spec.Files = append(spec.Files, bootstrapv1.File{
		Path:        kubeVIPManifestPath,
		Owner:       "root:root",
		Permissions: "0600",
		Content: kubeVIPManifest(
			cfg.Cluster.ControlPlaneEndpoint.Host,
			cfg.Cluster.ControlPlaneEndpoint.Port,
			cfg.Cluster.VirtualIP.Interface,
			version,
		),
	})

	spec.PreKubeadmCommands = append(spec.PreKubeadmCommands,
		fmt.Sprintf("sed -i 's#path: /etc/kubernetes/admin.conf#path: /etc/kubernetes/super-admin.conf#' %s",
			kubeVIPManifestPath),
	)
	spec.PostKubeadmCommands = append(spec.PostKubeadmCommands,
		fmt.Sprintf("sed -i 's#path: /etc/kubernetes/super-admin.conf#path: /etc/kubernetes/admin.conf#' %s",
			kubeVIPManifestPath),
	)
}

// kubeVIPManifest renders the kube-vip static pod in ARP mode, which is what
// works on a flat L2 network without BGP peers to configure.
func kubeVIPManifest(vip string, port int32, iface, version string) string {
	interfaceEnv := ""
	if iface != "" {
		interfaceEnv = fmt.Sprintf("    - name: vip_interface\n      value: %q\n", iface)
	}

	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: kube-vip
  namespace: kube-system
spec:
  containers:
  - name: kube-vip
    image: ghcr.io/kube-vip/kube-vip:%s
    imagePullPolicy: IfNotPresent
    args:
    - manager
    env:
    - name: vip_arp
      value: "true"
    - name: port
      value: "%d"
    - name: vip_cidr
      value: "32"
    - name: cp_enable
      value: "true"
    - name: cp_namespace
      value: kube-system
    - name: vip_ddns
      value: "false"
    - name: svc_enable
      value: "false"
    - name: vip_leaderelection
      value: "true"
    - name: vip_leaseduration
      value: "15"
    - name: vip_renewdeadline
      value: "10"
    - name: vip_retryperiod
      value: "2"
    - name: address
      value: %q
%s    securityContext:
      capabilities:
        add:
        - NET_ADMIN
        - NET_RAW
    volumeMounts:
    - mountPath: /etc/kubernetes/admin.conf
      name: kubeconfig
  hostAliases:
  - hostnames:
    - kubernetes
    ip: 127.0.0.1
  hostNetwork: true
  volumes:
  - hostPath:
      path: /etc/kubernetes/admin.conf
      type: FileOrCreate
    name: kubeconfig
`, version, port, vip, interfaceEnv)
}
