// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/kube"
	"github.com/Ashon/kg/internal/provisioner"
	"github.com/Ashon/kg/internal/render"
)

func newClusterCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Create, inspect and delete the target cluster",
	}
	cmd.AddCommand(
		newClusterCreateCommand(opts),
		newClusterStatusCommand(opts),
		newClusterDeleteCommand(opts),
	)
	return cmd
}

func newClusterCreateCommand(opts *Options) *cobra.Command {
	var (
		dryRun      bool
		wait        bool
		waitTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Stamp the cluster out of the host pool",
		Long: `Applies the Cluster API objects that describe the target cluster.

From there the providers take over: the kubeadm bootstrap provider generates
cloud-init per machine, and the kgenesis provider claims a host for each machine
and runs that cloud-init on it over SSH.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			objects, err := render.Render(cfg)
			if err != nil {
				return err
			}

			if dryRun {
				manifest, err := kube.ToYAML(objects.All())
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(manifest)
				return err
			}

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			out := cmd.OutOrStdout()
			step(out, "Applying the cluster definition for %q", cfg.Cluster.Name)
			if err := kube.Apply(cmd.Context(), c, objects.All()); err != nil {
				return err
			}
			fmt.Fprintf(out, "  %d control plane + %d worker machine(s) requested\n",
				cfg.Cluster.ControlPlaneReplicas, totalWorkers(cfg))

			if !wait {
				fmt.Fprintf(out, "\nFollow progress with:\n\n  %s\n", invoke("cluster status --watch"))
				return nil
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
			defer cancel()

			step(out, "Waiting for the cluster to come up")
			if err := waitForCluster(ctx, c, cfg, out); err != nil {
				return err
			}

			// Nodes stay NotReady until a CNI is present, so installing it is part
			// of "the cluster is up" rather than a separate thing to remember.
			// Saying nothing when there is none to install would make "ready"
			// mean a cluster whose every node is NotReady, and leave the reason
			// for an operator to find.
			if !cfg.Cluster.CNI.HasCNI() {
				fmt.Fprintf(out, "\nNo CNI is configured, so every node will stay NotReady.\n")
				fmt.Fprintf(out, "kgenesis does not bundle one; the version is yours to pin.\n\n")
				fmt.Fprintf(out, "  %s  once cluster.cni.manifests names one\n", pad(invoke("cni install")))
			}
			if cfg.Cluster.CNI.HasCNI() {
				step(out, "Installing the CNI")
				workloadKubeconfig, err := opts.requireWorkloadKubeconfig(ctx, cfg)
				if err != nil {
					return err
				}
				workloadClient, err := kube.NewClient(workloadKubeconfig)
				if err != nil {
					return err
				}
				if err := installCNI(ctx, workloadClient, cfg, out); err != nil {
					return err
				}
			}

			fmt.Fprintf(out, "\nCluster %s is ready.\n\n", cfg.Cluster.Name)
			fmt.Fprintf(out, "  %swrite the cluster's kubeconfig\n", pad(invoke("kubeconfig")))
			fmt.Fprintf(out, "  %slet the cluster go and drop the genesis node\n", pad(invoke("eject")))
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the manifests instead of applying them")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait for the control plane and the workers to be ready")
	cmd.Flags().DurationVar(&waitTimeout, "timeout", 45*time.Minute, "How long to wait when --wait is set")
	return cmd
}

func totalWorkers(cfg *config.Config) int32 {
	var n int32
	for _, w := range cfg.Workers {
		n += w.Replicas
	}
	return n
}

// waitForCluster polls until the control plane is available and every machine is
// running, printing the same summary `cluster status` does so progress is legible.
func waitForCluster(ctx context.Context, c client.Client, cfg *config.Config, out io.Writer) error {
	const pollInterval = 15 * time.Second

	for {
		summary, err := clusterSummary(ctx, c, cfg)
		if err != nil {
			return err
		}

		fmt.Fprintf(out, "  control plane %d/%d ready, machines %d/%d running\n",
			summary.controlPlaneReady, summary.controlPlaneDesired,
			summary.machinesRunning, summary.machinesDesired)

		if summary.ready() {
			return nil
		}

		// Waiting out the timeout on a bootstrap that has already failed helps
		// nobody: the run does not retry, and the reason is on the condition.
		if len(summary.failures) > 0 {
			return newBootstrapFailedError(summary.failures)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster %s was not ready in time; run `%s` to see where it stopped",
				cfg.Cluster.Name, invoke("cluster status"))
		case <-time.After(pollInterval):
		}
	}
}

// summary is the cluster state `status` prints and `create --wait` polls.
type summary struct {
	cluster             *clusterv1.Cluster
	controlPlaneReady   int32
	controlPlaneDesired int32
	machines            []clusterv1.Machine
	machinesRunning     int32
	machinesDesired     int32
	hosts               []infrav1.Host

	// failures are machines whose bootstrap will not recover on its own.
	failures []machineFailure
}

// machineFailure is a bootstrap that ran on a host and exited non-zero.
type machineFailure struct {
	machine string
	host    string
	message string
}

func (s *summary) ready() bool {
	return s.controlPlaneDesired > 0 &&
		s.controlPlaneReady == s.controlPlaneDesired &&
		s.machinesDesired > 0 &&
		s.machinesRunning == s.machinesDesired
}

func clusterSummary(ctx context.Context, c client.Client, cfg *config.Config) (*summary, error) {
	s := &summary{}

	cluster := &clusterv1.Cluster{}
	key := types.NamespacedName{Namespace: cfg.Cluster.Namespace, Name: cfg.Cluster.Name}
	if err := c.Get(ctx, key, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("cluster %s does not exist yet; run `%s`", key, invoke("cluster create"))
		}
		return nil, err
	}
	s.cluster = cluster

	machines := &clusterv1.MachineList{}
	if err := c.List(ctx, machines,
		client.InNamespace(cfg.Cluster.Namespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: cfg.Cluster.Name},
	); err != nil {
		return nil, err
	}
	s.machines = machines.Items

	for _, m := range machines.Items {
		if m.Status.Phase == string(clusterv1.MachinePhaseRunning) {
			s.machinesRunning++
		}
		if _, isControlPlane := m.Labels[clusterv1.MachineControlPlaneLabel]; isControlPlane {
			if m.Status.Phase == string(clusterv1.MachinePhaseRunning) {
				s.controlPlaneReady++
			}
		}
	}

	// What was asked for, not what exists yet. The providers create their
	// machines one at a time, so counting the ones that exist reports a target
	// that grows to meet the count - 5 of 5 running while a sixth has not been
	// created - and a wait that ends before the cluster is the size it was asked
	// to be.
	s.controlPlaneDesired = cfg.Cluster.ControlPlaneReplicas
	s.machinesDesired = cfg.Cluster.ControlPlaneReplicas + totalWorkers(cfg)

	hosts := &infrav1.HostList{}
	if err := c.List(ctx, hosts, client.InNamespace(cfg.Cluster.Namespace)); err != nil {
		return nil, err
	}
	s.hosts = hosts.Items

	hostMachines := &infrav1.HostMachineList{}
	if err := c.List(ctx, hostMachines, client.InNamespace(cfg.Cluster.Namespace)); err != nil {
		return nil, err
	}
	for i := range hostMachines.Items {
		if failure, ok := bootstrapFailure(&hostMachines.Items[i]); ok {
			s.failures = append(s.failures, failure)
		}
	}

	return s, nil
}

// bootstrapFailure reads the Provisioned condition for a run that has already
// failed. kgenesis does not re-run kubeadm over a half-configured host, so this
// state does not clear by itself and there is nothing to be gained by waiting.
func bootstrapFailure(hostMachine *infrav1.HostMachine) (machineFailure, bool) {
	condition := meta.FindStatusCondition(hostMachine.Status.Conditions,
		infrav1.HostMachineProvisionedCondition)
	if condition == nil ||
		condition.Status != metav1.ConditionFalse ||
		condition.Reason != infrav1.ReasonBootstrapFailed {
		return machineFailure{}, false
	}

	host := "-"
	if hostMachine.Status.HostRef != nil {
		host = hostMachine.Status.HostRef.Name
	}
	return machineFailure{
		machine: hostMachine.Name,
		host:    host,
		message: condition.Message,
	}, true
}

func newClusterStatusCommand(opts *Options) *cobra.Command {
	var watch bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show where the cluster rollout has got to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			if !opts.bootstrapClusterExists() {
				out := cmd.OutOrStdout()
				fmt.Fprintf(out, "Cluster %s has not been built yet.\n\n", cfg.Cluster.Name)
				fmt.Fprintf(out, "  %sbring up the bootstrap cluster and the providers\n", pad(invoke("init")))
				fmt.Fprintf(out, "  %sstamp out %s\n", pad(invoke("cluster create")), cfg.Cluster.Name)
				return nil
			}

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			for {
				s, err := clusterSummary(cmd.Context(), c, cfg)
				if err != nil {
					return err
				}
				printStatus(cmd.OutOrStdout(), cfg, s)

				if !watch || s.ready() {
					return nil
				}
				select {
				case <-cmd.Context().Done():
					return cmd.Context().Err()
				case <-time.After(15 * time.Second):
				}
			}
		},
	}

	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "Keep printing until the cluster is ready")
	return cmd
}

func printStatus(out io.Writer, cfg *config.Config, s *summary) {
	fmt.Fprintf(out, "Cluster %s/%s\n", cfg.Cluster.Namespace, cfg.Cluster.Name)
	fmt.Fprintf(out, "  phase          %s\n", s.cluster.Status.Phase)
	fmt.Fprintf(out, "  endpoint       %s:%d\n",
		s.cluster.Spec.ControlPlaneEndpoint.Host, s.cluster.Spec.ControlPlaneEndpoint.Port)
	fmt.Fprintf(out, "  control plane  %d/%d ready\n", s.controlPlaneReady, s.controlPlaneDesired)
	fmt.Fprintf(out, "  machines       %d/%d running\n\n", s.machinesRunning, s.machinesDesired)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MACHINE\tROLE\tPHASE\tHOST\tADDRESS")
	for _, m := range s.machines {
		role := "worker"
		if _, ok := m.Labels[clusterv1.MachineControlPlaneLabel]; ok {
			role = "control-plane"
		}
		host, address := hostForMachine(s.hosts, m.Name)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.Name, role, m.Status.Phase, host, address)
	}
	_ = w.Flush()
	fmt.Fprintln(out)

	for _, f := range s.failures {
		fmt.Fprintf(out, "%s on host %s failed to bootstrap:\n", f.machine, f.host)
		for _, line := range strings.Split(strings.TrimSpace(f.message), "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
		fmt.Fprintln(out)
	}
}

// hostForMachine finds which host a machine landed on, by the claim the kgenesis
// provider records on the Host.
func hostForMachine(hosts []infrav1.Host, machineName string) (string, string) {
	for _, h := range hosts {
		if h.Status.ClaimRef == nil {
			continue
		}
		// Cluster API gives the infrastructure machine the same name as its
		// Machine, so the claim reference identifies the pairing directly.
		if h.Status.ClaimRef.Name == machineName {
			return h.Name, h.Spec.Address
		}
	}
	return "-", "-"
}

func newClusterDeleteCommand(opts *Options) *cobra.Command {
	var (
		yes         bool
		waitTimeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete the cluster and reset its hosts",
		Long: `Deletes the Cluster object. Cluster API tears the machines down, and the
kgenesis provider runs kubeadm reset on each host before returning it to the pool.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(),
					"This deletes cluster %s and runs kubeadm reset on %d host(s).\nRe-run with --yes to proceed.\n",
					cfg.Cluster.Name, len(cfg.Hosts))
				return nil
			}

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return err
			}

			cluster := &clusterv1.Cluster{}
			key := types.NamespacedName{Namespace: cfg.Cluster.Namespace, Name: cfg.Cluster.Name}
			if err := c.Get(cmd.Context(), key, cluster); err != nil {
				if apierrors.IsNotFound(err) {
					fmt.Fprintf(cmd.OutOrStdout(), "Cluster %s is already gone.\n", key)
					return nil
				}
				return err
			}

			out := cmd.OutOrStdout()
			step(out, "Deleting cluster %s", key)
			if err := c.Delete(cmd.Context(), cluster); err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), waitTimeout)
			defer cancel()
			return waitForClusterGone(ctx, c, key, out)
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false, "Proceed without the confirmation prompt")
	cmd.Flags().DurationVar(&waitTimeout, "timeout", 20*time.Minute, "How long to wait for teardown")
	return cmd
}

func waitForClusterGone(ctx context.Context, c client.Client, key types.NamespacedName, out io.Writer) error {
	for {
		cluster := &clusterv1.Cluster{}
		err := c.Get(ctx, key, cluster)
		if apierrors.IsNotFound(err) {
			return reportReleasedHosts(ctx, c, key, out)
		}
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  waiting, phase %s\n", cluster.Status.Phase)

		select {
		case <-ctx.Done():
			return fmt.Errorf("cluster %s was still being deleted when the timeout expired", key)
		case <-time.After(10 * time.Second):
		}
	}
}

// newBootstrapFailedError reports every failed machine at once, with the tail of
// the host's bootstrap log that the provider copied onto the condition.
func newBootstrapFailedError(failures []machineFailure) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d machine(s) failed to bootstrap and will not retry:\n", len(failures))

	for _, f := range failures {
		fmt.Fprintf(&b, "\n  %s on host %s\n", f.machine, f.host)
		for _, line := range strings.Split(strings.TrimSpace(f.message), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}

	fmt.Fprintf(&b, "\nThe full log is at %s on each host. "+
		"Delete the cluster to reset the hosts and start again.\n", provisioner.LogPath)
	return errors.New(b.String())
}

// reportReleasedHosts says what the hosts came back as.
//
// A host is returned to the pool whether or not kgenesis could reach it to run
// kubeadm reset, because a Machine stuck forever on hardware that is powered off
// helps nobody. Reporting both the same way is what turns that trade-off into a
// surprise: the next cluster claims a host still carrying another one's
// certificates and etcd data, and nothing said so.
func reportReleasedHosts(ctx context.Context, c client.Client, key types.NamespacedName, out io.Writer) error {
	fmt.Fprintf(out, "Cluster %s is gone.\n", key)

	hosts := &infrav1.HostList{}
	if err := c.List(ctx, hosts, client.InNamespace(key.Namespace)); err != nil {
		return fmt.Errorf("read the host pool in %s: %w", key.Namespace, err)
	}

	var unverified []*infrav1.Host
	for i := range hosts.Items {
		host := &hosts.Items[i]
		if condition := meta.FindStatusCondition(host.Status.Conditions, infrav1.HostResetCondition); condition != nil &&
			condition.Status != metav1.ConditionTrue {
			unverified = append(unverified, host)
		}
	}

	fmt.Fprintf(out, "  %d host(s) back in the pool\n", len(hosts.Items)-len(unverified))
	if len(unverified) == 0 {
		return nil
	}

	fmt.Fprintf(out, "\n%d host(s) were released without being reset and still carry this cluster:\n",
		len(unverified))
	for _, host := range unverified {
		condition := meta.FindStatusCondition(host.Status.Conditions, infrav1.HostResetCondition)
		fmt.Fprintf(out, "  %s (%s): %s\n", host.Name, host.Spec.Address, condition.Message)
	}
	fmt.Fprintf(out, "\nClean them by hand before another cluster claims them.\n")
	return nil
}
