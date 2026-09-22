package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/config"
	"github.com/Ashon/kgenesis/internal/kube"
	"github.com/Ashon/kgenesis/internal/provisioner"
	"github.com/Ashon/kgenesis/internal/ssh"
)

func newInventoryCommand(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Inspect the pool of pre-provisioned hosts",
	}
	cmd.AddCommand(newInventoryCheckCommand(opts), newInventoryListCommand(opts))
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
			results[i] = checkHost(ctx, host)
		}()
	}
	wg.Wait()

	return results
}

func checkHost(ctx context.Context, host config.HostConfig) checkResult {
	result := checkResult{host: host}

	sshCfg := ssh.Config{
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
			result.err = fmt.Errorf("read private key: %w", err)
			return result
		}
		sshCfg.PrivateKey = key
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

	// A host that already carries kubeadm state will fail preflight during a join,
	// so it is worth calling out before anything is applied.
	if res, err := conn.Run(ctx, "if [ -d /etc/kubernetes ] || [ -d /var/lib/etcd ]; then echo dirty; fi"); err == nil {
		if strings.TrimSpace(res.Stdout) == "dirty" {
			result.warning = "already carries kubeadm state (/etc/kubernetes or /var/lib/etcd)"
		}
	}
	if info.ContainerRuntime == "" {
		result.warning = joinWarnings(result.warning, "no container runtime found; install containerd before provisioning")
	}

	return result
}

func reportCheck(out io.Writer, results []checkResult) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "HOST\tADDRESS\tROLE\tSTATUS\tOS\tARCH\tCPU\tMEM\tRUNTIME")

	var failed, warned int
	for _, r := range results {
		switch {
		case r.err != nil:
			failed++
			fmt.Fprintf(w, "%s\t%s\t%s\tFAIL\t-\t-\t-\t-\t-\n", r.host.Name, r.host.Address, r.host.Role)
		case r.warning != "":
			warned++
			fmt.Fprintf(w, "%s\t%s\t%s\tWARN\t%s\t%s\t%d\t%d MiB\t%s\n",
				r.host.Name, r.host.Address, r.host.Role,
				r.info.OSImage, r.info.Architecture, r.info.CPUCores, r.info.MemoryMB, runtimeOrDash(r.info))
		default:
			fmt.Fprintf(w, "%s\t%s\t%s\tOK\t%s\t%s\t%d\t%d MiB\t%s\n",
				r.host.Name, r.host.Address, r.host.Role,
				r.info.OSImage, r.info.Architecture, r.info.CPUCores, r.info.MemoryMB, runtimeOrDash(r.info))
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

			c, err := kube.NewClient(opts.BootstrapKubeconfig())
			if err != nil {
				return fmt.Errorf("%w\n\nRun `%s` first", err, invoke("init"))
			}

			hosts := &infrav1.HostList{}
			if err := c.List(cmd.Context(), hosts, client.InNamespace(cfg.Cluster.Namespace)); err != nil {
				return fmt.Errorf("list hosts: %w", err)
			}

			sort.Slice(hosts.Items, func(i, j int) bool {
				return hosts.Items[i].Name < hosts.Items[j].Name
			})

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
