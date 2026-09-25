<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Getting started

[Documentation](README.md) · [Project overview](../README.md)

Start with the [installation and host requirements](installation.md). This guide
creates one cluster on prepared hosts and leaves the genesis node managing it.

## Configure and check the hosts

```console
$ kg config init
$ $EDITOR ~/.kg/config
$ kg config validate
$ kg inventory check
```

Set the host addresses, SSH credentials, Kubernetes version, control plane
endpoint and CNI manifests. See [Configuration](configuration.md) for an example.
The hosts must already carry the runtime and Kubernetes packages; kg does not
install them. Provide a CNI manifest before expecting Nodes to become Ready.

The default config is `~/.kg/config`. `--config` (or `-c`) and `KGENESIS_CONFIG`
select another file, so commands can be run from any directory.

## Create and inspect the cluster

```console
$ kg init
$ kg cluster create
$ kg kubeconfig
$ kg clusters
$ kg cluster status
```

`init` creates the local kind genesis cluster and loads providers and inventory.
`cluster create` applies the desired cluster, waits for its machines by default,
and installs the configured CNI. `kubeconfig` prints the path it wrote; set
`KUBECONFIG` to that path when using `kubectl`.

```console
$ export KUBECONFIG=~/.kg/lab.kubeconfig   # use the path kg printed
$ kubectl get nodes
```

If bootstrap fails, inspect `/var/log/kgenesis-bootstrap.log` on the affected
host and its HostMachine conditions. [Architecture](architecture.md#host-bootstrap)
explains the on-host state and logs.

## Choose what manages it next

| Keep or change management | Command | Result |
| --- | --- | --- |
| Keep genesis managing the cluster | No handover command | `managed` |
| Move CAPI into the workload cluster | `kg eject --self-manage` | `self-managed` |
| Stop CAPI management | `kg eject` | `released` |

Self-management requires at least three control planes and spare capacity for
rolling replacements. Release retains the running cluster but gives up CAPI
lifecycle operations. Read [Cluster management](cluster-management.md) before
choosing a handover; a release cannot be reversed by editing the local registry.

The same guide covers multiple clusters, registry selectors and safe removal of
local records. [Command reference](commands.md) lists the CLI entry points.
