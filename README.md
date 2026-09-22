# kgenesis

Turn a pool of pre-provisioned physical or virtual hosts into a Kubernetes
cluster, from one machine acting as the genesis node.

kgenesis stands up a throwaway Cluster API management cluster locally, uses the
kubeadm bootstrap provider (CABPK) to generate each machine's cloud-init, pushes
that over SSH to a host it claims from the pool, and then hands management to the
cluster it built. The genesis node has no role after that.

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
            |                                             ^
            +------------ clusterctl move ----------------+
                        then kind is deleted
```

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
- SSH as root, by key or password
- No existing kubeadm state (`kg inventory check` flags leftovers)

## Getting started

The CLI installs as `kgenesis` with `kg` as a short alias beside it. The two are
the same binary, and every hint the CLI prints uses whichever name you typed, so
you can paste its suggestions straight back.

```console
$ kg config init            # write a starter kgenesis.yaml
$ $EDITOR kgenesis.yaml     # fill in hosts and the control plane VIP
$ kg config validate        # check it without contacting anything
$ kg inventory check        # connect to every host and report
$ kg init                   # bootstrap cluster + providers + inventory
$ kg cluster create         # stamp out the cluster and wait
$ kg pivot                  # hand over management, drop the genesis node
```

`inventory check` is worth running first. It needs nothing but the config file,
and a host that fails there would otherwise fail much later, part way through a
rollout.

## Configuration

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

## Host key policies

| Policy     | Behaviour                                                        |
| ---------- | ---------------------------------------------------------------- |
| `Strict`   | Requires `publicKey` on each host and rejects anything else       |
| `TOFU`     | Pins the key seen on first contact, rejects changes after that    |
| `Insecure` | Accepts any key. Lab use only                                     |

A mismatch is never retried into: it means the host changed identity, which an
operator has to resolve.

## Commands

| Command                    | What it does                                        |
| -------------------------- | --------------------------------------------------- |
| `config init` / `validate` | Write and check `kgenesis.yaml`                     |
| `inventory check`          | SSH preflight against the config alone              |
| `inventory list`           | The pool as the management cluster sees it          |
| `init`                     | Bootstrap cluster, providers, inventory             |
| `cluster create`           | Apply the cluster; `--dry-run` prints the manifests |
| `cluster status`           | Where the rollout has got to; `--watch` follows     |
| `cluster delete`           | Tear the cluster down and reset its hosts           |
| `cni install`              | Apply the configured CNI manifests                  |
| `kubeconfig`               | Write the target cluster's kubeconfig               |
| `pivot`                    | Move management to the cluster, delete kind         |
| `reset`                    | Delete the bootstrap cluster only                   |

Kubeconfigs live under `~/.kgenesis` rather than `~/.kube`, so bootstrapping
never disturbs the contexts you already have. Override with `--state-dir`.

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
```

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

It needs a raised inotify budget, which the harness checks and fixes:

```console
$ sudo sysctl -w fs.inotify.max_user_instances=8192
```

Without it kube-proxy dies with `too many open files` and nothing in the
cluster can reach the API server.

On Docker Desktop the harness skips the CLI-side `inventory check`, because
macOS cannot route to a docker bridge directly. The controller runs inside the
bootstrap cluster on that same network, so the rest of the test is unaffected.

Run a locally built provider on the genesis node:

```console
$ make docker-build
$ kg init --provider-image ghcr.io/ashon/kgenesis:dev --load-image
```

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
internal/config/     kgenesis.yaml
internal/controller/ the reconcilers
internal/provisioner/ the detached run on a host, and the probe
internal/render/     kgenesis.yaml to Cluster API objects
internal/ssh/        the transport
config/              CRDs, RBAC and the controller Deployment
test/e2e/            the end-to-end harness and its stand-in host image
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
