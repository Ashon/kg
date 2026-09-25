// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/Ashon/kg/internal/bootstrap"
)

func newResetCommand(opts *Options) *cobra.Command {
	var (
		yes         bool
		clusterName string
	)

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete the bootstrap cluster on this genesis node",
		Long: `Removes the ephemeral kind cluster and the kubeconfig kg wrote for it.

This touches nothing on the hosts. Use ` + "`" + invoke("cluster delete") + "`" + ` first if
you want the hosts reset and returned to the pool.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Teardown must not depend on the configuration parsing: an edit that
			// breaks the file would otherwise leave no way to remove the cluster it
			// created. --name covers that case.
			name := clusterName
			if name == "" {
				cfg, err := opts.Load()
				if err != nil {
					return fmt.Errorf("%w\n\nPass --name to delete a bootstrap cluster without reading the config", err)
				}
				name = cfg.Bootstrap.ClusterName
			}

			out := cmd.OutOrStdout()
			kindCluster := bootstrap.New(name, io.Discard)

			exists, err := kindCluster.Exists()
			if err != nil {
				return err
			}
			if !exists {
				fmt.Fprintf(out, "Bootstrap cluster %q is not running.\n", name)
				return nil
			}

			if !yes {
				fmt.Fprintf(out,
					"This deletes the bootstrap cluster %q. Hosts are not touched.\nRe-run with --yes to proceed.\n",
					name)
				return nil
			}

			step(out, "Deleting the bootstrap cluster %q", name)
			if err := kindCluster.Delete(opts.BootstrapKubeconfig()); err != nil {
				return err
			}
			if err := os.Remove(opts.BootstrapKubeconfig()); err != nil && !os.IsNotExist(err) {
				return err
			}

			fmt.Fprintf(out, "Done.\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Proceed without the confirmation prompt")
	cmd.Flags().StringVar(&clusterName, "name", "",
		"Bootstrap cluster to delete, bypassing the configuration file")
	return cmd
}
