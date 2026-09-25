<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Configuration

[Documentation](README.md) · [Project overview](../README.md)

`~/.kg/config`, in YAML. The command is `kg`, but the names Kubernetes stores -
the API group, the labels, the provider ID prefix, the `kgenesis-system`
namespace and the paths on the hosts - still say kgenesis. They are what a
running cluster reads, so they stay put: a cluster built by any release keeps
working, and can still take a newer controller.

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

Two escape hatches exist for what kg does not model.
`cluster.preKubeadmCommands` and `cluster.postKubeadmCommands` run on every node
around kubeadm, for a vendor agent, storage setup, NIC tuning, or an extra
kubeadm configuration document. `cluster.ignorePreflightErrors` downgrades
kubeadm checks that do not apply to your hardware; every entry is a check nobody
will see fail, so keep the list short.

## Worker pools

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
in the config plus the `kgenesis.io/role` label kg adds.

## CNI

kg does not bundle a CNI. Bundling one would mean shipping a copy that goes
stale, and deriving a download URL from a provider name breaks as soon as
upstream reorganises its releases. Give it manifests instead, so the version is
yours to pin and an air-gapped install works the same way:

```console
$ CILIUM_VERSION='<version-compatible-with-your-cluster>'
$ helm template cilium cilium/cilium --version "$CILIUM_VERSION" \
    --namespace kube-system > cni/cilium.yaml
```

Choose a CNI version and values that support your Kubernetes version and network.
The command above illustrates manifest rendering, not a complete Cilium setup.

They are applied at the end of `cluster create --wait`, or on demand with
`kg cni install`. Without a CNI the nodes come up `NotReady`.

## Control plane endpoint

Every node joins through one address. With `virtualIP.enabled`, kg writes a
kube-vip static pod onto the control plane hosts, which raises the VIP on
whichever host holds the lease. Leave it disabled when an external load balancer
already serves the endpoint.

## SSH host key policies

| Policy     | Behaviour                                                        |
| ---------- | ---------------------------------------------------------------- |
| `Strict`   | Requires `publicKey` on each host and rejects anything else       |
| `TOFU`     | Pins the key seen on first contact, rejects changes after that    |
| `Insecure` | Accepts any key. Lab use only                                     |

A mismatch is never retried into: it means the host changed identity, which an
operator has to resolve. A machine that was legitimately reinstalled looks
exactly like one being impersonated, so kg will not guess:

```console
$ kg inventory trust kg-cp-1
kg-cp-1: forgot ecdsa-sha2- AAAAE2VjZH...tqs4oY=; the next connection pins what the machine presents.
```

Under `Strict` there is nothing to forget: the key to accept is `publicKey` in
the configuration, and changing it is a deliberate edit rather than a command.
