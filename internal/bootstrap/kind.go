// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package bootstrap manages the ephemeral management cluster that runs on the
// genesis node. It exists only long enough to stamp out the real cluster and is
// thrown away after `kg eject`.
package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cmd"
)

// Cluster wraps the kind provider.
type Cluster struct {
	Name     string
	provider *cluster.Provider
}

// New returns a handle for the named kind cluster. Nothing is created yet.
func New(name string, logOutput io.Writer) *Cluster {
	logger := cmd.NewLogger()
	if l, ok := logger.(interface{ SetWriter(io.Writer) }); ok && logOutput != nil {
		l.SetWriter(logOutput)
	}

	return &Cluster{
		Name:     name,
		provider: cluster.NewProvider(cluster.ProviderWithLogger(logger)),
	}
}

// Exists reports whether the cluster is already running.
func (c *Cluster) Exists() (bool, error) {
	names, err := c.provider.List()
	if err != nil {
		return false, fmt.Errorf("list kind clusters: %w", err)
	}
	return slices.Contains(names, c.Name), nil
}

// CreateOptions tunes cluster creation.
type CreateOptions struct {
	// NodeImage pins the kind node image, and with it the bootstrap cluster's
	// Kubernetes version. Empty uses kind's default.
	NodeImage string

	// KubeconfigPath is where the cluster's kubeconfig is written. It is kept
	// separate from the operator's own kubeconfig so bootstrapping never disturbs
	// the contexts they already have.
	KubeconfigPath string
}

// Create brings the cluster up, or adopts it when it is already running.
func (c *Cluster) Create(ctx context.Context, opts CreateOptions) error {
	exists, err := c.Exists()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(opts.KubeconfigPath), 0o700); err != nil {
		return fmt.Errorf("prepare kubeconfig directory: %w", err)
	}

	if exists {
		// Re-export the kubeconfig: the cluster may predate this config file, or
		// the file may have been removed.
		if err := c.provider.ExportKubeConfig(c.Name, opts.KubeconfigPath, false); err != nil {
			return fmt.Errorf("export kubeconfig for the existing cluster: %w", err)
		}
		return nil
	}

	createOpts := []cluster.CreateOption{
		cluster.CreateWithKubeconfigPath(opts.KubeconfigPath),
		cluster.CreateWithWaitForReady(waitForReady),
		cluster.CreateWithDisplayUsage(false),
		cluster.CreateWithDisplaySalutation(false),
	}
	if opts.NodeImage != "" {
		createOpts = append(createOpts, cluster.CreateWithNodeImage(opts.NodeImage))
	}

	if err := c.provider.Create(c.Name, createOpts...); err != nil {
		return fmt.Errorf("create the bootstrap cluster: %w", err)
	}
	return nil
}

// Delete removes the cluster. Deleting one that does not exist is not an error,
// so teardown is safe to re-run.
func (c *Cluster) Delete(kubeconfigPath string) error {
	if err := c.provider.Delete(c.Name, kubeconfigPath); err != nil {
		return fmt.Errorf("delete the bootstrap cluster: %w", err)
	}
	return nil
}

// LoadImage makes a locally built image available inside the cluster, which is
// how the kgenesis provider image gets in without a registry.
func (c *Cluster) LoadImage(ctx context.Context, image string) error {
	nodes, err := c.provider.ListNodes(c.Name)
	if err != nil {
		return fmt.Errorf("list nodes of the bootstrap cluster: %w", err)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("bootstrap cluster %q has no nodes", c.Name)
	}
	return loadImageToNodes(ctx, image, nodes)
}
