// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Ashon/kg/internal/config"
	"github.com/Ashon/kg/internal/registry"
)

func (o *Options) registry() registry.Store { return registry.Store{Dir: o.StateDir} }

// Keep the original default namespace path. Custom namespaces need a distinct
// path: two clusters with the same name must not overwrite each other's keys.
func (o *Options) workloadPath(namespace, name string) string {
	if namespace == name {
		return o.WorkloadKubeconfig(name)
	}
	return filepath.Join(o.StateDir, "kubeconfigs", registry.Key(namespace, name)+".kubeconfig")
}

func (o *Options) recordFor(cfg *config.Config, mode registry.Mode) (*registry.Record, error) {
	workload, err := filepath.Abs(o.workloadPath(cfg.Cluster.Namespace, cfg.Cluster.Name))
	if err != nil {
		return nil, err
	}
	management, err := filepath.Abs(o.BootstrapKubeconfig())
	if err != nil {
		return nil, err
	}
	switch mode {
	case registry.SelfManaged:
		management = workload
	case registry.Released:
		management = ""
	}
	return &registry.Record{
		Version: 1, Name: cfg.Cluster.Name, Namespace: cfg.Cluster.Namespace, Mode: mode,
		Endpoint:             net.JoinHostPort(cfg.Cluster.ControlPlaneEndpoint.Host, strconv.Itoa(int(cfg.Cluster.ControlPlaneEndpoint.Port))),
		ManagementKubeconfig: management, WorkloadKubeconfig: workload,
	}, nil
}

func (o *Options) remember(cfg *config.Config, mode registry.Mode) error {
	r, err := o.recordFor(cfg, mode)
	if err != nil {
		return err
	}
	if err := o.registry().Put(*r); err != nil {
		return fmt.Errorf("save cluster management record: %w", err)
	}
	return nil
}

// requireGenesisManaged prevents an old config from recreating an ejected
// cluster on the genesis node or deleting a cluster's own management plane.
func (o *Options) requireGenesisManaged(cfg *config.Config) error {
	r, err := o.registry().Get(cfg.Cluster.Namespace, cfg.Cluster.Name)
	if err != nil {
		return err
	}
	if r != nil && r.Mode != registry.Managed {
		return fmt.Errorf("cluster %s/%s is %s; this operation requires genesis management. Use its workload kubeconfig for direct administration; use `%s` only after retiring the cluster to reuse its identity",
			r.Namespace, r.Name, r.Mode, invoke("cluster forget --cluster "+r.Namespace+"/"+r.Name))
	}
	return nil
}

// selectRecord allows observations without the original config or SSH keys.
// Without --cluster, preserve selection through --config / KG_CONFIG.
func (o *Options) selectRecord(selector string) (*registry.Record, error) {
	if selector == "" {
		cfg, err := o.Load()
		if err != nil {
			return nil, err
		}
		r, err := o.registry().Get(cfg.Cluster.Namespace, cfg.Cluster.Name)
		if err != nil || r != nil {
			return r, err
		}
		return o.recordFor(cfg, registry.Managed)
	}
	records, err := o.registry().List()
	if err != nil {
		return nil, err
	}
	var found *registry.Record
	for _, r := range records {
		if selector != r.Name && selector != r.Namespace+"/"+r.Name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("cluster %q is ambiguous; use namespace/name", selector)
		}
		copy := r
		found = &copy
	}
	if found == nil {
		return nil, fmt.Errorf("cluster %q is not registered; run `%s` to see known clusters", selector, invoke("clusters"))
	}
	return found, nil
}

func newClusterForgetCommand(opts *Options) *cobra.Command {
	var selector string
	var yes bool
	cmd := &cobra.Command{
		Use: "forget", Short: "Remove a cluster from the local registry without touching it",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := opts.selectRecord(selector)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(cmd.OutOrStdout(), "This removes the local record for %s/%s. The cluster and kubeconfigs are kept. Re-run with --yes to proceed.\n", r.Namespace, r.Name)
				return nil
			}
			if err := opts.registry().Forget(r.Namespace, r.Name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Forgot %s/%s. The cluster and kubeconfigs were not changed.\n", r.Namespace, r.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&selector, "cluster", "", "Registered cluster name or namespace/name (default: configuration)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Remove the local record")
	return cmd
}

// managementPath resolves CAPI operations without guessing from which files
// happen to exist. Released clusters retain a workload connection only.
func (o *Options) managementPath(cfg *config.Config) (string, error) {
	r, err := o.registry().Get(cfg.Cluster.Namespace, cfg.Cluster.Name)
	if err != nil {
		return "", err
	}
	if r == nil {
		return o.BootstrapKubeconfig(), nil
	}
	if r.Mode == registry.Released {
		return "", fmt.Errorf("cluster %s/%s is released and has no CAPI host inventory", r.Namespace, r.Name)
	}
	return r.ManagementKubeconfig, nil
}
