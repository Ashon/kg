// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"sigs.k8s.io/kind/pkg/cluster/nodes"
	"sigs.k8s.io/kind/pkg/cluster/nodeutils"
)

// waitForReady bounds how long Create blocks on the control plane coming up.
// Pulling the node image on a cold genesis node is the slow part.
const waitForReady = 5 * time.Minute

// loadImageToNodes streams a locally built image into the cluster's containerd,
// so a provider image built on the genesis node can be used without a registry.
func loadImageToNodes(ctx context.Context, image string, targets []nodes.Node) error {
	save := exec.CommandContext(ctx, "docker", "save", image)
	archive, err := save.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe docker save: %w", err)
	}
	if err := save.Start(); err != nil {
		return fmt.Errorf("run docker save %s: %w", image, err)
	}

	loadErr := loadToEach(archive, targets)

	// Drain anything left so docker save never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, archive)
	if err := save.Wait(); err != nil {
		return fmt.Errorf("docker save %s: %w", image, err)
	}
	return loadErr
}

func loadToEach(archive io.Reader, targets []nodes.Node) error {
	// The archive is a stream and can only be read once, so it is buffered when
	// more than one node needs it.
	if len(targets) == 1 {
		if err := nodeutils.LoadImageArchive(targets[0], archive); err != nil {
			return fmt.Errorf("load the image into node %s: %w", targets[0].String(), err)
		}
		return nil
	}

	buffered, err := io.ReadAll(archive)
	if err != nil {
		return fmt.Errorf("read the image archive: %w", err)
	}
	for _, node := range targets {
		if err := nodeutils.LoadImageArchive(node, bytes.NewReader(buffered)); err != nil {
			return fmt.Errorf("load the image into node %s: %w", node.String(), err)
		}
	}
	return nil
}

// ImageAvailableLocally reports whether the local Docker daemon already holds
// the image. A genesis node is often the machine the provider was built on, and
// the image it built has never been pushed anywhere, so this is what decides
// between loading it straight into the bootstrap cluster and letting kubelet
// pull from a registry.
func ImageAvailableLocally(ctx context.Context, image string) bool {
	if image == "" {
		return false
	}
	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	inspect.Stdout = io.Discard
	inspect.Stderr = io.Discard
	return inspect.Run() == nil
}
