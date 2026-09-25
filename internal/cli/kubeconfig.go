// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"context"
	"github.com/Ashon/kg/internal/capi"
	"github.com/Ashon/kg/internal/registry"
	"time"
)

func newKubeconfigCommand(opts *Options) *cobra.Command {
	var (
		selector string
		output   string
		toStdout bool
	)

	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Write the target cluster's kubeconfig",
		Long: `Reads a cluster's kubeconfig through its registered management location.
For released clusters, uses the saved workload kubeconfig without requiring CAPI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := opts.selectRecord(selector)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			kubeconfig, err := clusterKubeconfig(ctx, r)
			if err != nil {
				return err
			}

			if toStdout {
				fmt.Fprint(cmd.OutOrStdout(), kubeconfig)
				return nil
			}

			if output == "" {
				output = r.WorkloadKubeconfig
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

	cmd.Flags().StringVar(&selector, "cluster", "", "Registered cluster name or namespace/name (default: configuration)")
	cmd.Flags().StringVarP(&output, "output", "o", "",
		"Where to write the kubeconfig (default: <state-dir>/<cluster>.kubeconfig)")
	cmd.Flags().BoolVar(&toStdout, "stdout", false, "Print the kubeconfig instead of writing a file")
	return cmd
}

func clusterKubeconfig(ctx context.Context, r *registry.Record) (string, error) {
	if r.Mode == registry.Released {
		data, err := os.ReadFile(r.WorkloadKubeconfig)
		if err != nil {
			return "", fmt.Errorf("read released cluster kubeconfig: %w", err)
		}
		return string(data), nil
	}
	return capi.GetKubeconfig(ctx, r.ManagementKubeconfig, r.Name, r.Namespace)
}
