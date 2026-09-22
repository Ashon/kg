package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// Defaults applied when a field is left empty.
const (
	DefaultNamespace            = "default"
	DefaultAPIServerPort  int32 = 6443
	DefaultSSHPort        int32 = 22
	DefaultSSHUser              = "root"
	DefaultPodCIDR              = "10.244.0.0/16"
	DefaultServiceCIDR          = "10.96.0.0/12"
	DefaultServiceDomain        = "cluster.local"
	DefaultHostKeyPolicy        = "TOFU"
	DefaultBootstrapName        = "kgenesis-bootstrap"
	DefaultWorkerPoolName       = "default"
)

var dnsName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// isURL distinguishes a remote manifest from a path on the genesis node.
func isURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Load reads, defaults and validates kgenesis.yaml.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &Config{}
	if err := yaml.UnmarshalStrict(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Relative paths in the config are resolved against the config file, not the
	// working directory, so `kgenesis -c ../lab/kgenesis.yaml` keeps working.
	cfg.resolvePaths(filepath.Dir(path))
	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) resolvePaths(base string) {
	c.SSH.PrivateKeyPath = resolvePath(base, c.SSH.PrivateKeyPath)
	for i, manifest := range c.Cluster.CNI.Manifests {
		// URLs are passed through; only local paths are rebased.
		if !isURL(manifest) {
			c.Cluster.CNI.Manifests[i] = resolvePath(base, manifest)
		}
	}
	for i := range c.Hosts {
		c.Hosts[i].PrivateKeyPath = resolvePath(base, c.Hosts[i].PrivateKeyPath)
	}
}

func resolvePath(base, p string) string {
	switch {
	case p == "":
		return ""
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
		return p
	default:
		return filepath.Join(base, p)
	}
}

// ApplyDefaults fills in every optional field. It is safe to call more than once.
func (c *Config) ApplyDefaults() {
	if c.Cluster.Namespace == "" {
		c.Cluster.Namespace = DefaultNamespace
	}
	if c.Cluster.ControlPlaneEndpoint.Port == 0 {
		c.Cluster.ControlPlaneEndpoint.Port = DefaultAPIServerPort
	}
	if c.Cluster.Network.PodCIDR == "" {
		c.Cluster.Network.PodCIDR = DefaultPodCIDR
	}
	if c.Cluster.Network.ServiceCIDR == "" {
		c.Cluster.Network.ServiceCIDR = DefaultServiceCIDR
	}
	if c.Cluster.Network.ServiceDomain == "" {
		c.Cluster.Network.ServiceDomain = DefaultServiceDomain
	}
	if c.SSH.User == "" {
		c.SSH.User = DefaultSSHUser
	}
	if c.SSH.Port == 0 {
		c.SSH.Port = DefaultSSHPort
	}
	if c.SSH.HostKeyPolicy == "" {
		c.SSH.HostKeyPolicy = DefaultHostKeyPolicy
	}

	for i := range c.Hosts {
		h := &c.Hosts[i]
		if h.User == "" {
			h.User = c.SSH.User
		}
		if h.Port == 0 {
			h.Port = c.SSH.Port
		}
		if h.PrivateKeyPath == "" {
			h.PrivateKeyPath = c.SSH.PrivateKeyPath
		}
		if h.Passphrase == "" {
			h.Passphrase = c.SSH.Passphrase
		}
		if h.Password == "" {
			h.Password = c.SSH.Password
		}
		if h.HostKeyPolicy == "" {
			h.HostKeyPolicy = c.SSH.HostKeyPolicy
		}
	}

	if c.Cluster.ControlPlaneReplicas == 0 {
		c.Cluster.ControlPlaneReplicas = int32(len(c.HostsWithRole(RoleControlPlane)))
	}

	// With no explicit pools, every worker host lands in one default pool.
	if len(c.Workers) == 0 {
		if n := len(c.HostsWithRole(RoleWorker)); n > 0 {
			c.Workers = []WorkerPool{{Name: DefaultWorkerPoolName, Replicas: int32(n)}}
		}
	}
	for i := range c.Workers {
		if len(c.Workers[i].HostSelector) == 0 {
			c.Workers[i].HostSelector = map[string]string{RoleLabel: RoleWorker}
		}
	}

	if c.Bootstrap.ClusterName == "" {
		c.Bootstrap.ClusterName = DefaultBootstrapName
	}
}

// Role values accepted in the inventory, and the label they map to.
const (
	RoleControlPlane = "control-plane"
	RoleWorker       = "worker"
	RoleLabel        = "kgenesis.io/role"
)

// HostsWithRole returns every inventory entry carrying the given role.
func (c *Config) HostsWithRole(role string) []HostConfig {
	var out []HostConfig
	for _, h := range c.Hosts {
		if h.Role == role {
			out = append(out, h)
		}
	}
	return out
}

// Validate reports every problem it finds at once, so a broken inventory takes
// one round trip to fix instead of one per host.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.APIVersion != APIVersion {
		add("apiVersion must be %q, got %q", APIVersion, c.APIVersion)
	}
	if c.Kind != Kind {
		add("kind must be %q, got %q", Kind, c.Kind)
	}

	if c.Cluster.Name == "" {
		add("cluster.name is required")
	} else if !dnsName.MatchString(c.Cluster.Name) {
		add("cluster.name %q must be a lowercase DNS label", c.Cluster.Name)
	}
	if c.Cluster.KubernetesVersion == "" {
		add("cluster.kubernetesVersion is required, e.g. v1.34.1")
	} else if !strings.HasPrefix(c.Cluster.KubernetesVersion, "v") {
		add("cluster.kubernetesVersion %q must start with v", c.Cluster.KubernetesVersion)
	}
	if c.Cluster.ControlPlaneEndpoint.Host == "" {
		add("cluster.controlPlaneEndpoint.host is required")
	}
	for field, cidr := range map[string]string{
		"cluster.network.podCIDR":     c.Cluster.Network.PodCIDR,
		"cluster.network.serviceCIDR": c.Cluster.Network.ServiceCIDR,
	} {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			add("%s %q is not a valid CIDR", field, cidr)
		}
	}
	for i, manifest := range c.Cluster.CNI.Manifests {
		if isURL(manifest) {
			continue
		}
		if _, err := os.Stat(manifest); err != nil {
			add("cluster.cni.manifests[%d] %q is not readable: %v", i, manifest, err)
		}
	}

	if len(c.Hosts) == 0 {
		add("hosts must contain at least one entry")
	}
	seenName := map[string]bool{}
	seenAddr := map[string]bool{}
	for i, h := range c.Hosts {
		where := fmt.Sprintf("hosts[%d]", i)
		switch {
		case h.Name == "":
			add("%s.name is required", where)
		case !dnsName.MatchString(h.Name):
			add("%s.name %q must be a lowercase DNS label", where, h.Name)
		case seenName[h.Name]:
			add("%s.name %q is used more than once", where, h.Name)
		default:
			seenName[h.Name] = true
		}

		if h.Address == "" {
			add("%s.address is required", where)
		} else if seenAddr[h.Address] {
			add("%s.address %q is used more than once", where, h.Address)
		} else {
			seenAddr[h.Address] = true
		}

		if h.Role != RoleControlPlane && h.Role != RoleWorker {
			add("%s.role %q must be %q or %q", where, h.Role, RoleControlPlane, RoleWorker)
		}
		if h.PrivateKeyPath == "" && h.Password == "" {
			add("%s has no credential: set ssh.privateKeyPath, ssh.password or a per-host override", where)
		}
		if h.PrivateKeyPath != "" {
			if _, err := os.Stat(h.PrivateKeyPath); err != nil {
				add("%s.privateKeyPath %q is not readable: %v", where, h.PrivateKeyPath, err)
			}
		}
		switch h.HostKeyPolicy {
		case "Strict":
			if h.PublicKey == "" {
				add("%s.publicKey is required when hostKeyPolicy is Strict", where)
			}
		case "TOFU", "Insecure":
		default:
			add("%s.hostKeyPolicy %q must be Strict, TOFU or Insecure", where, h.HostKeyPolicy)
		}
	}

	cpHosts := int32(len(c.HostsWithRole(RoleControlPlane)))
	switch {
	case c.Cluster.ControlPlaneReplicas < 1:
		add("cluster.controlPlaneReplicas must be at least 1; no host has role %q", RoleControlPlane)
	case c.Cluster.ControlPlaneReplicas%2 == 0:
		add("cluster.controlPlaneReplicas is %d; etcd quorum needs an odd number", c.Cluster.ControlPlaneReplicas)
	case c.Cluster.ControlPlaneReplicas > cpHosts:
		add("cluster.controlPlaneReplicas is %d but only %d host(s) have role %q",
			c.Cluster.ControlPlaneReplicas, cpHosts, RoleControlPlane)
	}

	seenPool := map[string]bool{}
	var workerReplicas int32
	for i, w := range c.Workers {
		where := fmt.Sprintf("workers[%d]", i)
		switch {
		case w.Name == "":
			add("%s.name is required", where)
		case !dnsName.MatchString(w.Name):
			add("%s.name %q must be a lowercase DNS label", where, w.Name)
		case seenPool[w.Name]:
			add("%s.name %q is used more than once", where, w.Name)
		default:
			seenPool[w.Name] = true
		}
		if w.Replicas < 0 {
			add("%s.replicas must not be negative", where)
		}
		workerReplicas += w.Replicas
		for j, t := range w.NodeTaints {
			switch t.Effect {
			case "NoSchedule", "PreferNoSchedule", "NoExecute":
			default:
				add("%s.nodeTaints[%d].effect %q must be NoSchedule, PreferNoSchedule or NoExecute", where, j, t.Effect)
			}
			if t.Key == "" {
				add("%s.nodeTaints[%d].key is required", where, j)
			}
		}
	}

	// Every Machine claims exactly one Host, so the pool has to be big enough.
	if want := c.Cluster.ControlPlaneReplicas + workerReplicas; want > int32(len(c.Hosts)) {
		add("%d machines requested (%d control plane + %d worker) but the inventory has only %d host(s)",
			want, c.Cluster.ControlPlaneReplicas, workerReplicas, len(c.Hosts))
	}

	return errors.Join(errs...)
}
