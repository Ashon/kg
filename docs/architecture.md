<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Architecture

[Documentation](README.md) · [Project overview](../README.md)

## Why a genesis node

Cluster API has a bootstrap problem: to create a cluster you need a cluster. On
clouds the answer is a management cluster someone else already runs. On bare
metal there is nothing to start from, so kg makes one on the spot.

CABPK produces cloud-init, but pre-provisioned hosts have no metadata service to
hand it to them. The usual answers are an agent installed on every host (BYOH) or
full lifecycle management through BMC and PXE (Metal3). kg takes the third
path: it connects over SSH, which needs nothing installed ahead of time and works
the same for a rack of servers and a handful of VMs.

## Components

```
genesis node (kind)                        target cluster (your hosts)
+--------------------------+               +---------------------------------+
| cert-manager             |               |   cp-1     cp-2     cp-3        |
| Cluster API core         |               |                                 |
| CABPK (bootstrap)        | --- SSH --->  |   worker-1 worker-2 ...         |
| KCP (control plane)      |  cloud-init   |                                 |
| kg infra provider        |               |   kube-vip holds the VIP        |
+--------------------------+               +---------------------------------+
            |
            +-- keep managing, release, or move management to the workload
```

What the target cluster gets is a working kubeadm cluster, not a copy of the
genesis node. Releasing it leaves it on its own: the SSH credentials stay on the genesis
side, and no kg or CAPI management controller runs in the workload cluster.
Its CNI and optional kube-vip continue serving the cluster. See
[Release or self-manage](cluster-management.md#release-or-self-manage).

The provider adds four resources:

| Kind                  | What it is                                              |
| --------------------- | ------------------------------------------------------- |
| `Host`                | One machine in the pool: address, SSH credentials, state |
| `HostCluster`         | The infrastructure cluster; carries the control plane endpoint |
| `HostMachine`         | A Cluster API Machine bound to a claimed `Host`         |
| `HostMachineTemplate` | What KubeadmControlPlane and MachineDeployment stamp from |

## Host bootstrap

The provider renders CABPK's cloud-config into a single bash script rather than
relying on a cloud-init daemon, which nothing would feed on a pre-provisioned
host anyway. The script is uploaded to `/var/lib/kgenesis/bootstrap.sh` and run
detached, so a reconcile never blocks on kubeadm:

```
/var/lib/kgenesis/bootstrap.sh    the rendered script
/var/lib/kgenesis/checksum        which bootstrap data it came from
/var/lib/kgenesis/exit-code       written when the run finishes
/var/log/kgenesis-bootstrap.log   the full output
```

Every piece of state the controller needs is on the host, so a run survives a
controller restart and a `clusterctl move`. When a bootstrap fails, that log is
the first place to look; its tail is also copied onto the `HostMachine`'s
`Provisioned` condition.

### Provider IDs

Cluster API pairs a Node with its Machine by provider ID, so the kubelet has to
register with one. It cannot come from the `KubeadmConfig`, because the bootstrap
data is generated per Machine before any host has been claimed. kg knows
the value by the time it pushes the script, so it rewrites the kubeadm
configuration in flight, adding `provider-id` to
`nodeRegistration.kubeletExtraArgs`.

A systemd drop-in looks like the simpler answer and does not work: systemd lets
`EnvironmentFile=` override `Environment=` whatever the order, and the kubeadm
packages install an `/etc/default/kubelet` that sets `KUBELET_EXTRA_ARGS`. A
drop-in is silently ignored on exactly the hosts this provider targets. Going
through kubeadm puts the flag in `KUBELET_KUBEADM_ARGS`, which nothing else
competes for.

Deleting a machine runs `kubeadm reset` and the matching cleanup before the host
returns to the pool.

### Node addresses

The same rewrite pins the address each node publishes: `node-ip` for the kubelet
and, on a control plane node, `localAPIEndpoint.advertiseAddress` for the API
server and the etcd URLs derived from it. Both otherwise default to the address
of the default route.

That default is wrong on any host with more than one interface, and actively
breaks a cluster whose hosts sit behind a per-machine NAT: every node publishes
the identical NAT address, so Nodes collide on it, the `kubernetes` Service
points at whichever answers, and the second etcd member never finds the first.
The address in the inventory is the one kg reaches the host on, so that is
the one the cluster uses.
