# kg

[![CI](https://github.com/Ashon/kg/actions/workflows/ci.yml/badge.svg)](https://github.com/Ashon/kg/actions/workflows/ci.yml)
[![Scenarios](https://github.com/Ashon/kg/actions/workflows/scenarios.yml/badge.svg)](https://github.com/Ashon/kg/actions/workflows/scenarios.yml)
[![Scenarios on machines](https://github.com/Ashon/kg/actions/workflows/scenarios-machines.yml/badge.svg)](https://github.com/Ashon/kg/actions/workflows/scenarios-machines.yml)
[![Latest release](https://img.shields.io/github/v/release/Ashon/kg?include_prereleases&sort=semver)](https://github.com/Ashon/kg/releases)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

**Manage Kubernetes clusters on prepared hosts.**

kg turns pools of physical servers or VMs into Kubernetes clusters over SSH.
A local **genesis node** runs Cluster API to create and manage them. Use one
genesis node for several clusters, then keep managing them, move management into
a workload cluster, or release a cluster to run independently.

The result is an ordinary kubeadm cluster. You choose where its management lives.

## Core capabilities

| Capability | What you can do |
| --- | --- |
| Host inventory | Check SSH access, runtime and Kubernetes readiness before deploying |
| Cluster lifecycle | Create clusters, scale worker pools, tear down and reuse hosts |
| Multiple clusters | Manage separate cluster inventories from one genesis node |
| Management handover | Keep clusters `managed`, move to `self-managed`, or leave them `released` |
| Persistent cluster access | List known clusters and query status or export kubeconfigs after handover |
| Availability and rollouts | Use a kube-vip endpoint and spare hosts for CAPI rolling replacements |

kg is for **pre-provisioned Linux hosts** with SSH, a container runtime and
Kubernetes packages already installed. It does not provision operating systems
or upgrade host packages. You also supply the CNI manifests. The cluster
registry is local to your state directory; it is not a shared fleet database.

[Architecture](docs/architecture.md) explains the design.
[Cluster management](docs/cluster-management.md) explains the lifecycle choices.

## Install

Download the CLI archive for your operating system and architecture from
[GitHub Releases](https://github.com/Ashon/kg/releases). Verify it against the
release's `SHA256SUMS`, extract it, and place `kg` on your PATH:

```console
$ kg version
```

Releases support macOS and Linux on `amd64` and `arm64`. The machine running kg
needs Docker and SSH reachability to the hosts.

See [Installation](docs/installation.md) for exact installation steps, source
builds and host requirements.

## Quick start

Prepare the hosts, install kg, and configure your host inventory, Kubernetes
version, control plane endpoint and CNI manifests:

```console
$ kg config init
$ $EDITOR ~/.kg/config
$ kg config validate
$ kg inventory check
$ kg init
$ kg cluster create
$ kg kubeconfig
$ kg clusters
$ kg cluster status
```

This leaves the cluster managed by its genesis node. For the full walkthrough,
see [Getting started](docs/getting-started.md) and the
[configuration example](docs/configuration.md).

When you are ready to hand it over, choose deliberately:

| Command | Outcome |
| --- | --- |
| `kg eject --self-manage` | Move Cluster API into the workload cluster; requires spare hosts and an HA control plane |
| `kg eject` | Stop Cluster API management while the workload keeps running |

See [Cluster management](docs/cluster-management.md) for requirements,
limitations and multi-cluster workflows.

## Documentation

Start at the [documentation index](docs/README.md), or go directly to:

- [Installation](docs/installation.md) and [Getting started](docs/getting-started.md)
- [Configuration](docs/configuration.md): host inventory, worker pools, CNI, VIP and SSH trust
- [Cluster management](docs/cluster-management.md): multiple clusters, handovers and local records
- [Command reference](docs/commands.md)
- [Rollout order](docs/rollout-order.md)
- [Architecture](docs/architecture.md)
- [Development and testing](docs/development.md): builds, E2E, releases and CI

## Validation and development

The same scenario suite runs against container hosts and virtual machines.
VM coverage includes VIP failover, self-management and a Kubernetes rolling
upgrade. Container runs explicitly skip scenarios that require VM capabilities.
See the [scenario catalog](test/scenarios/SCENARIOS.md) for assertions and the
management acceptance cases; the badges above link to current CI results.

```console
$ make build
$ make test
```

Contributor workflows and local VM setup are in
[Development and testing](docs/development.md).

## License

MIT. See [LICENSE](LICENSE). Dependencies and vendored assets retain their own
licenses; see [third-party licensing](docs/development.md#licensing).
