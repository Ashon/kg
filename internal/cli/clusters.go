// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/kube"
)

// newClustersCommand lists what the genesis node is managing. A genesis node
// builds several clusters and lets them go one at a time, so "what is still
// here" is a question worth being able to ask without a kubeconfig in hand.
func newClustersCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:     "clusters",
		Aliases: []string{"ls"},
		Short:   "List the clusters this genesis node manages",
		Long: `Lists every cluster the genesis node is managing, across namespaces.

Each cluster lives in its own namespace, which is what lets one be ejected
without disturbing the others.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !opts.bootstrapClusterExists() {
				fmt.Fprintf(cmd.OutOrStdout(),
					"This genesis node has not been brought up yet.\n\n  %s\n", invoke("init"))
				return nil
			}

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			clusters := &clusterv1.ClusterList{}
			if err := c.List(cmd.Context(), clusters); err != nil {
				return fmt.Errorf("list clusters: %w", err)
			}

			if len(clusters.Items) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"The genesis node is up but manages nothing yet.\n\n  %s\n",
					invoke("cluster create"))
				return nil
			}

			hosts := &infrav1.HostList{}
			if err := c.List(cmd.Context(), hosts); err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "CLUSTER\tNAMESPACE\tPHASE\tENDPOINT\tHOSTS")
			for _, cluster := range clusters.Items {
				var claimed, total int
				for _, host := range hosts.Items {
					if host.Namespace != cluster.Namespace {
						continue
					}
					total++
					if host.Status.ClaimRef != nil {
						claimed++
					}
				}
				// A released cluster is left paused rather than deleted, so its
				// hosts stay claimed and no controller touches it. Its phase is
				// whatever it was at that moment and says nothing useful.
				phase := string(cluster.Status.Phase)
				if cluster.Spec.Paused != nil && *cluster.Spec.Paused {
					phase = "Released"
				}

				fmt.Fprintf(w, "%s\t%s\t%s\t%s:%d\t%d/%d\n",
					cluster.Name, cluster.Namespace, phase,
					cluster.Spec.ControlPlaneEndpoint.Host, cluster.Spec.ControlPlaneEndpoint.Port,
					claimed, total)
			}
			return w.Flush()
		},
	}
}
