// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package cli implements the kg command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Ashon/kg/internal/capi"
	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/registry"
)

// Options are the flags shared by every subcommand.
type Options struct {
	// ConfigPath is the configuration describing the cluster and the host pool.
	ConfigPath string

	// StateDir holds the bootstrap cluster's kubeconfig and the workload
	// kubeconfig kg writes. It sits beside the configuration, and out of
	// ~/.kube, so bootstrapping never disturbs the contexts the operator already
	// has.
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
		Long: `kg makes the machine it runs on a genesis node: a temporary Cluster API
management cluster that stamps a real Kubernetes cluster onto physical or virtual
hosts you already have, over SSH.

The usual sequence:

  ` + pad(invoke("config init")) + `write a starter configuration
  ` + pad(invoke("inventory check")) + `confirm every host is reachable and ready
  ` + pad(invoke("init")) + `bring up the bootstrap cluster and the providers
  ` + pad(invoke("cluster create")) + `stamp out the cluster and wait for it
  ` + pad(invoke("eject")) + `let the new cluster go, drop the genesis node`,
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
		defaultState = ConfigDir
	}

	cmd.PersistentFlags().StringVarP(&opts.ConfigPath, "config", "c", defaultConfigPath(),
		"Path to the kg configuration file (also KG_CONFIG)")
	cmd.PersistentFlags().StringVar(&opts.StateDir, "state-dir", defaultState,
		"Directory for the kubeconfigs kg manages")
	cmd.PersistentFlags().BoolVarP(&opts.Verbose, "verbose", "v", false,
		"Show the output of the underlying kind and clusterctl operations")

	cmd.AddCommand(
		newVersionCommand(),
		newConfigCommand(opts),
		newInventoryCommand(opts),
		newCNICommand(opts),
		newInitCommand(opts),
		newClusterCommand(opts),
		newClustersCommand(opts),
		newKubeconfigCommand(opts),
		newPivotCommand(opts),
		newResetCommand(opts),
	)

	return cmd
}

// ConfigDir is where kg keeps a configuration when none is given.
const ConfigDir = ".kg"

// ConfigFile is the name of that configuration. It is YAML, and named without an
// extension the way ~/.kube/config and ~/.gitconfig are: it is the tool's
// configuration rather than one file among several.
const ConfigFile = "config"

// defaultConfigPath resolves the configuration to use when --config is not
// given. The environment comes first, so a fleet can be selected for a shell
// without repeating the flag, the same way KUBECONFIG works.
//
// KGENESIS_CONFIG is still read, after KG_CONFIG, so a shell set up before the
// command was named kg keeps working.
func defaultConfigPath() string {
	for _, key := range []string{"KG_CONFIG", "KGENESIS_CONFIG"} {
		if fromEnv := os.Getenv(key); fromEnv != "" {
			return fromEnv
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		// Nothing better to offer; the error surfaces when the file is read.
		return filepath.Join(ConfigDir, ConfigFile)
	}
	return filepath.Join(home, ConfigDir, ConfigFile)
}

func defaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDir), nil
}

// Execute runs the CLI and turns an error into an exit code.
func Execute() {
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// requireWorkloadKubeconfig returns a kubeconfig for the target cluster, taking
// it from the management cluster whenever there is one to ask.
func (o *Options) requireWorkloadKubeconfig(ctx context.Context, cfg *config.Config) (string, error) {
	path := o.workloadPath(cfg.Cluster.Namespace, cfg.Cluster.Name)
	management := o.BootstrapKubeconfig()
	r, err := o.registry().Get(cfg.Cluster.Namespace, cfg.Cluster.Name)
	if err != nil {
		return "", err
	}
	if r != nil {
		path = r.WorkloadKubeconfig
		if r.Mode == registry.Released {
			if _, err := os.Stat(path); err != nil {
				return "", fmt.Errorf("released cluster kubeconfig: %w", err)
			}
			return path, nil
		}
		management = r.ManagementKubeconfig
	}

	if _, err := os.Stat(management); err != nil {
		// Nothing left to ask. A released cluster's kubeconfig is all there is,
		// and it is still good: nothing has rebuilt the cluster since.
		if _, statErr := os.Stat(path); statErr == nil {
			return path, nil
		}
		return "", fmt.Errorf("no management kubeconfig in %s; run `%s` first", o.StateDir, invoke("init"))
	}

	// The copy on disk is a convenience, not the record. A cluster torn down and
	// built again on the same hosts has a new certificate authority, and the old
	// file then fails with a TLS error naming neither the cluster nor the reason.
	// While the genesis node is there to ask, what it holds wins.

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

// bootstrapClusterExists reports whether `init` has been run for this state
// directory. It is the difference between a command that cannot answer and one
// whose answer is "nothing has been built yet".
func (o *Options) bootstrapClusterExists() bool {
	_, err := os.Stat(o.BootstrapKubeconfig())
	return err == nil
}
