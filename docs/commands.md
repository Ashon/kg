<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Command reference

[Documentation](README.md) · [Project overview](../README.md)

| Command                    | What it does                                        |
| -------------------------- | --------------------------------------------------- |
| `config init` / `validate` | Write and check `~/.kg/config`                      |
| `inventory check`          | SSH preflight against the config alone              |
| `inventory list`           | The pool as the management cluster sees it          |
| `inventory trust`          | Accept the host key a machine presents now          |
| `init`                     | Bootstrap cluster, providers, inventory             |
| `cluster create`           | Apply the cluster; `--dry-run` prints the manifests |
| `cluster status`           | Live status; `--cluster` selects a registered cluster |
| `cluster delete`           | Tear a genesis-managed cluster down and reset its hosts |
| `cluster forget`           | Remove a local record, keeping the cluster and kubeconfig |
| `cni install`              | Apply the configured CNI manifests                  |
| `kubeconfig`               | Write the target cluster's kubeconfig               |
| `clusters`                 | Known clusters and their management modes           |
| `eject` / `pivot`          | Release one cluster and drop the genesis node       |
| `reset`                    | Delete the bootstrap cluster only                   |

The default configuration is `~/.kg/config`; select another file with `--config`
or `KGENESIS_CONFIG`. State lives under `~/.kg` by default: the bootstrap and
workload kubeconfigs, plus the cluster registry. `--state-dir` selects another
state directory without changing the configuration path. kg keeps these
kubeconfigs separate from your existing `~/.kube` contexts.

Use `kg <command> --help` for flags and defaults. For lifecycle operations and
registered cluster selectors, see [Cluster management](cluster-management.md).
