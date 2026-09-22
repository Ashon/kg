// Package capi drives Cluster API through the clusterctl library, so kgenesis
// does not require a clusterctl binary on the genesis node.
package capi

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	clusterctl "sigs.k8s.io/cluster-api/cmd/clusterctl/client"
	clusterctllog "sigs.k8s.io/cluster-api/cmd/clusterctl/log"
)

// Provider names as clusterctl knows them.
const (
	CoreProvider         = "cluster-api"
	BootstrapProvider    = "kubeadm"
	ControlPlaneProvider = "kubeadm"
)

// InitOptions describes the providers to install into the bootstrap cluster.
type InitOptions struct {
	KubeconfigPath string

	// Version pins the core, bootstrap and control plane providers. Empty lets
	// clusterctl resolve the latest release.
	Version string

	// InfrastructureProvider is the kgenesis provider reference in clusterctl
	// form, for example "kgenesis:v0.1.0". Empty skips it, which is what the
	// out-of-tree development flow does: the provider is applied from local
	// manifests instead.
	InfrastructureProvider string

	// WaitTimeout bounds how long to wait for the provider Deployments.
	WaitTimeout time.Duration
}

// Init installs cert-manager and the Cluster API providers.
func Init(ctx context.Context, opts InitOptions) ([]clusterctl.Components, error) {
	c, err := clusterctl.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("create the clusterctl client: %w", err)
	}

	initOpts := clusterctl.InitOptions{
		Kubeconfig:            clusterctl.Kubeconfig{Path: opts.KubeconfigPath},
		CoreProvider:          withVersion(CoreProvider, opts.Version),
		BootstrapProviders:    []string{withVersion(BootstrapProvider, opts.Version)},
		ControlPlaneProviders: []string{withVersion(ControlPlaneProvider, opts.Version)},
		LogUsageInstructions:  false,
		WaitProviders:         true,
		WaitProviderTimeout:   opts.WaitTimeout,
	}
	if opts.InfrastructureProvider != "" {
		initOpts.InfrastructureProviders = []string{opts.InfrastructureProvider}
	}

	components, err := c.Init(ctx, initOpts)
	if err != nil {
		return nil, fmt.Errorf("install the Cluster API providers: %w", err)
	}
	return components, nil
}

// withVersion appends a pinned version when one was configured.
func withVersion(provider, version string) string {
	if version == "" {
		return provider
	}
	return provider + ":" + version
}

// MoveOptions describes a pivot.
type MoveOptions struct {
	FromKubeconfig string
	ToKubeconfig   string

	// Namespace holds the Cluster API objects to move. Empty moves the namespace
	// of the source kubeconfig's current context.
	Namespace string

	DryRun bool
}

// Move transfers the Cluster API objects to the target cluster, which is what
// makes the new cluster self-managing and lets the genesis node be discarded.
func Move(ctx context.Context, opts MoveOptions) error {
	c, err := clusterctl.New(ctx, "")
	if err != nil {
		return fmt.Errorf("create the clusterctl client: %w", err)
	}

	if err := c.Move(ctx, clusterctl.MoveOptions{
		FromKubeconfig: clusterctl.Kubeconfig{Path: opts.FromKubeconfig},
		ToKubeconfig:   clusterctl.Kubeconfig{Path: opts.ToKubeconfig},
		Namespace:      opts.Namespace,
		DryRun:         opts.DryRun,
	}); err != nil {
		return fmt.Errorf("move the Cluster API objects: %w", err)
	}
	return nil
}

// GetKubeconfig returns the workload cluster's kubeconfig from the management
// cluster, where Cluster API stores it as a Secret.
func GetKubeconfig(ctx context.Context, managementKubeconfig, clusterName, namespace string) (string, error) {
	c, err := clusterctl.New(ctx, "")
	if err != nil {
		return "", fmt.Errorf("create the clusterctl client: %w", err)
	}

	kubeconfig, err := c.GetKubeconfig(ctx, clusterctl.GetKubeconfigOptions{
		Kubeconfig:          clusterctl.Kubeconfig{Path: managementKubeconfig},
		WorkloadClusterName: clusterName,
		Namespace:           namespace,
	})
	if err != nil {
		return "", fmt.Errorf("get the kubeconfig for cluster %s/%s: %w", namespace, clusterName, err)
	}
	return kubeconfig, nil
}

// SetLogger routes clusterctl's output.
//
// clusterctl logs a full Go stack trace for every retried API call during init,
// which is normal while the API server is still settling but reads like a crash.
// Passing a discarding logger keeps that out of the way unless the operator asked
// for it with --verbose.
func SetLogger(l logr.Logger) {
	clusterctllog.SetLogger(l)
}

// DiscardLogger drops everything clusterctl logs.
func DiscardLogger() logr.Logger { return logr.Discard() }

// TextLogger writes clusterctl's messages to w, without the stack traces that
// its own default logger attaches to retried calls.
func TextLogger(w io.Writer) logr.Logger {
	return funcr.New(func(prefix, args string) {
		fmt.Fprintf(w, "%s %s\n", prefix, args)
	}, funcr.Options{})
}
