<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# kg documentation

[Project overview](../README.md)

kg manages Kubernetes clusters on hosts you have already prepared. Start with
installation and a single cluster, then choose how its management should continue.

## Start here

1. [Installation](installation.md): release archives, source builds and prerequisites.
2. [Getting started](getting-started.md): configure hosts, build and inspect a cluster.
3. [Cluster management](cluster-management.md): keep management on genesis, move it,
   or release the cluster.

## Operator manuals

| Guide | Use it when you need to… |
| --- | --- |
| [Configuration](configuration.md) | Set hosts, worker pools, CNI, endpoint and SSH trust |
| [Cluster management](cluster-management.md) | Operate multiple clusters, hand over management or manage local records |
| [Command reference](commands.md) | Find a command and understand configuration versus state paths |
| [Rollout order](rollout-order.md) | Plan machine replacements and understand host reuse |

## Design and contribution

- [Architecture](architecture.md): genesis, providers, host bootstrap state and node identity.
- [Development and testing](development.md): build, test, debug, release and CI workflows.
- [Scenario catalog](../test/scenarios/SCENARIOS.md): executable acceptance contracts,
  fleet capabilities and test reports.

## Keeping the docs useful

Keep the project README focused on the introduction, capabilities, installation
entry point and quick start. Put detailed procedures in the manuals and link to
them from this index. Keep scenario assertions in the scenario catalog beside
the test harness. Update the relevant manual when command behavior changes;
link to the source of detailed information instead of maintaining duplicate guides.
