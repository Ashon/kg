<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Rollout order

A machine is never upgraded in place. Cluster API replaces it: a new machine is
created, it joins, and the old one is removed. An upgrade is a sequence of those
replacements, so the only questions it raises are which machine goes next and
which host the replacement lands on. Both have an answer before the rollout
starts, and this page is that answer.

## The three deciders

| Question | Decided by | Rule |
| -------- | ---------- | ---- |
| Which control plane machine goes next | `KubeadmControlPlane` | the oldest, with the name breaking a tie |
| Which worker goes next | `MachineDeployment` `spec.deletion.order` | the oldest, because kgenesis renders `Oldest`. Cluster API defaults to `Random` |
| Which host the replacement lands on | kgenesis | the first free host of the role, in inventory order |

The first is Cluster API's own behaviour and kgenesis leaves it alone. The second
is a field kgenesis sets so worker pools go the same way the control plane
already does. The third is kgenesis's alone: hosts come from a pool, and nothing
in Cluster API has an opinion about which one a machine gets.

### Inventory order

The pool is walked in the order `kg inventory list` prints it: by name, with the
digits in a name counted as a number rather than as text. `kg-worker-2` comes
before `kg-worker-10`, which is where a plain string order would put it last.

A host is a candidate only if it is `Available`, unclaimed, not marked
`unhealthy`, and matches the machine's host selector - the role label for a
control plane, and whatever a worker pool's `hostSelector` says. The first
candidate wins.

## One replacement, step by step

1. `maxSurge` is 1 on both sides, so a new machine is created before the old one
   is touched. The pool is asked for a host one above the desired count.
2. The new `HostMachine` claims the first free host of its role and runs kubeadm
   on it over SSH.
3. Once its node is Ready, the old machine is deleted.
4. Deleting it resets its host - `kubeadm reset`, the VIP off the interface, the
   cluster's files gone - and the host returns to the pool. It goes back as
   `Pending` and only a fresh probe makes it `Available` again.
5. The next replacement starts from step 1, and by then the host freed in step 4
   is usually the first free one.

This is why a rollout needs one free host per role to start at all, and why
`kg eject --self-manage` is refused without one: a cluster that cannot add a
machine before removing one cannot replace the machines its own controllers are
running on.

## A worked example

Six hosts - `kg-cp-1` to `kg-cp-4` and `kg-worker-1` to `kg-worker-2` - and a
cluster that asks for three control plane replicas and one worker. This is the
fleet the `upgrade` scenario runs on.

| Step | What happens | In use | Free |
| ---- | ------------ | ------ | ---- |
| build | three control plane machines, one at a time | cp-1, cp-2, cp-3 | cp-4 |
| roll 1 | a new machine takes cp-4; the oldest, on cp-1, is removed | cp-2, cp-3, cp-4 | cp-1 |
| roll 2 | a new machine takes cp-1; the machine on cp-2 is removed | cp-1, cp-3, cp-4 | cp-2 |
| roll 3 | a new machine takes cp-2; the machine on cp-3 is removed | cp-1, cp-2, cp-4 | cp-3 |

The rollout walks the pool and leaves the spare one further along than it found
it. The worker pool does the same between `kg-worker-1` and `kg-worker-2`.

Two things make the trace exact here and are worth knowing before reading it as
a promise:

- `KubeadmControlPlane` builds its machines one at a time - it has to, since the
  first runs `kubeadm init` and the rest join it - so control plane machines end
  up on hosts in inventory order and their ages follow that order.
- A `MachineDeployment` creates all of a pool's replicas at once. They race for
  the pool, and the loser of a claim moves to the next host, so with two or more
  replicas the *set* of hosts taken is the first n but which machine got which is
  not fixed. During a rollout only one machine is created at a time, so this does
  not apply to an upgrade.

If a host's reset and probe have not finished by the time the next replacement
starts, the pool hands out the next free host instead and the walk skips a place.
Nothing breaks: the order is still the pool's order, it is just being read at a
different moment.

## Sending a particular machine first

Cluster API takes an annotation that outranks everything above, on both the
control plane and a worker pool:

```console
$ kubectl annotate machine -n lab lab-default-abc12 cluster.x-k8s.io/delete-machine=""
```

The annotated machine is the next one replaced. It is the way to drain a host you
want back - to re-image it, or to take it out of the rack - without waiting for
its turn.

To hold a host out of the pool entirely, mark it in the inventory instead:

```console
$ kubectl patch host -n lab kg-worker-3 --type merge -p '{"spec":{"unhealthy":true}}'
```

An unhealthy host is never handed out. It says nothing about the machine already
on it, which keeps running.

## Watching one go

```console
$ kg inventory list   # the pool, in the order it is handed out
$ kubectl get machines -n lab --sort-by=.metadata.creationTimestamp
$ kubectl get machines -n lab -o wide -w
```

The order the second command prints is the order the machines should be replaced
in, and the third shows them going.

The `scale` and `upgrade` scenarios assert this rather than describe it: the
first names the host it expects back before it asks for the change, and the
second records the order machines are marked for deletion in while the rollout
runs and holds it against the order they were created in. See
[SCENARIOS.md](../test/scenarios/SCENARIOS.md).
