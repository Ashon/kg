package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/Ashon/kgenesis/internal/assets"
	"github.com/Ashon/kgenesis/internal/bootstrap"
	"github.com/Ashon/kgenesis/internal/capi"
	"github.com/Ashon/kgenesis/internal/kube"
)

func newPivotCommand(opts *Options) *cobra.Command {
	var (
		dryRun        bool
		keepBootstrap bool
		providerImage string
		waitTimeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "pivot",
		Short: "Hand cluster management to the new cluster and drop the genesis node",
		Long: `Moves the Cluster API objects from the bootstrap cluster into the cluster it
built, so that cluster manages itself. The bootstrap kind cluster is then deleted
and the genesis node has no further role.

The target cluster must already be ready: moving to a cluster whose control plane
is not up would leave the objects with no controllers to act on them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
			defer cancel()

			out := cmd.OutOrStdout()
			bootstrapKubeconfig := opts.BootstrapKubeconfig()

			bootstrapClient, err := kube.NewClient(bootstrapKubeconfig)
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			step(out, "Checking that %s is ready to take over", cfg.Cluster.Name)
			summary, err := clusterSummary(ctx, bootstrapClient, cfg)
			if err != nil {
				return err
			}
			if !summary.ready() {
				return fmt.Errorf(
					"cluster %s is not ready yet (control plane %d/%d, machines %d/%d); "+
						"run `"+invoke("cluster status --watch")+"`",
					cfg.Cluster.Name, summary.controlPlaneReady, summary.controlPlaneDesired,
					summary.machinesRunning, len(summary.machines))
			}

			step(out, "Fetching the target cluster's kubeconfig")
			workloadKubeconfig := opts.WorkloadKubeconfig(cfg.Cluster.Name)
			kubeconfig, err := capi.GetKubeconfig(ctx, bootstrapKubeconfig, cfg.Cluster.Name, cfg.Cluster.Namespace)
			if err != nil {
				return err
			}
			if err := os.WriteFile(workloadKubeconfig, []byte(kubeconfig), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", workloadKubeconfig, err)
			}

			step(out, "Installing the providers into %s", cfg.Cluster.Name)
			if err := installTargetProviders(ctx, workloadKubeconfig, cfg.Bootstrap.CAPIVersion, providerImage, waitTimeout); err != nil {
				return err
			}

			step(out, "Moving the Cluster API objects")
			if err := capi.Move(ctx, capi.MoveOptions{
				FromKubeconfig: bootstrapKubeconfig,
				ToKubeconfig:   workloadKubeconfig,
				Namespace:      cfg.Cluster.Namespace,
				DryRun:         dryRun,
			}); err != nil {
				return err
			}
			if dryRun {
				fmt.Fprintf(out, "\nDry run only; nothing was moved and the bootstrap cluster is untouched.\n")
				return nil
			}

			if err := verifyMoved(ctx, workloadKubeconfig, cfg.Cluster.Namespace, cfg.Cluster.Name); err != nil {
				return err
			}

			if keepBootstrap {
				fmt.Fprintf(out, "\nPivot complete. The bootstrap cluster was kept as requested.\n")
			} else {
				step(out, "Deleting the bootstrap cluster")
				var kindLog io.Writer = io.Discard
				if opts.Verbose {
					kindLog = cmd.ErrOrStderr()
				}
				if err := bootstrap.New(cfg.Bootstrap.ClusterName, kindLog).Delete(bootstrapKubeconfig); err != nil {
					return err
				}
			}

			fmt.Fprintf(out, "\n%s now manages itself.\n\n", cfg.Cluster.Name)
			fmt.Fprintf(out, "  export KUBECONFIG=%s\n", workloadKubeconfig)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Check the move without performing it")
	cmd.Flags().BoolVar(&keepBootstrap, "keep-bootstrap", false,
		"Leave the bootstrap kind cluster running after the move")
	cmd.Flags().StringVar(&providerImage, "provider-image", "",
		"Override the kgenesis controller image installed into the target cluster")
	cmd.Flags().DurationVar(&waitTimeout, "timeout", 20*time.Minute, "Time budget for the pivot")
	return cmd
}

// installTargetProviders puts the same controllers into the target cluster.
// clusterctl move transfers objects, not the controllers that act on them, so
// without this step the moved objects would sit untouched.
func installTargetProviders(ctx context.Context, kubeconfig, capiVersion, providerImage string, timeout time.Duration) error {
	if _, err := capi.Init(ctx, capi.InitOptions{
		KubeconfigPath: kubeconfig,
		Version:        capiVersion,
		WaitTimeout:    timeout,
	}); err != nil {
		return err
	}
	return installProvider(ctx, kubeconfig, providerImage)
}

// verifyMoved confirms the Cluster landed before the bootstrap cluster is
// destroyed: deleting it while the move was incomplete would strand the cluster
// with no management plane at all.
func verifyMoved(ctx context.Context, kubeconfig, namespace, name string) error {
	c, err := kube.NewClient(kubeconfig)
	if err != nil {
		return err
	}

	cluster := &clusterv1.Cluster{}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := c.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("cluster %s is not present in the target cluster after the move, "+
			"so the bootstrap cluster was left alone: %w", key, err)
	}

	if _, err := kube.DecodeManifest(assets.ProviderComponents); err != nil {
		return err
	}
	return nil
}
