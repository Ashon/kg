// Package cli implements the kgenesis command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Ashon/kgenesis/internal/capi"
	"github.com/Ashon/kgenesis/internal/config"
)

// Options are the flags shared by every subcommand.
type Options struct {
	// ConfigPath is the kgenesis.yaml describing the cluster and the host pool.
	ConfigPath string

	// StateDir holds the bootstrap cluster's kubeconfig and the workload
	// kubeconfig kgenesis writes. Keeping them out of ~/.kube means bootstrapping
	// never disturbs the contexts the operator already has.
	StateDir string

	// Verbose turns on the underlying kind and clusterctl output.
	Verbose bool
}

// BootstrapKubeconfig is where the ephemeral management cluster's kubeconfig lives.
func (o *Options) BootstrapKubeconfig() string {
	return filepath.Join(o.StateDir, "bootstrap.kubeconfig")
}

// WorkloadKubeconfig is where the target cluster's kubeconfig is written.
func (o *Options) WorkloadKubeconfig(clusterName string) string {
	return filepath.Join(o.StateDir, clusterName+".kubeconfig")
}

// Load reads and validates the config file.
func (o *Options) Load() (*config.Config, error) {
	return config.Load(o.ConfigPath)
}

// NewRootCommand builds the command tree.
func NewRootCommand() *cobra.Command {
	opts := &Options{}

	cmd := &cobra.Command{
		Use:   binaryName,
		Short: "Turn a pool of pre-provisioned hosts into a Kubernetes cluster",
		Long: `kgenesis makes the machine it runs on a genesis node: a temporary Cluster API
management cluster that stamps a real Kubernetes cluster onto physical or virtual
hosts you already have, over SSH.

The usual sequence:

  ` + pad(invoke("config init")) + `write a starter kgenesis.yaml
  ` + pad(invoke("inventory check")) + `confirm every host is reachable and ready
  ` + pad(invoke("init")) + `bring up the bootstrap cluster and the providers
  ` + pad(invoke("cluster create")) + `stamp out the cluster and wait for it
  ` + pad(invoke("pivot")) + `hand management to the new cluster, drop the genesis node`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			// Two libraries log on their own. clusterctl prints a stack trace for
			// every retried API call while the bootstrap cluster settles, and
			// controller-runtime prints one the first time something logs through
			// it without a logger having been set. Both read as a crash in the
			// middle of a normal run, so they are quiet unless asked for.
			logger := logr.Discard()
			if opts.Verbose {
				logger = capi.TextLogger(cmd.ErrOrStderr())
			}
			capi.SetLogger(logger)
			ctrllog.SetLogger(logger)
		},
	}

	defaultState, err := defaultStateDir()
	if err != nil {
		defaultState = ".kgenesis"
	}

	cmd.PersistentFlags().StringVarP(&opts.ConfigPath, "config", "c", "kgenesis.yaml",
		"Path to the kgenesis configuration file")
	cmd.PersistentFlags().StringVar(&opts.StateDir, "state-dir", defaultState,
		"Directory for the kubeconfigs kgenesis manages")
	cmd.PersistentFlags().BoolVarP(&opts.Verbose, "verbose", "v", false,
		"Show the output of the underlying kind and clusterctl operations")

	cmd.AddCommand(
		newVersionCommand(),
		newConfigCommand(opts),
		newInventoryCommand(opts),
		newCNICommand(opts),
		newInitCommand(opts),
		newClusterCommand(opts),
		newKubeconfigCommand(opts),
		newPivotCommand(opts),
		newResetCommand(opts),
	)

	return cmd
}

func defaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".kgenesis"), nil
}

// Execute runs the CLI and turns an error into an exit code.
func Execute() {
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// requireWorkloadKubeconfig returns a kubeconfig for the target cluster, writing
// one from the management cluster when it is not on disk yet.
func (o *Options) requireWorkloadKubeconfig(ctx context.Context, cfg *config.Config) (string, error) {
	path := o.WorkloadKubeconfig(cfg.Cluster.Name)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	management := o.BootstrapKubeconfig()
	if _, err := os.Stat(management); err != nil {
		return "", fmt.Errorf("no management kubeconfig in %s; run `%s` first", o.StateDir, invoke("init"))
	}

	kubeconfig, err := capi.GetKubeconfig(ctx, management, cfg.Cluster.Name, cfg.Cluster.Namespace)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
