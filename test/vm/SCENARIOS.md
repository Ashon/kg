<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Virtual machine scenarios

Each scenario is one path an operator actually takes, and each asserts what that
path is supposed to leave behind. They share one fleet of virtual machines and
run in order, because every one of them depends on the state the previous one
ends in.

Run them with `test/vm/run.sh`, or a subset by name:

```console
$ test/vm/run.sh                       # all of them, in order
$ test/vm/run.sh build vip-failover    # two of them, on an existing fleet
$ REUSE=1 test/vm/run.sh scale         # skip creating and provisioning the VMs
```

## The fleet

Six machines on one socket_vmnet segment, `192.168.105.0/24`. Three are
control planes, two are workers, and one is spare so a scale-out has somewhere
to go. The Mac is the genesis node. The control plane VIP is `192.168.105.200`.

| Host          | Address           | Role          |
| ------------- | ----------------- | ------------- |
| `kg-cp-1`     | `192.168.105.11`  | control-plane |
| `kg-cp-2`     | `192.168.105.12`  | control-plane |
| `kg-cp-3`     | `192.168.105.13`  | control-plane |
| `kg-worker-1` | `192.168.105.14`  | worker        |
| `kg-worker-2` | `192.168.105.15`  | worker        |
| `kg-worker-3` | `192.168.105.16`  | worker, spare |

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
