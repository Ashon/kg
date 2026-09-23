// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/bootstrap"
	"github.com/Ashon/kgenesis/internal/capi"
	"github.com/Ashon/kgenesis/internal/config"
	"github.com/Ashon/kgenesis/internal/kube"
)

func newPivotCommand(opts *Options) *cobra.Command {
	var (
		dryRun        bool
		keepBootstrap bool
		selfManage    bool
		providerImage string
		waitTimeout   time.Duration
	)

	cmd := &cobra.Command{
		Use:     "eject",
		Aliases: []string{"pivot"},
		Short:   "Let a cluster go and drop the genesis node",
		Long: `Releases one cluster from the genesis node.

The cluster keeps running untouched. It is an ordinary kubeadm cluster and needs
nothing from kgenesis to serve. What it gives up is Cluster API: no node is
added, replaced or upgraded through kgenesis afterwards. In exchange the SSH keys
never leave the genesis node, and nothing is left running inside the cluster that
could act on the hosts underneath it.

A genesis node builds several clusters and releases them one at a time. Only the
cluster named by the configuration is released; the others stay where they are.
The genesis node is deleted once nothing is left for it to manage, and kept
otherwise.

--self-manage moves the Cluster API objects into the cluster instead, and is
refused unless the cluster could actually survive managing itself: three control
plane replicas for etcd quorum, and a free host of each role for a rollout to
move onto. It is also not finished - the provider does not rebuild its state
after the move, so it re-runs kubeadm against hosts that have already joined. Do
not use it on a cluster you care about.`,
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

			if selfManage {
				step(out, "Checking that %s is ready to take over", cfg.Cluster.Name)
			} else {
				step(out, "Checking that %s is healthy before letting it go", cfg.Cluster.Name)
			}
			summary, err := clusterSummary(ctx, bootstrapClient, cfg)
			if err != nil {
				return err
			}
			if !summary.ready() {
				return fmt.Errorf(
					"cluster %s is not ready yet (control plane %d/%d, machines %d/%d); "+
						"run `"+invoke("cluster status --watch")+"`",
					cfg.Cluster.Name, summary.controlPlaneReady, summary.controlPlaneDesired,
					summary.machinesRunning, summary.machinesDesired)
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

			if selfManage {
				if err := checkSelfManageable(ctx, bootstrapClient, cfg); err != nil {
					return err
				}
				if err := moveToCluster(ctx, moveRequest{
					cfg:                 cfg,
					bootstrapClient:     bootstrapClient,
					bootstrapKubeconfig: bootstrapKubeconfig,
					workloadKubeconfig:  workloadKubeconfig,
					providerImage:       providerImage,
					waitTimeout:         waitTimeout,
					dryRun:              dryRun,
					out:                 out,
				}); err != nil {
					return err
				}
				if dryRun {
					fmt.Fprintf(out, "\nDry run only; nothing was moved and the bootstrap cluster is untouched.\n")
					return nil
				}
			} else {
				if dryRun {
					fmt.Fprintf(out, "\nDry run only; %s was left where it is.\n", cfg.Cluster.Name)
					return nil
				}
				step(out, "Releasing %s from the genesis node", cfg.Cluster.Name)
				if err := pauseCluster(ctx, bootstrapClient, cfg); err != nil {
					return err
				}
			}

			// The genesis node stamps out several clusters and ejects them one at
			// a time. Tearing it down while it still manages others would strand
			// them, so what is left decides.
			remaining, err := clustersRemaining(ctx, bootstrapClient, cfg.Cluster.Namespace)
			if err != nil {
				return err
			}

			switch {
			case keepBootstrap:
				fmt.Fprintf(out, "\nEjected. The genesis node was kept as requested.\n")
			case len(remaining) > 0:
				fmt.Fprintf(out, "\nEjected. The genesis node still manages %d cluster(s): %s\n",
					len(remaining), strings.Join(remaining, ", "))
			default:
				step(out, "Deleting the bootstrap cluster")
				var kindLog io.Writer = io.Discard
				if opts.Verbose {
					kindLog = cmd.ErrOrStderr()
				}
				if err := bootstrap.New(cfg.Bootstrap.ClusterName, kindLog).Delete(bootstrapKubeconfig); err != nil {
					return err
				}
				// kind leaves the file behind with its context removed. Every
				// later command reads "the genesis node is up" from its presence,
				// and would report a client-go parse failure instead of saying
				// there is no genesis node any more.
				if err := os.Remove(bootstrapKubeconfig); err != nil && !os.IsNotExist(err) {
					return err
				}
				fmt.Fprintf(out, "\nNothing else was left to manage, so the genesis node is gone.\n")
			}

			if selfManage {
				fmt.Fprintf(out, "\n%s now manages itself.\n\n", cfg.Cluster.Name)
			} else {
				fmt.Fprintf(out, "\n%s is on its own now; nothing manages it.\n\n", cfg.Cluster.Name)
			}
			fmt.Fprintf(out, "  export KUBECONFIG=%s\n", workloadKubeconfig)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report what would happen without doing it")
	cmd.Flags().BoolVar(&selfManage, "self-manage", false,
		"Move the Cluster API objects into the cluster instead of releasing it (unfinished; re-runs kubeadm on joined hosts)")
	cmd.Flags().BoolVar(&keepBootstrap, "keep-bootstrap", false,
		"Leave the genesis node running afterwards")
	cmd.Flags().StringVar(&providerImage, "provider-image", "",
		"With --self-manage, override the kgenesis controller image installed into the cluster")
	cmd.Flags().DurationVar(&waitTimeout, "timeout", 20*time.Minute, "Time budget for the whole operation")
	return cmd
}

// minSelfManagedControlPlanes is the point at which etcd keeps quorum while one
// member is being replaced.
const minSelfManagedControlPlanes = 3

// checkSelfManageable refuses to hand a cluster its own management unless it can
// survive managing itself.
//
// Cluster API keeps a cluster healthy by replacing machines, and a self-managed
// cluster has to do that to the machines its own controllers are running on.
// KubeadmControlPlane adds a machine before it removes one, and a MachineDeploy-
// ment does the same, so each needs a host free to add. Below three control
// plane replicas etcd loses quorum the moment one goes.
//
// A cluster that cannot meet this can still be released, which asks nothing of
// it. What it cannot do is carry the responsibility and then be unable to act on
// it - the state where the controllers see what is wrong and can do nothing.
func checkSelfManageable(ctx context.Context, c client.Client, cfg *config.Config) error {
	var problems []string

	if cfg.Cluster.ControlPlaneReplicas < minSelfManagedControlPlanes {
		problems = append(problems, fmt.Sprintf(
			"the control plane has %d replica(s); etcd needs %d to keep quorum while one is replaced",
			cfg.Cluster.ControlPlaneReplicas, minSelfManagedControlPlanes))
	}

	hosts := &infrav1.HostList{}
	if err := c.List(ctx, hosts, client.InNamespace(cfg.Cluster.Namespace)); err != nil {
		return fmt.Errorf("read the host pool in %s: %w", cfg.Cluster.Namespace, err)
	}

	spare := map[string]int{}
	for i := range hosts.Items {
		host := &hosts.Items[i]
		if host.Status.ClaimRef != nil || host.Status.Phase != infrav1.HostPhaseAvailable {
			continue
		}
		spare[host.Labels[infrav1.RoleLabel]]++
	}

	if spare[infrav1.RoleControlPlane] == 0 {
		problems = append(problems,
			"no control plane host is free, so the control plane can never be rolled")
	}
	if len(cfg.Workers) > 0 && spare[infrav1.RoleWorker] == 0 {
		problems = append(problems,
			"no worker host is free, so a worker pool can never be rolled")
	}

	if len(problems) == 0 {
		return nil
	}

	return fmt.Errorf("%s cannot manage itself:\n  - %s\n\n"+
		"Add hosts to the pool and retry, or run `%s` to release it instead",
		cfg.Cluster.Name, strings.Join(problems, "\n  - "), invoke("eject"))
}

// pauseCluster stops the genesis node acting on a cluster it has released.
//
// Deleting the Cluster object would be the obvious way and is the wrong one: it
// cascades into Machine deletion, and kgenesis answers that by running kubeadm
// reset on hosts that are serving. Pausing stops every controller for this
// cluster while leaving the record - and the host claims that go with it - in
// place, so a second cluster on the same genesis node cannot take the hosts the
// released one is running on.
func pauseCluster(ctx context.Context, c client.Client, cfg *config.Config) error {
	cluster := &clusterv1.Cluster{}
	key := types.NamespacedName{Namespace: cfg.Cluster.Namespace, Name: cfg.Cluster.Name}
	if err := c.Get(ctx, key, cluster); err != nil {
		return fmt.Errorf("read cluster %s: %w", key, err)
	}

	if cluster.Spec.Paused != nil && *cluster.Spec.Paused {
		return nil
	}

	cluster.Spec.Paused = ptr.To(true)
	if err := c.Update(ctx, cluster); err != nil {
		return fmt.Errorf("release cluster %s: %w", key, err)
	}
	return nil
}

// moveRequest is what a self-managing handover needs.
type moveRequest struct {
	cfg                 *config.Config
	bootstrapClient     client.Client
	bootstrapKubeconfig string
	workloadKubeconfig  string
	providerImage       string
	waitTimeout         time.Duration
	dryRun              bool
	out                 io.Writer
}

// moveToCluster carries the controllers and the Cluster API objects into the
// cluster, so it manages itself from then on.
func moveToCluster(ctx context.Context, req moveRequest) error {
	image, err := providerImageFor(req.providerImage)
	if err != nil {
		return err
	}
	if bootstrap.ImageAvailableLocally(ctx, image) {
		hosts, err := claimedHosts(ctx, req.bootstrapClient, req.cfg)
		if err != nil {
			return err
		}
		step(req.out, "Carrying %s to the cluster's own hosts", image)
		if err := seedProviderImage(ctx, hosts, image, req.out); err != nil {
			return err
		}
	}

	step(req.out, "Installing the providers into %s", req.cfg.Cluster.Name)
	if err := installTargetProviders(ctx, req.workloadKubeconfig,
		req.cfg.Bootstrap.CAPIVersion, req.providerImage, req.waitTimeout); err != nil {
		return err
	}

	// Counted before the move so the target can be checked against it.
	held, err := countInventory(ctx, req.bootstrapClient, req.cfg.Cluster.Namespace)
	if err != nil {
		return err
	}

	step(req.out, "Moving the Cluster API objects")
	if err := capi.Move(ctx, capi.MoveOptions{
		FromKubeconfig: req.bootstrapKubeconfig,
		ToKubeconfig:   req.workloadKubeconfig,
		Namespace:      req.cfg.Cluster.Namespace,
		DryRun:         req.dryRun,
	}); err != nil {
		return err
	}
	if req.dryRun {
		return nil
	}

	return verifyMoved(ctx, req.workloadKubeconfig, req.cfg.Cluster.Namespace, req.cfg.Cluster.Name, held)
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

// inventory is what the genesis node holds for one cluster.
type inventory struct {
	hosts        int
	hostMachines int
}

// countInventory counts the kgenesis objects in a namespace.
//
// clusterctl moves what it discovers and says nothing about the rest, so a move
// can report success having left every Host and HostMachine behind. Counting
// both sides is the only way to tell.
func countInventory(ctx context.Context, c client.Client, namespace string) (inventory, error) {
	hosts := &infrav1.HostList{}
	if err := c.List(ctx, hosts, client.InNamespace(namespace)); err != nil {
		return inventory{}, fmt.Errorf("count the hosts in %s: %w", namespace, err)
	}

	machines := &infrav1.HostMachineList{}
	if err := c.List(ctx, machines, client.InNamespace(namespace)); err != nil {
		return inventory{}, fmt.Errorf("count the host machines in %s: %w", namespace, err)
	}

	return inventory{hosts: len(hosts.Items), hostMachines: len(machines.Items)}, nil
}

// verifyMoved confirms the cluster and its inventory landed before the bootstrap
// cluster is destroyed: deleting it while the move was incomplete would strand
// the cluster with no management plane at all.
func verifyMoved(ctx context.Context, kubeconfig, namespace, name string, held inventory) error {
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

	landed, err := countInventory(ctx, c, namespace)
	if err != nil {
		return err
	}
	if landed != held {
		return fmt.Errorf("the move left part of %s behind: the genesis node had %d host(s) and "+
			"%d host machine(s), %s has %d and %d. The bootstrap cluster was left alone",
			name, held.hosts, held.hostMachines, name, landed.hosts, landed.hostMachines)
	}
	return nil
}

// clustersRemaining lists the clusters the genesis node still manages, ignoring
// the namespace that was just released. A namespace per cluster is what makes
// releasing one of several possible at all: clusterctl moves a namespace rather
// than a cluster, and a namespace is also the boundary a released cluster's host
// claims sit behind.
func clustersRemaining(ctx context.Context, c client.Client, ejected string) ([]string, error) {
	clusters := &clusterv1.ClusterList{}
	if err := c.List(ctx, clusters); err != nil {
		return nil, fmt.Errorf("list the clusters still managed here: %w", err)
	}

	var names []string
	for _, cluster := range clusters.Items {
		if cluster.Namespace == ejected {
			continue
		}
		// A cluster released earlier is still recorded here, but nothing acts on
		// it any more, so it is not a reason to keep the genesis node alive.
		if cluster.Spec.Paused != nil && *cluster.Spec.Paused {
			continue
		}
		names = append(names, cluster.Namespace+"/"+cluster.Name)
	}
	sort.Strings(names)
	return names, nil
}
