# kgenesis

[![CI](https://github.com/Ashon/kgenesis/actions/workflows/ci.yml/badge.svg)](https://github.com/Ashon/kgenesis/actions/workflows/ci.yml)
[![Scenarios](https://github.com/Ashon/kgenesis/actions/workflows/scenarios.yml/badge.svg)](https://github.com/Ashon/kgenesis/actions/workflows/scenarios.yml)
[![Scenarios on machines](https://github.com/Ashon/kgenesis/actions/workflows/scenarios-machines.yml/badge.svg)](https://github.com/Ashon/kgenesis/actions/workflows/scenarios-machines.yml)
[![Latest release](https://img.shields.io/github/v/release/Ashon/kgenesis?include_prereleases&sort=semver)](https://github.com/Ashon/kgenesis/releases)
[![License](https://img.shields.io/badge/license-MIT-blue)](LICENSE)

Turn a pool of pre-provisioned physical or virtual hosts into a Kubernetes
cluster, from one machine acting as the genesis node.

kgenesis stands up a throwaway Cluster API management cluster locally, uses the
kubeadm bootstrap provider (CABPK) to generate each machine's cloud-init, pushes
that over SSH to a host it claims from the pool, and then lets the finished
cluster go. The genesis node has no role after that, and neither does kgenesis:
what it leaves behind is an ordinary kubeadm cluster.

## What it covers

Every row is a path with a scenario behind it, and every scenario asserts what
the path is supposed to leave behind rather than that it ran. The last column is
where that scenario last passed, which is the answer to whether any of this is
safe to rely on.

| To | Run | Scenario | Verified on |
| -- | --- | -------- | ----------- |
| See whether the hosts are usable at all | `kg inventory check` | `inventory` | containers, machines |
| Build a cluster on prepared hosts | `kg init`, `kg cluster create` | `build` | containers, machines |
| Keep serving when the machine holding the VIP goes | nothing; kube-vip elects | `vip-failover` | machines |
| Grow or shrink a worker pool | edit `workers[].replicas`, `kg cluster create` | `scale` | containers, machines |
| Use the same hosts for another cluster | `kg cluster delete`, `kg cluster create` | `rebuild` | containers, machines |
| Hand the cluster over and walk away | `kg eject` | `release` | containers, machines |
| Run several clusters from one genesis node | a configuration each, `kg clusters` | `multi-cluster` | containers, machines |
| Leave the cluster managing itself | `kg eject --self-manage` | `self-manage` | machines |
| Roll a self-managed cluster to a newer Kubernetes | `kubectl patch` its `spec.version` | `upgrade` | machines |

**containers** is every push: host containers on one docker bridge, which is
enough for everything except an election over ARP. **machines** is nightly: KVM
virtual machines with a kernel and a network stack each, where every scenario
runs. [SCENARIOS.md](test/scenarios/SCENARIOS.md) says what each one asserts,
and why.

What has no scenario behind it, and is not finished:

| Not covered | Where it stands |
| ----------- | --------------- |
| Preparing the hosts | kgenesis bootstraps machines that already have a container runtime, kubeadm, kubelet and kubectl. `kg inventory check` reports what is missing; putting it there is someone else's job, and so is replacing it for an upgrade. |

## Why this shape

Cluster API has a bootstrap problem: to create a cluster you need a cluster. On
clouds the answer is a management cluster someone else already runs. On bare
metal there is nothing to start from, so kgenesis makes one on the spot.

CABPK produces cloud-init, but pre-provisioned hosts have no metadata service to
hand it to them. The usual answers are an agent installed on every host (BYOH) or
full lifecycle management through BMC and PXE (Metal3). kgenesis takes the third
path: it connects over SSH, which needs nothing installed ahead of time and works
the same for a rack of servers and a handful of VMs.

## How it fits together

```
genesis node (kind)                        target cluster (your hosts)
+--------------------------+               +---------------------------------+
| cert-manager             |               |   cp-1     cp-2     cp-3        |
| Cluster API core         |               |                                 |
| CABPK (bootstrap)        | --- SSH --->  |   worker-1 worker-2 ...         |
| KCP (control plane)      |  cloud-init   |                                 |
| kgenesis infra provider  |               |   kube-vip holds the VIP        |
+--------------------------+               +---------------------------------+
            |
            +-- the cluster is released, then kind is deleted
```

What the target cluster gets is a working kubeadm cluster, not a copy of the
genesis node. Releasing it leaves it on its own: the SSH keys stay behind, and
nothing kgenesis installed keeps running on the hosts. See
[Releasing a cluster](#releasing-a-cluster).

The provider adds four resources:

| Kind                  | What it is                                              |
| --------------------- | ------------------------------------------------------- |
| `Host`                | One machine in the pool: address, SSH credentials, state |
| `HostCluster`         | The infrastructure cluster; carries the control plane endpoint |
| `HostMachine`         | A Cluster API Machine bound to a claimed `Host`         |
| `HostMachineTemplate` | What KubeadmControlPlane and MachineDeployment stamp from |

## Requirements

On the genesis node:

- Docker, for the kind bootstrap cluster
- Network reachability to every host on port 22

On every host:

- A Linux distribution with `bash`, `base64`, `install`, `setsid` and `systemctl`
- A container runtime, normally containerd
- `kubeadm`, `kubelet` and `kubectl`, at the minor `cluster.kubernetesVersion`
  asks for. kgenesis does not install them: it bootstraps machines that are
  already provisioned, so the host decides which Kubernetes it can build
- SSH as root, by key or password
- No existing kubeadm state

`kg inventory check` reports all of it, including a kubeadm a minor away from
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

## Getting started

The CLI installs as `kgenesis` with `kg` as a short alias beside it. The two are
the same binary, and every hint the CLI prints uses whichever name you typed, so
you can paste its suggestions straight back.

```console
$ kg config init            # write a starter ~/.kg/config
$ $EDITOR ~/.kg/config      # hosts, the control plane VIP, and a CNI
$ kg config validate        # check it without contacting anything
$ kg inventory check        # connect to every host and report
$ kg init                   # bootstrap cluster + providers + inventory
$ kg cluster create         # stamp out the cluster and wait
$ kg eject                  # let the cluster go, drop the genesis node
```

kgenesis does not bundle a CNI, so `cluster.cni.manifests` has to name one
before the cluster will have a Ready node. `cluster create` says so when it is
missing rather than reporting a cluster that is up and leaving the reason to be
found. See [CNI](#cni).

The configuration is read from `~/.kg/config` unless `--config` or
`KGENESIS_CONFIG` says otherwise, so the commands above work from any directory.
Set `KGENESIS_CONFIG` to point a shell at one fleet among several, the way
`KUBECONFIG` does.

`inventory check` is worth running first. It needs nothing but the config file,
and a host that fails there would otherwise fail much later, part way through a
rollout.

## Configuration

`~/.kg/config`, in YAML:

```yaml
apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig

cluster:
  name: lab
  kubernetesVersion: v1.34.1
  controlPlaneEndpoint:
    host: 10.10.0.100      # a free address on the control plane subnet
    port: 6443
  network:
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12
  virtualIP:
    enabled: true          # kube-vip raises the endpoint on the control plane
  cni:
    manifests:
      - ./cni/cilium.yaml

ssh:
  user: root
  privateKeyPath: ~/.ssh/id_ed25519
  hostKeyPolicy: TOFU      # Strict | TOFU | Insecure

hosts:
  - {name: cp-1, address: 10.10.0.11, role: control-plane}
  - {name: cp-2, address: 10.10.0.12, role: control-plane}
  - {name: cp-3, address: 10.10.0.13, role: control-plane}
  - {name: worker-1, address: 10.10.0.21, role: worker}
  - {name: worker-2, address: 10.10.0.22, role: worker}
```

`kg config validate` reports every problem at once, so a broken inventory
takes one round trip to fix rather than one per host.

Two escape hatches exist for what kgenesis does not model.
`cluster.preKubeadmCommands` and `cluster.postKubeadmCommands` run on every node
around kubeadm, for a vendor agent, storage setup, NIC tuning, or an extra
kubeadm configuration document. `cluster.ignorePreflightErrors` downgrades
kubeadm checks that do not apply to your hardware; every entry is a check nobody
will see fail, so keep the list short.

### Worker pools

Without a `workers` block, every host with role `worker` lands in one pool. Name
pools explicitly to give them different sizes, labels or taints:

```yaml
workers:
  - name: gpu
    replicas: 2
    hostSelector:
      kgenesis.io/role: worker
      accelerator: gpu
    nodeLabels:
      node.kubernetes.io/accelerator: gpu
    nodeTaints:
      - {key: accelerator, value: gpu, effect: NoSchedule}
```

Selectors match labels on `Host` objects, which come from each host's `labels`
in the config plus the `kgenesis.io/role` label kgenesis adds.

### CNI

kgenesis does not bundle a CNI. Bundling one would mean shipping a copy that goes
stale, and deriving a download URL from a provider name breaks as soon as
upstream reorganises its releases. Give it manifests instead, so the version is
yours to pin and an air-gapped install works the same way:

```console
$ helm template cilium cilium/cilium --version 1.16.5 \
    --namespace kube-system > cni/cilium.yaml
```

They are applied at the end of `cluster create --wait`, or on demand with
`kg cni install`. Without a CNI the nodes come up `NotReady`.

### Control plane endpoint

Every node joins through one address. With `virtualIP.enabled`, kgenesis writes a
kube-vip static pod onto the control plane hosts, which raises the VIP on
whichever host holds the lease. Leave it disabled when an external load balancer
already serves the endpoint.

## What happens on a host

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
data is generated per Machine before any host has been claimed. kgenesis knows
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
The address in the inventory is the one kgenesis reaches the host on, so that is
the one the cluster uses.

## Host key policies

| Policy     | Behaviour                                                        |
| ---------- | ---------------------------------------------------------------- |
| `Strict`   | Requires `publicKey` on each host and rejects anything else       |
| `TOFU`     | Pins the key seen on first contact, rejects changes after that    |
| `Insecure` | Accepts any key. Lab use only                                     |

A mismatch is never retried into: it means the host changed identity, which an
operator has to resolve. A machine that was legitimately reinstalled looks
exactly like one being impersonated, so kgenesis will not guess:

```console
$ kg inventory trust kg-cp-1
kg-cp-1: forgot ecdsa-sha2- AAAAE2VjZH...tqs4oY=; the next connection pins what the machine presents.
```

Under `Strict` there is nothing to forget: the key to accept is `publicKey` in
the configuration, and changing it is a deliberate edit rather than a command.

## Commands

| Command                    | What it does                                        |
| -------------------------- | --------------------------------------------------- |
| `config init` / `validate` | Write and check `~/.kg/config`                      |
| `inventory check`          | SSH preflight against the config alone              |
| `inventory list`           | The pool as the management cluster sees it          |
| `inventory trust`          | Accept the host key a machine presents now          |
| `init`                     | Bootstrap cluster, providers, inventory             |
| `cluster create`           | Apply the cluster; `--dry-run` prints the manifests |
| `cluster status`           | Where the rollout has got to; `--watch` follows     |
| `cluster delete`           | Tear the cluster down and reset its hosts           |
| `cni install`              | Apply the configured CNI manifests                  |
| `kubeconfig`               | Write the target cluster's kubeconfig               |
| `clusters`                 | The clusters this genesis node manages              |
| `eject` / `pivot`          | Release one cluster and drop the genesis node       |
| `reset`                    | Delete the bootstrap cluster only                   |

Everything kgenesis keeps lives under `~/.kg`: the configuration, the bootstrap
cluster's kubeconfig, and the workload cluster's. Keeping the kubeconfigs out of
`~/.kube` means bootstrapping never disturbs the contexts you already have.
Override with `--state-dir`.

## Several clusters from one genesis node

A genesis node is not limited to one cluster. Keep a configuration per cluster
and point at it:

```console
$ kg cluster create -c ~/.kg/lab.yaml
$ kg cluster create -c ~/.kg/prod.yaml
$ kg clusters
CLUSTER  NAMESPACE  PHASE        ENDPOINT          HOSTS
lab      lab        Provisioned  10.10.0.100:6443  5/5
prod     prod       Provisioned  10.20.0.100:6443  7/9
```

Each cluster gets its own namespace, named after it unless `cluster.namespace`
says otherwise. That is not tidiness. A machine draws from the host pool in its
own namespace, so one cluster cannot take hosts meant for another, and
`clusterctl` moves a namespace rather than a cluster, so a namespace per cluster
is what makes ejecting one of several possible at all.

`kg eject` releases one cluster and leaves the rest alone. The genesis node is
deleted once nothing is left for it to manage, and kept otherwise.

## Releasing a cluster

`kg eject` hands the cluster's kubeconfig over and stops managing it. The cluster
itself is not touched: it is an ordinary kubeadm cluster and needs nothing from
kgenesis to serve.

What it gives up is Cluster API. Nodes are not added, replaced or upgraded
through kgenesis afterwards, and there is no going back - Cluster API does not
adopt an existing kubeadm cluster. What it gains is that the SSH keys never leave
the genesis node, and nothing kgenesis installed is left running with the power
to reset the hosts underneath it.

A released cluster is left paused on the genesis node rather than deleted, so its
host claims stand and a later cluster cannot take the hosts it is running on.
`kg clusters` shows it as `Released`. When the genesis node is deleted, the
record goes with it.

`--self-manage` moves the Cluster API objects into the cluster instead, which is
what `clusterctl move` means by a pivot.

It is refused unless the cluster could survive managing itself. Cluster API keeps
a cluster healthy by replacing machines, and a self-managed cluster has to do
that to the machines its own controllers run on: `KubeadmControlPlane` adds a
machine before it removes one, and a `MachineDeployment` does the same, so each
needs a host free to add. Below three control plane replicas etcd loses quorum
the moment one goes. A cluster that cannot meet this can still be released, which
asks nothing of it.

It is also not finished. `clusterctl move` carries objects but not their status,
by design: a provider is expected to rebuild status on the next reconcile.
kgenesis does not yet, so the moved provider re-claims hosts it has already
provisioned and runs kubeadm against nodes that have already joined.

## Development

```console
$ make help            # list targets
$ make build           # bin/kgenesis and the bin/kg alias
$ make install         # both into GOBIN
$ make test            # unit tests
$ make e2e             # build a real cluster and pivot it
$ make generate        # deepcopy, CRDs, RBAC, embedded provider manifest
$ make verify          # fail when generated files are out of date
$ make docker-build    # build the provider image
$ make release VERSION=v0.1.0   # the release artifacts, into dist/
```

## Releases

A tag cuts a release. The tag name is the version, and it goes into three places
that have to agree: the binaries, the container image, and the manifest those
binaries install. `kg version` says which image the binary in your hand will
install, which is the first thing to check when the controller will not start.

```console
$ kg version
kgenesis v0.1.0 (commit 1a2b3c4, darwin/arm64, go1.27.1), built 2026-09-23T05:00:00Z
controller image ghcr.io/ashon/kgenesis:v0.1.0
```

A release publishes a CLI archive per platform, `provider-components.yaml` for
reading the manifest without a CLI, and `SHA256SUMS` over both. The controller
image is built for `linux/amd64` and `linux/arm64`, because it runs on whatever
the cluster's own machines are.

`hack/release.sh` builds the same artifacts locally, which is how to see what a
tag would produce before pushing one.

### Tests

Unit tests cover the parts that are easy to get subtly wrong and expensive to
debug on hardware. The cloud-config renderer is driven by CABPK's own generator,
so an upstream template change shows up there rather than on a half-bootstrapped
host, and the SSH client is tested against an in-process SSH server.

`make e2e` builds a real cluster. kgenesis reaches hosts over SSH and nothing
else, so containers running sshd stand in for machines; they are built from
kindest/node, which already carries systemd, containerd, kubeadm, kubelet and
the control plane images, so kubeadm genuinely runs and the test needs no
network once the images are local. It covers claiming hosts, rendering and
pushing cloud-config, kubeadm init and join, node registration by provider ID,
the CNI install and the pivot. It does not cover real hardware, firmware
variation, VIP failover or network partitions.

Two things it needs from the machine running it, because a container standing in
for a host shares them with everything else:

```console
$ sudo sysctl -w fs.inotify.max_user_instances=8192   # the harness does this
$ sudo swapoff -a                                     # you have to do this
```

A bootstrap cluster plus one systemd container per host exhausts the default
inotify budget, and kube-proxy then dies with `too many open files` while
nothing in the cluster can reach the API server. And `/proc/swaps` is not
namespaced, so the containers see the machine's swap and kubelet refuses to
start; kgenesis cannot turn that off from inside, because the swapfile is not in
their mount namespace. On a real host it is.

On Docker Desktop the harness skips the CLI-side `inventory check`, because
macOS cannot route to a docker bridge directly. The controller runs inside the
bootstrap cluster on that same network, so the rest of the test is unaffected.

Run a locally built provider on the genesis node:

```console
$ make docker-build
$ kg init
```

`kg init` carries the controller image into the bootstrap cluster whenever the
local Docker daemon already has it, so a freshly built provider needs no
registry. `--provider-image` selects a different one, and `--load-image` forces
the same path when the daemon cannot be asked.

`kg eject --self-manage` does the same for the cluster it hands over, importing
the image into each host's containerd before the controllers move. Without it the
moved controller would wait on a registry that never had the image.

`make generate` rewrites `internal/assets/provider-components.yaml`, the manifest
`kg init` applies. It is embedded in the binary so a genesis node needs no
access to a manifest registry.

## Layout

```
api/v1alpha1/        Host, HostCluster, HostMachine, HostMachineTemplate
cmd/kgenesis/        the CLI (installed as kgenesis, aliased to kg)
cmd/manager/         the provider controllers
internal/cli/        command tree
internal/cloudinit/  CABPK cloud-config to bash
internal/config/     the genesis configuration
internal/controller/ the reconcilers
internal/provisioner/ the detached run on a host, and the probe
internal/render/     the genesis configuration to Cluster API objects
internal/ssh/        the transport
config/              CRDs, RBAC and the controller Deployment
test/assets/         the stand-in host image and the vendored CNI
test/scenarios/      the scenarios, and the fleet drivers they run on
```

## CI

| Workflow | What it does |
| -------- | ------------ |
| `CI` | gofmt, vet, unit tests, build, and a check that the generated files are current |
| `E2E` | builds a cluster from container hosts and pivots it, on every push and weekly |

The generated-files check matters more than it looks: the CRDs, RBAC and the
provider manifest embedded in the CLI are all generated, and a stale copy would
install the wrong thing on a genesis node without anything failing until
`kg init`.

## License

MIT. See [LICENSE](LICENSE).

Every file kgenesis owns carries an SPDX header, in the form the REUSE
specification defines, so the licence is machine readable rather than a prose
notice somebody has to interpret. `make verify` fails when one is missing.

kgenesis builds on Cluster API and its kubeadm bootstrap and control plane
providers, which are Apache 2.0, and it installs cert-manager, a CNI you supply
and optionally kube-vip into the clusters it creates. Those keep their own
licences; nothing here relicenses them. One file is vendored rather than
referenced: `test/assets/kindnet.yaml` comes from kind and carries kind's Apache
2.0 header, not this project's.
