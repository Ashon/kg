// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Ashon/kg/internal/capi"
)

func newKubeconfigCommand(opts *Options) *cobra.Command {
	var (
		output   string
		toStdout bool
	)

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Write the target cluster's kubeconfig",
		Long: `Reads the workload cluster's kubeconfig from the management cluster, where
Cluster API stores it as a Secret once the control plane is up.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			// After a pivot the management cluster is the workload cluster itself,
			// so fall back to its own kubeconfig when the bootstrap one is gone.
			management := opts.BootstrapKubeconfig()
			if _, err := os.Stat(management); err != nil {
				management = opts.WorkloadKubeconfig(cfg.Cluster.Name)
				if _, err := os.Stat(management); err != nil {
					return fmt.Errorf("no management kubeconfig found in %s; run `%s` first", opts.StateDir, invoke("init"))
				}
			}

			kubeconfig, err := capi.GetKubeconfig(cmd.Context(), management, cfg.Cluster.Name, cfg.Cluster.Namespace)
			if err != nil {
				return err
			}

			if toStdout {
				fmt.Fprint(cmd.OutOrStdout(), kubeconfig)
				return nil
			}

			if output == "" {
				output = opts.WorkloadKubeconfig(cfg.Cluster.Name)
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(output, []byte(kubeconfig), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", output, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n\n  export KUBECONFIG=%s\n", output, output)
			return nil
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "",
		"Where to write the kubeconfig (default: <state-dir>/<cluster>.kubeconfig)")
	cmd.Flags().BoolVar(&toStdout, "stdout", false, "Print the kubeconfig instead of writing a file")
	return cmd
}
