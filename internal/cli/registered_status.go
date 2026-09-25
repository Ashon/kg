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
	corev1 "k8s.io/api/core/v1"
	controlplanev1 "sigs.k8s.io/cluster-api/api/controlplane/kubeadm/v1beta2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/kube"
	"github.com/Ashon/kg/internal/registry"
)

func newClusterStatusCommand(opts *Options) *cobra.Command {
	var selector string
	var watch bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use: "status", Short: "Show cluster status through its current management location",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := opts.selectRecord(selector)
			if err != nil {
				return err
			}
			// Preserve the normal pre-init response, but never mistake a
			// registered cluster with a lost management plane for an unbuilt one.
			if selector == "" && r.Mode == registry.Managed && !opts.bootstrapClusterExists() {
				saved, err := opts.registry().Get(r.Namespace, r.Name)
				if err != nil {
					return err
				}
				if saved == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Cluster %s has not been built yet. Run `%s` first.\n", r.Name, invoke("init"))
					return nil
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			path := r.ManagementKubeconfig
			if r.Mode == registry.Released {
				path = r.WorkloadKubeconfig
			}
			c, err := kube.NewClient(path)
			if err != nil {
				return fmt.Errorf("%s cluster %s/%s: %w", r.Mode, r.Namespace, r.Name, err)
			}
			for {
				fmt.Fprintf(cmd.OutOrStdout(), "Management: %s\n", r.Mode)
				var ready bool
				if r.Mode == registry.Released {
					ready, err = printReleasedStatus(ctx, c, r, cmd.OutOrStdout())
				} else {
					var s *summary
					var cfg *config.Config
					cfg, err = liveClusterConfig(ctx, c, r)
					if err == nil {
						s, err = clusterSummary(ctx, c, cfg)
					}
					if err == nil {
						printStatus(cmd.OutOrStdout(), cfg, s)
						ready = s.ready()
					}
				}
				if err != nil {
					return fmt.Errorf("query %s cluster %s/%s: %w", r.Mode, r.Namespace, r.Name, err)
				}
				if !watch || ready {
					return nil
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(15 * time.Second):
				}
			}
		},
	}
	cmd.Flags().StringVar(&selector, "cluster", "", "Registered cluster name or namespace/name (default: configuration)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Keep printing until the cluster is ready")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "Time budget for status queries")
	return cmd
}

// Read desired replicas from the current management API. The original config
// can be gone, or out of date after a self-managed cluster was scaled directly.
func liveClusterConfig(ctx context.Context, c client.Client, r *registry.Record) (*config.Config, error) {
	cfg := &config.Config{Cluster: config.ClusterConfig{Name: r.Name, Namespace: r.Namespace}}
	filters := []client.ListOption{client.InNamespace(r.Namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: r.Name}}
	cps := &controlplanev1.KubeadmControlPlaneList{}
	if err := c.List(ctx, cps, filters...); err != nil {
		return nil, err
	}
	for _, cp := range cps.Items {
		if cp.Spec.Replicas != nil {
			cfg.Cluster.ControlPlaneReplicas += *cp.Spec.Replicas
		}
	}
	pools := &clusterv1.MachineDeploymentList{}
	if err := c.List(ctx, pools, filters...); err != nil {
		return nil, err
	}
	for _, pool := range pools.Items {
		replicas := int32(0)
		if pool.Spec.Replicas != nil {
			replicas = *pool.Spec.Replicas
		}
		cfg.Workers = append(cfg.Workers, config.WorkerPool{Name: pool.Name, Replicas: replicas})
	}
	return cfg, nil
}

// Released clusters have no CAPI controllers or objects to query. Node health
// is observed directly, without implying that kg can manage their lifecycle.
func printReleasedStatus(ctx context.Context, c client.Client, r *registry.Record, out io.Writer) (bool, error) {
	nodes := &corev1.NodeList{}
	if err := c.List(ctx, nodes); err != nil {
		return false, err
	}
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	fmt.Fprintf(out, "Cluster %s/%s\n  endpoint       %s\n", r.Namespace, r.Name, r.Endpoint)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NODE\tREADY\tVERSION")
	ready := 0
	for _, node := range nodes.Items {
		healthy := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				healthy = true
			}
		}
		if healthy {
			ready++
		}
		fmt.Fprintf(w, "%s\t%t\t%s\n", node.Name, healthy, node.Status.NodeInfo.KubeletVersion)
	}
	if err := w.Flush(); err != nil {
		return false, err
	}
	fmt.Fprintf(out, "  nodes          %d/%d ready\n", ready, len(nodes.Items))
	return len(nodes.Items) > 0 && ready == len(nodes.Items), nil
}
