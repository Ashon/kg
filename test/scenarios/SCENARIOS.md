<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Scenarios

Each scenario is one path an operator actually takes, and each asserts what that
path is supposed to leave behind. They share one fleet of machines and run in
order, because every one of them depends on the state the previous one ends in.

Run them with `test/scenarios/run.sh`, or a subset by name:

```console
$ test/scenarios/run.sh                      # all of them, in order
$ test/scenarios/run.sh build vip-failover   # two of them, on an existing fleet
$ DRIVER=docker test/scenarios/run.sh        # against containers
$ REUSE=1 test/scenarios/run.sh scale        # skip creating the machines
```

## The fleets

A case says what it needs from the machines it runs on, and a fleet says what it
can give. A case that needs what a fleet cannot give is skipped and reported as
skipped, because a case that quietly asserts less than it claims is worse than
no case at all.

| Driver    | Machines                        | Gives           | Runs where          |
| --------- | ------------------------------- | --------------- | ------------------- |
| `lima`    | Lima virtual machines           | `vip`, `reboot` | macOS               |
| `libvirt` | KVM virtual machines            | `vip`, `reboot` | Linux, CI nightly   |
| `docker`  | containers on one docker bridge | neither         | Linux, CI per push  |

kgenesis reaches a host over SSH and nothing else, so a container that answers
on port 22 and runs kubeadm is indistinguishable from a machine as far as the
provider is concerned. What a container cannot give is a kernel and a network
stack of its own, which is what kube-vip's election needs and what makes a
stopped machine a machine that went away rather than one that paused.

## The machines

Hosts are named `kg-cp-N` and `kg-worker-N`. There are more of them than any one
cluster asks for: a scale-out needs a host free to move onto, and two clusters
need a control plane host each.

| Driver    | Control planes | Workers | Cluster asks for    | Endpoint                    |
| --------- | -------------- | ------- | ------------------- | --------------------------- |
| `lima`    | 3              | 3       | 3 control, 2 worker | a VIP on `192.168.105.0/24` |
| `libvirt` | 4              | 2       | 3 control, 1 worker | a VIP on `192.168.105.0/24` |
| `docker`  | 2              | 2       | 1 control, 1 worker | the first control plane     |

`libvirt` uses the same addresses as `lima` on purpose, so a scenario cannot
come to depend on one of them.

The genesis node is the machine running the suite: this Mac under `lima`, the
runner under `libvirt` and `docker`.

## The paths

### 1. `inventory` - the pool is described truthfully

`kg inventory check` against the fleet, plus one address with nothing on it.

Asserts: every real host reports its OS, architecture, CPU count, memory and
container runtime; the runtime is a name and a version and not the whole
`--version` line; the dead address is reported as unreachable rather than
quietly skipped, and never reaches `Available`.

### 2. `build` - a cluster from nothing

`kg init`, then `kg cluster create --wait` for three control planes and two
workers, then the CNI.

Asserts: all five nodes Ready; each node advertises **its own** address rather
than the one its default route happens to carry; every node carries a
`kgenesis://` provider ID; three etcd members; the API server answers on the VIP.

### 3. `vip-failover` - the VIP outlives its holder

Stops the machine holding the VIP, waits for it to move, then brings the machine
back.

Asserts: the VIP moves to another control plane; the API server answers on it
again; the machine that went away rejoins and returns to Ready.

These machines take a fresh cloud-init identity on every start, so the one that
comes back presents a different SSH host key. Real hardware does not, but a
machine that was reinstalled does, and TOFU refuses both the same way - that is
what it is for. So the scenario also does the operator's half: `kg inventory
trust` accepts the new key, and the pinned one is checked to be gone.

### 4. `scale` - the pool absorbs a change

Takes the worker pool from two replicas to three and back.

Asserts: the new machine claims the spare host and its node joins Ready; scaling
back removes one machine - whichever the `MachineDeployment` chooses, which is
not necessarily the one that joined last - and returns its host to the pool as
`Available`, with nothing of the cluster left on it.

### 5. `rebuild` - a host can be used twice

`kg cluster delete`, then `kg cluster create` again on the same machines.

Asserts: after the delete, every host is `Available` with no `/etc/kubernetes`,
no etcd data and no leftover VIP on its interface; the second cluster comes up on
the same hardware. A host that keeps the VIP from its last life sends the next
`kubeadm join` to itself, so this is the path that catches it.

### 6. `release` - letting a cluster go

`kg eject` on the finished cluster.

Asserts: the cluster still serves every node; nothing kgenesis installed is
running inside it; the genesis node and its kubeconfig are gone; `kg clusters`
says so rather than reporting a parse error.

### 7. `multi-cluster` - one genesis node, several clusters

A fresh genesis node builds `alpha` and `beta`, one control plane and one worker
each, in a namespace per cluster. Then it releases them one at a time.

Asserts: `kg clusters` lists both; each cluster draws only from the hosts in its
own namespace; `--self-manage` is refused on `alpha`, which has one control plane
and nothing spare to roll onto, and the refusal leaves it untouched; releasing
`alpha` leaves the genesis node running and `beta` untouched; `alpha` then reads
as `Released`; releasing `beta` takes the genesis node with it.

### 8. `self-manage` - the cluster carries its own management

A fresh cluster with three control planes, handed its own Cluster API with
`kg eject --self-manage`.

Asserts: the objects, the SSH credential and the provider all arrive; every
machine comes back to Running without kubeadm being run again; the hosts that
were free before the handover are the same ones free after it, because a
provider that failed to recognise what it inherited would claim fresh ones; the
genesis node is gone.

`clusterctl move` recreates every object with a new UID and carries no status, so
this is the path that says whether the claim survives being moved. It needs a
host free of each role, which is what `--self-manage` is refused without, so the
`lima` and `libvirt` fleets carry one more control plane than the cluster asks
for.
