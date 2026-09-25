// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/kube"
	"github.com/Ashon/kg/internal/registry"
)

type clusterRow struct {
	record registry.Record
	phase  string
	hosts  string
}

func newClustersCommand(opts *Options) *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use: "clusters", Aliases: []string{"ls"},
		Short: "List known clusters and their management mode",
		Long: `Lists registered clusters, including those released or managing themselves.

The management mode is the last confirmed arrangement, not a health check.
When the genesis node is reachable, its clusters are also discovered and their
current phases shown. A dash means the phase or host count was not checked.
Use cluster status --cluster namespace/name for a live check of any entry.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			records, err := opts.registry().List()
			if err != nil {
				return err
			}
			rows := map[string]clusterRow{}
			for _, r := range records {
				rows[registry.Key(r.Namespace, r.Name)] = clusterRow{r, "-", "-"}
			}
			if !offline && opts.bootstrapClusterExists() {
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				defer cancel()
				c, err := kube.NewClient(opts.BootstrapKubeconfig())
				if err == nil {
					err = discoverClusters(ctx, c, opts, rows)
				}
				if err != nil {
					if len(rows) == 0 {
						return err
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "Could not refresh genesis state; showing registered clusters: %v\n", err)
				}
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No clusters are registered.")
				return nil
			}
			return printClusterRows(cmd.OutOrStdout(), rows)
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "Read the registry without contacting a cluster")
	return cmd
}

// Discover only adds unknown identities. A paused Cluster may be paused for
// maintenance or a move, so it cannot establish that kg released it.
func discoverClusters(ctx context.Context, c client.Client, opts *Options, rows map[string]clusterRow) error {
	clusters := &clusterv1.ClusterList{}
	if err := c.List(ctx, clusters); err != nil {
		return fmt.Errorf("list genesis clusters: %w", err)
	}
	hosts := &infrav1.HostList{}
	if err := c.List(ctx, hosts); err != nil {
		return fmt.Errorf("list genesis hosts: %w", err)
	}
	for _, cluster := range clusters.Items {
		key := registry.Key(cluster.Namespace, cluster.Name)
		row, exists := rows[key]
		if !exists {
			cfg := &config.Config{Cluster: config.ClusterConfig{
				Name: cluster.Name, Namespace: cluster.Namespace,
				ControlPlaneEndpoint: config.Endpoint{Host: cluster.Spec.ControlPlaneEndpoint.Host, Port: cluster.Spec.ControlPlaneEndpoint.Port},
			}}
			r, err := opts.recordFor(cfg, registry.Managed)
			if err != nil {
				return err
			}
			if err := opts.registry().Put(*r); err != nil {
				return err
			}
			row.record = *r
		}
		// A stale source object must not overwrite the target's management state.
		if row.record.Mode == registry.SelfManaged {
			continue
		}
		row.phase = string(cluster.Status.Phase)
		if row.phase == "" {
			row.phase = "-"
		}
		if cluster.Spec.Paused != nil && *cluster.Spec.Paused {
			row.phase = "Paused"
		}
		claimed, total := 0, 0
		for _, h := range hosts.Items {
			if h.Namespace != cluster.Namespace {
				continue
			}
			total++
			if h.Status.ClaimRef != nil {
				claimed++
			}
		}
		row.hosts = fmt.Sprintf("%d/%d", claimed, total)
		rows[key] = row
	}
	return nil
}

func printClusterRows(out io.Writer, rows map[string]clusterRow) error {
	sorted := make([]clusterRow, 0, len(rows))
	for _, row := range rows {
		sorted = append(sorted, row)
	}
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i].record, sorted[j].record
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CLUSTER\tNAMESPACE\tMANAGEMENT\tPHASE\tENDPOINT\tHOSTS")
	for _, row := range sorted {
		r := row.record
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Namespace, r.Mode, row.phase, r.Endpoint, row.hosts)
	}
	return w.Flush()
}
