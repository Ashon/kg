// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
	"github.com/Ashon/kgenesis/internal/config"
	"github.com/Ashon/kgenesis/internal/ssh"
	"github.com/Ashon/kgenesis/internal/version"
)

// providerImageFor reports the controller image that will actually run: the
// override when one was given, and the one this build was stamped with
// otherwise. Callers need it before installing, to decide whether the image has
// to be carried to the machines that will run it.
func providerImageFor(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if version.Image == "" {
		return "", errors.New("this build carries no controller image; pass --provider-image")
	}
	return version.Image, nil
}

// claimedHosts reports the configured hosts this cluster has taken, which are
// the machines the controller can be scheduled onto once it moves. The claim
// lives on the Host in the bootstrap cluster, so a genesis node holding several
// clusters does not hand one cluster's work to another's hosts.
func claimedHosts(ctx context.Context, c client.Client, cfg *config.Config) ([]config.HostConfig, error) {
	hosts := &infrav1.HostList{}
	if err := c.List(ctx, hosts,
		client.InNamespace(cfg.Cluster.Namespace),
		client.MatchingLabels{infrav1.ClusterNameLabel: cfg.Cluster.Name}); err != nil {
		return nil, fmt.Errorf("list the hosts claimed by %s: %w", cfg.Cluster.Name, err)
	}

	claimed := map[string]bool{}
	for i := range hosts.Items {
		if hosts.Items[i].Status.ClaimRef != nil {
			claimed[hosts.Items[i].Name] = true
		}
	}

	out := make([]config.HostConfig, 0, len(claimed))
	for _, host := range cfg.Hosts {
		if claimed[host.Name] {
			out = append(out, host)
		}
	}
	return out, nil
}

// seedProviderImage imports the controller image into each host's containerd.
//
// After the pivot the controller runs on the cluster it just built, and nothing
// constrains which of its nodes it lands on. The image it needs is the one built
// on the genesis node and pushed nowhere, so every host that could run it gets it
// directly; without this the moved controller sits in ImagePullBackOff and the
// cluster has no management plane at all.
func seedProviderImage(ctx context.Context, hosts []config.HostConfig, image string, out io.Writer) error {
	for _, host := range hosts {
		if err := seedHost(ctx, host, image); err != nil {
			return fmt.Errorf("seed %s onto %s: %w", image, host.Name, err)
		}
		fmt.Fprintf(out, "  %s\n", host.Name)
	}
	return nil
}

func seedHost(ctx context.Context, host config.HostConfig, image string) error {
	sshCfg, err := sshConfigFor(host)
	if err != nil {
		return err
	}
	conn, err := ssh.Dial(ctx, sshCfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	save := exec.CommandContext(ctx, "docker", "save", image)
	archive, err := save.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe docker save: %w", err)
	}
	if err := save.Start(); err != nil {
		return fmt.Errorf("run docker save: %w", err)
	}

	// k8s.io is the namespace kubelet looks in; an import anywhere else is
	// invisible to it.
	res, runErr := conn.RunWithInput(ctx, "ctr --namespace k8s.io images import -", archive)

	// Drain whatever is left so docker save never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, archive)
	if err := save.Wait(); err != nil {
		return fmt.Errorf("docker save: %w", err)
	}
	if runErr != nil {
		return fmt.Errorf("%w: %s", runErr, strings.TrimSpace(res.Stderr))
	}
	return nil
}
