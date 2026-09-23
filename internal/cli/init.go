// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/assets"
	"github.com/Ashon/kgenesis/internal/bootstrap"
	"github.com/Ashon/kgenesis/internal/capi"
	"github.com/Ashon/kgenesis/internal/config"
	"github.com/Ashon/kgenesis/internal/kube"
	"github.com/Ashon/kgenesis/internal/render"
)

// Names inside the embedded provider manifest.
const (
	providerDeployment = "kgenesis-controller-manager"
	providerContainer  = "manager"
)

func newInitCommand(opts *Options) *cobra.Command {
	var (
		providerImage string
		loadImage     bool
		waitTimeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bring up the bootstrap cluster and install the providers",
		Long: `Turns this machine into a genesis node.

It creates a kind cluster, installs cert-manager, the Cluster API core,
the kubeadm bootstrap and control plane providers, and the kgenesis
infrastructure provider, then loads the host inventory into it.

Nothing is provisioned on the hosts yet; that is ` + "`" + invoke("cluster create") + "`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
			defer cancel()

			out := cmd.OutOrStdout()
			kubeconfig := opts.BootstrapKubeconfig()

			var kindLog io.Writer
			if opts.Verbose {
				kindLog = cmd.ErrOrStderr()
			} else {
				kindLog = io.Discard
			}

			step(out, "Creating the bootstrap cluster %q", cfg.Bootstrap.ClusterName)
			kindCluster := bootstrap.New(cfg.Bootstrap.ClusterName, kindLog)
			if err := kindCluster.Create(ctx, bootstrap.CreateOptions{
				NodeImage:      cfg.Bootstrap.NodeImage,
				KubeconfigPath: kubeconfig,
			}); err != nil {
				return err
			}
			fmt.Fprintf(out, "  kubeconfig: %s\n", kubeconfig)

			step(out, "Installing cert-manager and the Cluster API providers")
			if _, err := capi.Init(ctx, capi.InitOptions{
				KubeconfigPath: kubeconfig,
				Version:        cfg.Bootstrap.CAPIVersion,
				WaitTimeout:    waitTimeout,
			}); err != nil {
				return err
			}

			step(out, "Installing the kgenesis infrastructure provider")
			image, err := providerImageFor(providerImage)
			if err != nil {
				return err
			}
			if err := installProvider(ctx, kubeconfig, providerImage); err != nil {
				return err
			}

			// A genesis node is usually the machine the provider was built on,
			// and that image has never been pushed, so kubelet would sit in
			// ImagePullBackOff waiting for a registry that does not have it.
			// Having it locally is the answer; --load-image only forces the
			// same path when the daemon cannot be asked.
			if loadImage || bootstrap.ImageAvailableLocally(ctx, image) {
				step(out, "Loading %s into the bootstrap cluster", image)
				if err := kindCluster.LoadImage(ctx, image); err != nil {
					return err
				}
			}

			step(out, "Loading the host inventory")
			applied, err := applyInventory(ctx, kubeconfig, cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "  %d host(s) in namespace %s\n", applied, cfg.Cluster.Namespace)

			fmt.Fprintf(out, "\nGenesis node is ready.\n\n")
			fmt.Fprintf(out, "  %swatch the hosts become Available\n", pad(invoke("inventory list")))
			fmt.Fprintf(out, "  %sstamp out %s\n", pad(invoke("cluster create")), cfg.Cluster.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&providerImage, "provider-image", "",
		"Override the kgenesis controller image, for running a locally built provider")
	cmd.Flags().BoolVar(&loadImage, "load-image", false,
		"Load the controller image from the local Docker daemon even when it is not visible there")
	cmd.Flags().DurationVar(&waitTimeout, "timeout", 15*time.Minute,
		"Time budget for the whole initialisation")

	return cmd
}

func installProvider(ctx context.Context, kubeconfig, image string) error {
	c, err := kube.NewClient(kubeconfig)
	if err != nil {
		return err
	}

	objects, err := kube.DecodeManifest(assets.ProviderComponents)
	if err != nil {
		return err
	}

	if image != "" {
		if err := kube.SetDeploymentImage(objects, providerDeployment, providerContainer, image); err != nil {
			return err
		}
	}

	return kube.ApplyObjects(ctx, c, objects)
}

// applyInventory creates the Host objects and their SSH secrets. It runs during
// init rather than at cluster creation so the probe has already sorted the pool
// into reachable and not by the time a cluster asks for machines.
func applyInventory(ctx context.Context, kubeconfig string, cfg *config.Config) (int, error) {
	c, err := kube.NewClient(kubeconfig)
	if err != nil {
		return 0, err
	}

	objects, err := render.Render(cfg)
	if err != nil {
		return 0, err
	}

	// The namespace has to exist before anything lands in it.
	inventory := make([]client.Object, 0, len(objects.Secrets)+len(objects.Hosts)+1)
	inventory = append(inventory, objects.Namespace)
	for _, s := range objects.Secrets {
		inventory = append(inventory, s)
	}
	for _, h := range objects.Hosts {
		inventory = append(inventory, h)
	}

	if err := waitForCRD(ctx, c, cfg.Cluster.Namespace); err != nil {
		return 0, err
	}
	if err := kube.Apply(ctx, c, inventory); err != nil {
		return 0, err
	}
	return len(objects.Hosts), nil
}

// waitForCRD blocks until the API server serves the Host type. Applying the CRD
// and the first Host in one go otherwise races the API server's discovery cache.
func waitForCRD(ctx context.Context, c client.Client, namespace string) error {
	deadline := time.Now().Add(2 * time.Minute)

	for {
		hosts := &infrav1.HostList{}
		err := c.List(ctx, hosts, client.InNamespace(namespace))
		if err == nil {
			return nil
		}
		if !meta.IsNoMatchError(err) {
			return fmt.Errorf("wait for the Host CRD: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the Host CRD did not become available in time")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func step(out io.Writer, format string, args ...any) {
	fmt.Fprintf(out, "==> "+format+"\n", args...)
}
