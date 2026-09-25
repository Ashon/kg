// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/inventory"
	"github.com/Ashon/kg/internal/kube"
	"github.com/Ashon/kg/internal/provisioner"
	"github.com/Ashon/kg/internal/ssh"
)

func newInventoryCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Inspect the pool of pre-provisioned hosts",
	}
	cmd.AddCommand(newInventoryCheckCommand(opts), newInventoryListCommand(opts),
		newInventoryTrustCommand(opts))
	return cmd
}

// checkResult is one host's preflight outcome.
type checkResult struct {
	host    config.HostConfig
	info    *provisioner.SystemInfo
	err     error
	warning string
}

func newInventoryCheckCommand(opts *Options) *cobra.Command {
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Connect to every host in the configuration and report what it finds",
		Long: `Connects to each host over SSH and reports whether it is usable.

This needs nothing but the configuration file, so it is worth running before
` + "`" + invoke("init") + "`" + `: a host that fails here would otherwise fail much later,
in the middle of a cluster rollout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()

			results := checkHosts(ctx, cfg)
			return reportCheck(cmd.OutOrStdout(), results)
		},
	}

	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "Overall time budget for the preflight")
	return cmd
}

// checkHosts probes every host concurrently: a pool of any size should take
// about as long as its slowest host, not the sum of them.
func checkHosts(ctx context.Context, cfg *config.Config) []checkResult {
	results := make([]checkResult, len(cfg.Hosts))

	var wg sync.WaitGroup
	for i, host := range cfg.Hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = checkHost(ctx, host, cfg.Cluster.KubernetesVersion)
		}()
	}
	wg.Wait()

	return results
}

func checkHost(ctx context.Context, host config.HostConfig, wantVersion string) checkResult {
	result := checkResult{host: host}

	sshCfg, err := sshConfigFor(host)
	if err != nil {
		result.err = err
		return result
	}

	conn, err := ssh.Dial(ctx, sshCfg)
	if err != nil {
		result.err = err
		return result
	}
	defer func() { _ = conn.Close() }()

	info, err := provisioner.Probe(ctx, conn)
	if err != nil {
		result.err = err
		return result
	}
	result.info = info

	if res, err := conn.Run(ctx, "id -u"); err != nil {
		result.err = fmt.Errorf("check privileges: %w", err)
		return result
	} else if trimmed := strings.TrimSpace(res.Stdout); trimmed != "0" {
		result.err = fmt.Errorf("connects as uid %s; kgenesis needs root", trimmed)
		return result
	}

	// A host that has already been through kubeadm will fail preflight on a join,
	// so it is worth calling out before anything is applied.
	//
	// The test is for what kubeadm writes, not for what its packages create.
	// /etc/kubernetes and an empty /var/lib/etcd are present on every host that
	// merely has kubeadm installed, which is all of them, and a warning that
	// always fires is one nobody reads.
	const dirtyCheck = `if ls /etc/kubernetes/*.conf >/dev/null 2>&1; then echo conf; ` +
		`elif [ -n "$(ls -A /var/lib/etcd 2>/dev/null)" ]; then echo etcd; fi`

	if res, err := conn.Run(ctx, dirtyCheck); err == nil {
		switch strings.TrimSpace(res.Stdout) {
		case "conf":
			result.warning = "kubeadm has already run here; /etc/kubernetes holds its kubeconfigs"
		case "etcd":
			result.warning = "/var/lib/etcd is not empty, so this host held an etcd member"
		}
	}
	if info.ContainerRuntime == "" {
		result.warning = joinWarnings(result.warning, "no container runtime found; install containerd before provisioning")
	}
	if mismatch := kubeadmMismatch(wantVersion, info.KubeadmVersion); mismatch != "" {
		result.warning = joinWarnings(result.warning, mismatch)
	}

	return result
}

func reportCheck(out io.Writer, results []checkResult) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tADDRESS\tROLE\tSTATUS\tOS\tARCH\tCPU\tMEM\tRUNTIME\tKUBEADM")

	var failed, warned int
	for _, r := range results {
		switch {
		case r.err != nil:
			failed++
			fmt.Fprintf(w, "%s\t%s\t%s\tFAIL\t-\t-\t-\t-\t-\t-\n", r.host.Name, r.host.Address, r.host.Role)
		case r.warning != "":
			warned++
			fmt.Fprintf(w, "%s\t%s\t%s\tWARN\t%s\t%s\t%d\t%d MiB\t%s\t%s\n",
				r.host.Name, r.host.Address, r.host.Role,
				r.info.OSImage, r.info.Architecture, r.info.CPUCores, r.info.MemoryMB,
				runtimeOrDash(r.info), dashIfEmpty(r.info.KubeadmVersion))
		default:
			fmt.Fprintf(w, "%s\t%s\t%s\tOK\t%s\t%s\t%d\t%d MiB\t%s\t%s\n",
				r.host.Name, r.host.Address, r.host.Role,
				r.info.OSImage, r.info.Architecture, r.info.CPUCores, r.info.MemoryMB,
				runtimeOrDash(r.info), dashIfEmpty(r.info.KubeadmVersion))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}

	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(out, "\n%s (%s) failed:\n  %v\n", r.host.Name, r.host.Address, r.err)
		} else if r.warning != "" {
			fmt.Fprintf(out, "\n%s (%s): %s\n", r.host.Name, r.host.Address, r.warning)
		}
	}

	if failed > 0 {
		// A denial by the operating system looks like a routing failure on every
		// host at once. Said once, where the results are gathered, it reads as
		// what it is: a condition of this machine, not of the fleet.
		for _, r := range results {
			if hint := ssh.DialHint(r.err); hint != "" {
				fmt.Fprintf(out, "\n%s\n", hint)
				break
			}
		}
		return fmt.Errorf("%d of %d host(s) are not usable", failed, len(results))
	}
	fmt.Fprintf(out, "\n%d host(s) reachable, %d with warnings.\n", len(results), warned)
	return nil
}

func runtimeOrDash(info *provisioner.SystemInfo) string {
	if info.ContainerRuntime == "" {
		return "-"
	}
	return info.ContainerRuntime
}

func newInventoryListCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the host pool as the management cluster sees it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			if !opts.bootstrapClusterExists() {
				fmt.Fprintf(cmd.OutOrStdout(),
					"The pool is not loaded yet; %s puts it in the bootstrap cluster.\n\n"+
						"To check the hosts themselves right now, without one:\n\n  %s\n",
					invoke("init"), invoke("inventory check"))
				return nil
			}

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			hosts := &infrav1.HostList{}
			if err := c.List(cmd.Context(), hosts, client.InNamespace(cfg.Cluster.Namespace)); err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}

			inventory.SortHosts(hosts.Items)

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "HOST\tADDRESS\tROLE\tPHASE\tCLAIMED BY")
			for _, h := range hosts.Items {
				claimedBy := "-"
				if h.Status.ClaimRef != nil {
					claimedBy = h.Status.ClaimRef.Name
				}
				phase := string(h.Status.Phase)
				if phase == "" {
					phase = string(infrav1.HostPhasePending)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					h.Name, h.Spec.Address, h.Labels[infrav1.RoleLabel], phase, claimedBy)
			}
			return w.Flush()
		},
	}
}

func joinWarnings(existing, additional string) string {
	if existing == "" {
		return additional
	}
	return existing + "; " + additional
}

// sshConfigFor turns one host's entry in the genesis config into a dial config.
// The private key is read here rather than at load time so a key that is missing
// or unreadable is reported against the host that needs it.
func sshConfigFor(host config.HostConfig) (ssh.Config, error) {
	cfg := ssh.Config{
		Address:        host.Address,
		Port:           host.Port,
		User:           host.User,
		Policy:         ssh.HostKeyPolicy(host.HostKeyPolicy),
		Password:       host.Password,
		Passphrase:     host.Passphrase,
		KnownPublicKey: host.PublicKey,
	}
	if host.PrivateKeyPath != "" {
		key, err := os.ReadFile(host.PrivateKeyPath)
		if err != nil {
			return ssh.Config{}, fmt.Errorf("read private key: %w", err)
		}
		cfg.PrivateKey = key
	}
	return cfg, nil
}

// newInventoryTrustCommand lets an operator accept a host key that changed.
func newInventoryTrustCommand(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "trust HOST...",
		Short: "Accept the host key a machine presents now",
		Long: `Forgets the SSH host key pinned for a host, so the next connection pins
whatever the machine presents.

Under the TOFU policy kgenesis pins the key it first sees and refuses the host
for good if it ever changes, because that is what an impersonated machine looks
like. A machine that was legitimately reinstalled looks exactly the same, and
this is how an operator says which of the two it was.

Name only the hosts you mean. A host whose key changed for a reason you cannot
account for is a host to go and look at, not one to trust.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, names []string) error {
			cfg, err := opts.Load()
			if err != nil {
				return err
			}

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			out := cmd.OutOrStdout()
			for _, name := range names {
				host := &infrav1.Host{}
				key := types.NamespacedName{Namespace: cfg.Cluster.Namespace, Name: name}
				if err := c.Get(cmd.Context(), key, host); err != nil {
					return fmt.Errorf("read host %s: %w", key, err)
				}

				if host.Spec.HostKeyPolicy == infrav1.HostKeyPolicyStrict {
					return fmt.Errorf("%s uses the Strict policy, where the key to accept is "+
						"spec.publicKey and changing it is a deliberate edit, not a command", name)
				}

				if host.Status.ObservedPublicKey == "" {
					fmt.Fprintf(out, "%s has no pinned key; the next connection will pin one.\n", name)
					continue
				}

				previous := abbreviateKey(host.Status.ObservedPublicKey)
				// The Host controller probes on its own schedule, so the version
				// read a moment ago may already be stale.
				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					if err := c.Get(cmd.Context(), key, host); err != nil {
						return err
					}
					host.Status.ObservedPublicKey = ""
					return c.Status().Update(cmd.Context(), host)
				})
				if err != nil {
					return fmt.Errorf("forget the pinned key for %s: %w", name, err)
				}
				fmt.Fprintf(out, "%s: forgot %s; the next connection pins what the machine presents.\n",
					name, previous)
			}
			return nil
		},
	}
}

// abbreviateKey shortens an authorized_keys line enough to recognise without
// filling the terminal.
func abbreviateKey(authorizedKey string) string {
	fields := strings.Fields(authorizedKey)
	if len(fields) < 2 || len(fields[1]) < 20 {
		return authorizedKey
	}
	return fields[0] + " " + fields[1][:10] + "..." + fields[1][len(fields[1])-6:]
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// kubeadmMismatch reports a host whose kubeadm is not the release the cluster
// is configured for.
//
// kgenesis does not install kubeadm, so the host decides which Kubernetes it can
// build. A minor apart from the configuration is not a warning to be read later:
// kubeadm refuses, several minutes into a rollout, and says nothing about where
// the number it disagreed with came from.
func kubeadmMismatch(want, got string) string {
	if want == "" || got == "" {
		return ""
	}
	if minorOf(want) == minorOf(got) {
		return ""
	}
	return fmt.Sprintf("kubeadm is %s, and the configuration asks for %s", got, want)
}

// minorOf reduces v1.33.13 to v1.33, which is as far as kubeadm's own skew rules
// care.
func minorOf(version string) string {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
	if len(parts) < 2 {
		return version
	}
	return "v" + parts[0] + "." + parts[1]
}
