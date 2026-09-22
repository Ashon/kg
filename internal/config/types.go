// Package config defines the kgenesis.yaml file: the single input a genesis node
// needs to stamp a cluster out of a pool of pre-provisioned hosts.
package config

const (
	// APIVersion is the only apiVersion this loader accepts.
	APIVersion = "kgenesis.io/v1alpha1"
	// Kind is the only kind this loader accepts.
	Kind = "GenesisConfig"
)

// Config is the root of kgenesis.yaml.
type Config struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`

	Cluster   ClusterConfig   `json:"cluster"`
	SSH       SSHConfig       `json:"ssh"`
	Hosts     []HostConfig    `json:"hosts"`
	Workers   []WorkerPool    `json:"workers,omitempty"`
	Bootstrap BootstrapConfig `json:"bootstrap,omitempty"`
}

// ClusterConfig describes the cluster to stamp out.
type ClusterConfig struct {
	// name is the Cluster API cluster name and the workload cluster's identity.
	Name string `json:"name"`

	// namespace holds the Cluster API objects. Defaults to "default".
	Namespace string `json:"namespace,omitempty"`

	// kubernetesVersion is the version kubeadm installs, e.g. "v1.34.1". Required:
	// there is no sensible default that stays correct over time.
	KubernetesVersion string `json:"kubernetesVersion"`

	// controlPlaneEndpoint is the API server address every node joins through. On
	// bare metal this is a VIP; enable cluster.virtualIP to have kube-vip serve it
	// from the control plane hosts themselves.
	ControlPlaneEndpoint Endpoint `json:"controlPlaneEndpoint"`

	// controlPlaneReplicas is how many control plane machines to create. When 0 it
	// is derived from the number of hosts with role control-plane.
	ControlPlaneReplicas int32 `json:"controlPlaneReplicas,omitempty"`

	// network configures the cluster CIDRs.
	Network NetworkConfig `json:"network,omitempty"`

	// imageRepository overrides where kubeadm pulls control plane images from, for
	// air-gapped or mirrored environments.
	ImageRepository string `json:"imageRepository,omitempty"`

	// preKubeadmCommands and postKubeadmCommands run on every node, before and
	// after kubeadm. They are the escape hatch for whatever a particular fleet
	// needs that kgenesis does not model: a vendor agent, storage setup, NIC
	// tuning, or an extra kubeadm configuration document appended to
	// /run/kubeadm/kubeadm.yaml.
	//
	// They run under `set -e`, so a command that is only sometimes necessary has
	// to tolerate its own absence.
	PreKubeadmCommands  []string `json:"preKubeadmCommands,omitempty"`
	PostKubeadmCommands []string `json:"postKubeadmCommands,omitempty"`

	// ignorePreflightErrors lists kubeadm preflight checks to downgrade to
	// warnings on every node, by their kubeadm name, for example
	// "SystemVerification" or "NumCPU". Use it when a check does not apply to
	// your hardware; each entry is a check nobody will see fail, so keep the list
	// as short as the environment allows.
	IgnorePreflightErrors []string `json:"ignorePreflightErrors,omitempty"`

	// cni is applied to the workload cluster once its API server answers.
	// Without a CNI the nodes stay NotReady.
	CNI CNIConfig `json:"cni,omitempty"`

	// virtualIP configures the kube-vip static pod that serves the control plane
	// endpoint. Leave disabled when an external load balancer already provides it.
	VirtualIP VirtualIPConfig `json:"virtualIP,omitempty"`
}

// Endpoint is a host/port pair.
type Endpoint struct {
	Host string `json:"host"`
	Port int32  `json:"port,omitempty"`
}

// NetworkConfig holds the cluster CIDRs.
type NetworkConfig struct {
	PodCIDR       string `json:"podCIDR,omitempty"`
	ServiceCIDR   string `json:"serviceCIDR,omitempty"`
	ServiceDomain string `json:"serviceDomain,omitempty"`
}

// CNIConfig points at the manifests that install a CNI.
//
// kgenesis does not bundle a CNI or pin one to a version of its own. Bundling
// would mean shipping a copy that goes stale, and deriving a download URL from a
// provider name would break the moment upstream reorganises its releases. A
// manifest you name is also what works air-gapped, and it is what `helm template`
// already produces for charts like Cilium.
type CNIConfig struct {
	// manifests are file paths or URLs, applied in order. Relative paths resolve
	// against the configuration file.
	Manifests []string `json:"manifests,omitempty"`
}

// HasCNI reports whether any manifest was configured.
func (c CNIConfig) HasCNI() bool { return len(c.Manifests) > 0 }

// VirtualIPConfig configures the kube-vip static pod.
type VirtualIPConfig struct {
	Enabled bool `json:"enabled,omitempty"`

	// interface is the NIC kube-vip advertises the VIP on. Empty lets kube-vip
	// auto-detect the interface holding the default route.
	Interface string `json:"interface,omitempty"`

	// version is the kube-vip image tag.
	Version string `json:"version,omitempty"`
}

// SSHConfig is the default SSH credential applied to every host that does not
// override it.
type SSHConfig struct {
	User string `json:"user,omitempty"`
	Port int32  `json:"port,omitempty"`

	// privateKeyPath is read on the genesis node and stored as a Secret in the
	// bootstrap cluster. Supports a leading "~/".
	PrivateKeyPath string `json:"privateKeyPath,omitempty"`

	// passphrase decrypts privateKeyPath when it is an encrypted PEM.
	Passphrase string `json:"passphrase,omitempty"`

	// password is used when no private key is configured.
	Password string `json:"password,omitempty"`

	// hostKeyPolicy is Strict, TOFU or Insecure. Defaults to TOFU.
	HostKeyPolicy string `json:"hostKeyPolicy,omitempty"`
}

// HostConfig is one entry of the inventory.
type HostConfig struct {
	Name    string `json:"name"`
	Address string `json:"address"`

	// role is control-plane or worker. It becomes the kgenesis.io/role label that
	// the rendered host selectors match on.
	Role string `json:"role"`

	// labels are merged onto the Host object, so custom pools can select on them.
	Labels map[string]string `json:"labels,omitempty"`

	// Per-host SSH overrides. Empty fields fall back to the top level ssh block.
	User           string `json:"user,omitempty"`
	Port           int32  `json:"port,omitempty"`
	PrivateKeyPath string `json:"privateKeyPath,omitempty"`
	Passphrase     string `json:"passphrase,omitempty"`
	Password       string `json:"password,omitempty"`
	HostKeyPolicy  string `json:"hostKeyPolicy,omitempty"`

	// publicKey is the expected SSH host key, required under the Strict policy.
	PublicKey string `json:"publicKey,omitempty"`
}

// WorkerPool becomes one MachineDeployment.
type WorkerPool struct {
	Name     string `json:"name"`
	Replicas int32  `json:"replicas"`

	// hostSelector narrows which hosts this pool draws from. Defaults to
	// kgenesis.io/role=worker.
	HostSelector map[string]string `json:"hostSelector,omitempty"`

	// nodeLabels and nodeTaints are passed through to the kubelet registration of
	// the machines in this pool.
	NodeLabels map[string]string `json:"nodeLabels,omitempty"`
	NodeTaints []Taint           `json:"nodeTaints,omitempty"`
}

// Taint is a node taint applied at kubelet registration.
type Taint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"`
}

// BootstrapConfig tunes the ephemeral management cluster on the genesis node.
type BootstrapConfig struct {
	// clusterName is the kind cluster name. Defaults to "kgenesis-bootstrap".
	ClusterName string `json:"clusterName,omitempty"`

	// nodeImage overrides the kind node image, which also pins the bootstrap
	// cluster's Kubernetes version. Empty uses the kind default.
	NodeImage string `json:"nodeImage,omitempty"`

	// capiVersion pins the Cluster API core, bootstrap and control plane provider
	// version installed by `kgenesis init`. Empty uses the latest release
	// clusterctl resolves.
	CAPIVersion string `json:"capiVersion,omitempty"`

	// certManagerVersion pins cert-manager. Empty uses the clusterctl default.
	CertManagerVersion string `json:"certManagerVersion,omitempty"`
}
