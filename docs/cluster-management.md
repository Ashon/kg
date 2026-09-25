<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Cluster management

[Documentation](README.md) · [Project overview](../README.md)

## Multiple clusters

A genesis node is not limited to one cluster. Keep a configuration per cluster
and point at it:

```console
$ kg init -c ~/.kg/lab.yaml
$ kg cluster create -c ~/.kg/lab.yaml
$ kg init -c ~/.kg/prod.yaml
$ kg cluster create -c ~/.kg/prod.yaml
$ kg clusters
CLUSTER  NAMESPACE  MANAGEMENT  PHASE        ENDPOINT          HOSTS
lab      lab        managed     Provisioned  10.10.0.100:6443  5/5
prod     prod       managed     Provisioned  10.20.0.100:6443  7/9
```

Each cluster gets its own namespace, named after it unless `cluster.namespace`
says otherwise. That is not tidiness. A machine draws from the host pool in its
own namespace, so one cluster cannot take hosts meant for another, and
`clusterctl` moves a namespace rather than a cluster, so a namespace per cluster
is what makes ejecting one of several possible at all.

`kg eject` releases one cluster and leaves the rest alone. The genesis node is
deleted once nothing is left for it to manage, and kept otherwise.

## Management modes and local registry

kg keeps a local registry under `<state-dir>/clusters` (by default `~/.kg/clusters`).
Each record identifies a cluster by namespace and name and stores its endpoint,
kubeconfig paths and last confirmed management mode. SSH credentials and
kubeconfig contents are not copied into the registry.

| Management | Recorded when | What `cluster status` queries |
| ---------- | ------------- | ----------------------------- |
| `managed` | `cluster create` applies its objects, or `clusters` discovers it on the genesis node | CAPI on the genesis node |
| `self-managed` | `eject --self-manage` successfully moves and verifies the objects | CAPI on the workload cluster |
| `released` | `eject` successfully pauses management | Kubernetes Nodes on the workload cluster |

Management mode is independent of health. An unreachable cluster stays registered
in its last confirmed mode. Pausing CAPI manually does not mean it was released.
`clusters` shows live genesis phases when available; `-` means that field was not
checked. It does not contact every workload cluster to produce the list.

```console
$ kg clusters
$ kg clusters --offline
$ kg cluster status --cluster lab
$ kg cluster status --cluster production/lab --watch
$ kg kubeconfig --cluster production/lab --stdout
```

`--cluster` selects a saved record without loading the original configuration or
SSH keys. Use `namespace/name` when names are ambiguous. Without it, the config
still selects the cluster. Status reads current desired replicas from CAPI, so
changes made directly to a self-managed cluster are reflected. For a released
cluster, `kubeconfig` reads the saved file without asking for CAPI Secrets.

`inventory list` and `inventory trust` also follow the registered management
location; released clusters have no CAPI host inventory to query.

An unrelated cluster's genesis node may still be running: it does not change
where self-managed and released entries are queried. Kubeconfigs for custom
namespaces have separate paths under `<state-dir>/kubeconfigs`; the usual
namespace-equals-name case keeps `<state-dir>/<name>.kubeconfig`.

Creation, deletion and ejection through the genesis node are refused for an
identity registered as self-managed or released. Changing a label in a registry
cannot adopt an existing kubeadm cluster. After retiring one outside kg, remove
its local record before reusing its identity:

```console
$ kg cluster forget --cluster production/lab --yes
```

`forget` keeps both the cluster and its kubeconfig. `cluster delete` removes its
record after a successful teardown. `reset` keeps the records; it does not turn
managed clusters into released ones. A reachable genesis node will rediscover a
forgotten cluster it still holds.

The registry is local to one state directory. Keep it with the kubeconfigs;
it is not a shared fleet database or a backup of CAPI state. Older clusters can
be discovered while their genesis node is still present. Clusters ejected before
this registry existed are not reconstructed automatically from kubeconfig files,
which do not say whether CAPI was moved or removed.

## Release or self-manage

`kg eject` hands the cluster's kubeconfig over and stops managing it. The cluster
itself is not touched: it is an ordinary kubeadm cluster and needs nothing from
kg to serve.

What it gives up is Cluster API. Nodes are not added, replaced or upgraded
through kg afterwards, and there is no going back - Cluster API does not
adopt an existing kubeadm cluster. What it gains is that the SSH keys never leave
the genesis node, and no kg management controller remains in the workload cluster with the power
to reset its hosts.

A released cluster is left paused on the genesis node rather than deleted, so its
host claims stand and a later cluster cannot take the hosts it is running on.
`kg clusters` shows its management mode as `released`. The local registry
keeps that record after the genesis node is deleted.

`--self-manage` moves the Cluster API objects into the cluster instead, which is
what `clusterctl move` means by a pivot. The cluster then keeps itself alive:
the `upgrade` scenario rolls one to a newer Kubernetes with nothing but
`kubectl`, because there is no genesis node left to ask.

It is refused unless the cluster could survive managing itself. Cluster API keeps
a cluster healthy by replacing machines, and a self-managed cluster has to do
that to the machines its own controllers run on: `KubeadmControlPlane` adds a
machine before it removes one, and a `MachineDeployment` does the same, so each
needs a host free to add. Below three control plane replicas etcd loses quorum
the moment one goes. A cluster that cannot meet this can still be released, which
asks nothing of it.

`clusterctl move` carries objects but not their status, by design: a provider is
expected to rebuild status on the next reconcile. kg reads the claim back
off the Host itself, where a move carries it, so what arrives is a cluster still
running the hosts it had rather than one reaching for new ones.

## Rollouts

A machine is never upgraded in place: it is replaced. An upgrade is therefore a
sequence of replacements, and the question worth asking about one is which
machine goes next. It has an answer before the rollout starts.

- `KubeadmControlPlane` replaces the oldest control plane machine first. That is
  Cluster API's own behaviour, with the name breaking a tie.
- Worker pools are rendered with `deletion.order: Oldest`, so they go the same
  way. Cluster API's default is `Random`.
- The pool hands out hosts in the order `kg inventory list` prints them, and the
  digits in a name count as a number there, so `kg-worker-2` comes before
  `kg-worker-10`. A machine takes the first free host of its role.

A released host rejoins the pool only once it has been reset and probed, so a
rollout that moves faster than a reset takes the next free host rather than
waiting for the one it just gave back.

`--self-manage` carries the rules but not the ages they sort on: a move recreates
every machine, so after a handover the oldest is the one the move made first.
Read the order rather than assume it, with
`kubectl get machines -n lab --sort-by=.metadata.creationTimestamp`.

[Rollout order](rollout-order.md) traces a rollout host by host,
and says how to send a particular machine first or hold a host out of the pool.
