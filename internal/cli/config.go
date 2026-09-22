package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Ashon/kgenesis/internal/config"
)

func newConfigCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create and check the kgenesis configuration file",
	}
	cmd.AddCommand(newConfigInitCommand(opts), newConfigValidateCommand(opts))
	return cmd
}

func newConfigInitCommand(opts *Options) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter kgenesis.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := os.Stat(opts.ConfigPath); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", opts.ConfigPath)
			}
			if err := os.WriteFile(opts.ConfigPath, []byte(sampleConfig), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", opts.ConfigPath, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n\n", opts.ConfigPath)
			fmt.Fprintf(cmd.OutOrStdout(),
				"Fill in your hosts and the control plane VIP, then run:\n\n  %s\n", invoke("inventory check"))
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing configuration file")
	return cmd
}

func newConfigValidateCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the configuration file without contacting anything",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s is valid.\n\n", opts.ConfigPath)
			fmt.Fprintf(out, "  cluster              %s (namespace %s)\n", cfg.Cluster.Name, cfg.Cluster.Namespace)
			fmt.Fprintf(out, "  kubernetes           %s\n", cfg.Cluster.KubernetesVersion)
			fmt.Fprintf(out, "  endpoint             %s:%d\n",
				cfg.Cluster.ControlPlaneEndpoint.Host, cfg.Cluster.ControlPlaneEndpoint.Port)
			fmt.Fprintf(out, "  control plane        %d replica(s) from %d host(s)\n",
				cfg.Cluster.ControlPlaneReplicas, len(cfg.HostsWithRole(config.RoleControlPlane)))

			for _, pool := range cfg.Workers {
				fmt.Fprintf(out, "  worker pool %-8s %d replica(s)\n", pool.Name, pool.Replicas)
			}
			fmt.Fprintf(out, "  inventory            %d host(s)\n", len(cfg.Hosts))

			if cfg.Cluster.CNI.HasCNI() {
				for i, manifest := range cfg.Cluster.CNI.Manifests {
					label := "cni"
					if i > 0 {
						label = ""
					}
					fmt.Fprintf(out, "  %-20s %s\n", label, manifest)
				}
			} else {
				fmt.Fprintf(out, "  cni                  none configured; nodes will stay NotReady\n")
			}
			return nil
		},
	}
}

const sampleConfig = `apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig

cluster:
  name: lab
  # The version kubeadm installs on the hosts. There is no default: pick the
  # version you actually want rather than inheriting whatever kgenesis was built
  # against.
  kubernetesVersion: v1.34.1

  # Every node joins through this address. With virtualIP enabled below, kube-vip
  # raises it on whichever control plane host holds the lease, so it must be a
  # free address on the control plane hosts' subnet.
  controlPlaneEndpoint:
    host: 10.10.0.100
    port: 6443

  # Leave at 0 to use every host with role control-plane. etcd needs an odd count.
  controlPlaneReplicas: 0

  network:
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12

  # Applied to the workload cluster once its API server answers. kgenesis does
  # not bundle a CNI: give it manifests, so the version is yours to pin and an
  # air-gapped install works the same way. For a chart-based CNI, render it once:
  #
  #   helm template cilium cilium/cilium --version 1.16.5 \
  #     --namespace kube-system > cni/cilium.yaml
  #
  # Without this the nodes come up NotReady.
  cni:
    manifests: []

  virtualIP:
    enabled: true
    # Leave empty to let kube-vip pick the interface holding the default route.
    interface: ""

  # Run on every node around kubeadm, for whatever this fleet needs that
  # kgenesis does not model. They run under set -e, so a command that is only
  # sometimes necessary has to tolerate its own absence.
  # preKubeadmCommands:
  #   - /opt/vendor/prepare-nic.sh
  # postKubeadmCommands:
  #   - systemctl enable --now node-exporter

  # kubeadm preflight checks to downgrade to warnings, by their kubeadm name.
  # Keep the list as short as the hardware allows.
  # ignorePreflightErrors:
  #   - NumCPU

# Defaults for every host. Any host may override them.
ssh:
  user: root
  port: 22
  privateKeyPath: ~/.ssh/id_ed25519
  # Strict pins each host's key (set publicKey per host), TOFU trusts the first
  # connection and pins it, Insecure accepts any key.
  hostKeyPolicy: TOFU

hosts:
  - name: cp-1
    address: 10.10.0.11
    role: control-plane
  - name: cp-2
    address: 10.10.0.12
    role: control-plane
  - name: cp-3
    address: 10.10.0.13
    role: control-plane
  - name: worker-1
    address: 10.10.0.21
    role: worker
  - name: worker-2
    address: 10.10.0.22
    role: worker

# Optional. Without this block every host with role worker lands in one pool.
# workers:
#   - name: gpu
#     replicas: 2
#     hostSelector:
#       kgenesis.io/role: worker
#       accelerator: gpu
#     nodeLabels:
#       node.kubernetes.io/accelerator: gpu
#     nodeTaints:
#       - key: accelerator
#         value: gpu
#         effect: NoSchedule

bootstrap:
  clusterName: kgenesis-bootstrap
  # Pin the Cluster API providers. Empty tracks the latest release.
  capiVersion: ""
`
