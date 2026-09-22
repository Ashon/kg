// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Ashon/kgenesis/internal/config"
	"github.com/Ashon/kgenesis/internal/kube"
)

// manifestFetchTimeout bounds a single remote manifest download.
const manifestFetchTimeout = 60 * time.Second

func newCNICommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cni",
		Short: "Install the CNI into the target cluster",
	}
	cmd.AddCommand(newCNIInstallCommand(opts))
	return cmd
}

func newCNIInstallCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Apply cluster.cni.manifests to the target cluster",
		Long: `Applies the manifests listed under cluster.cni in the configuration.

Until a CNI is installed the nodes stay NotReady, so this runs automatically at
the end of ` + "`" + invoke("cluster create --wait") + "`" + `. Run it by hand when you
skipped the wait, or after changing the manifests.

kgenesis does not bundle a CNI. For a chart-based one, render it first:

  helm template cilium cilium/cilium --version 1.16.5 \
    --namespace kube-system > cni/cilium.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			if !cfg.Cluster.CNI.HasCNI() {
				fmt.Fprintf(cmd.OutOrStdout(),
					"No manifests are configured under cluster.cni; nothing to install.\n")
				return nil
			}

			kubeconfig, err := opts.requireWorkloadKubeconfig(cmd.Context(), cfg)
			if err != nil {
				return err
			}

			c, err := kube.NewClient(kubeconfig)
			if err != nil {
				return err
			}

			return installCNI(cmd.Context(), c, cfg, cmd.OutOrStdout())
		},
	}
}

// installCNI applies every configured manifest to the workload cluster.
func installCNI(ctx context.Context, c client.Client, cfg *config.Config, out io.Writer) error {
	for _, source := range cfg.Cluster.CNI.Manifests {
		step(out, "Applying %s", source)

		data, err := readManifest(ctx, source)
		if err != nil {
			return err
		}
		if err := kube.ApplyManifest(ctx, c, data); err != nil {
			return fmt.Errorf("apply %s: %w", source, err)
		}
	}
	return nil
}

func readManifest(ctx context.Context, source string) ([]byte, error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		data, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", source, err)
		}
		return data, nil
	}

	ctx, cancel := context.WithTimeout(ctx, manifestFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", source, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: %s", source, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	return data, nil
}
