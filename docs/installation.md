<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Installation

[Documentation](README.md) · [Project overview](../README.md)

## Install a release

Download the CLI archive and `SHA256SUMS` from the same
[GitHub release](https://github.com/Ashon/kg/releases). Archives are available for
macOS (`darwin`) and Linux, on `amd64` and `arm64`.

Archive names follow `kg_<version>_<os>_<arch>.tar.gz`. Choose the archive for
the machine running the CLI, and replace the values below with that release:

```sh
archive='kg_<version>_<os>_<arch>.tar.gz'
# Compare this hash with the matching entry in the downloaded SHA256SUMS.
shasum -a 256 "$archive"
tar -xzf "$archive"
mkdir -p "$HOME/.local/bin"
install -m 0755 "${archive%.tar.gz}/kg" "$HOME/.local/bin/kg"
export PATH="$HOME/.local/bin:$PATH"
kg version
```

Add `$HOME/.local/bin` to your shell's PATH to keep it available in new sessions.
`kg version` prints the CLI version and the controller image it installs.
Use the release's CLI and matching image together.

## Build from source

Install the Go version declared in `go.mod`, then:

```sh
git clone https://github.com/Ashon/kg.git
cd kg
make build
./bin/kg version
# Optional: install the CLI into GOBIN, or GOPATH/bin when GOBIN is unset.
make install
```

For an unreleased checkout, build the matching provider with `make docker-build`
before `kg init`. The CLI loads a locally available image into its bootstrap
cluster. See [Development and testing](development.md) for the full workflow.

## Host and operator requirements

On the genesis node:

- Docker, for the kind bootstrap cluster
- Network reachability to every host on port 22

On every host:

- A Linux distribution with `bash`, `base64`, `install`, `setsid`, `timeout` (GNU coreutils) and `systemctl`
- A container runtime, normally containerd
- `kubeadm`, `kubelet` and `kubectl`, at the minor `cluster.kubernetesVersion`
  asks for. kg does not install them: it bootstraps machines that are
  already provisioned, so the host decides which Kubernetes it can build
- SSH as root, by key or password
- No existing kubeadm state

`kg inventory check` reports host readiness, including a kubeadm a minor away from
what the configuration asks for, which kubeadm itself would only refuse several
minutes into a rollout.

The bootstrap prepares two things itself and stops with a clear message when it
cannot, rather than letting kubeadm fail several minutes later wearing someone
else's name:

- **Swap off.** Every active area in `/proc/swaps` is disabled and the
  `/etc/fstab` entries commented out. `swapoff -a` alone would miss a swapfile
  enabled by hand, zram or systemd-swap, and kubelet refuses to start with any
  of it on.
- **Bridge netfilter.** `br_netfilter` is loaded if it is a module, and its
  presence is then verified. Without it kube-proxy's rules never see pod
  traffic, and `sysctl --system` reports success either way.

Continue with [Getting started](getting-started.md).
