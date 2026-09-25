<!--
SPDX-FileCopyrightText: 2026 Ashon
SPDX-License-Identifier: MIT
-->

# Development and testing

[Documentation](README.md) · [Project overview](../README.md)

## Build and contribute

```console
$ make help            # list targets
$ make build           # bin/kg
$ make install         # CLI into GOBIN
$ make test            # unit tests
$ make e2e             # container E2E suite
$ make generate        # deepcopy, CRDs, RBAC, embedded provider manifest
$ make verify          # fail when generated files are out of date
$ make docker-build    # build the provider image
$ make release VERSION=v0.1.0   # the release artifacts, into dist/
```

## Tests

Unit tests cover the parts that are easy to get subtly wrong and expensive to
debug on hardware. The cloud-config renderer is driven by CABPK's own generator,
so an upstream template change shows up there rather than on a half-bootstrapped
host, and the SSH client is tested against an in-process SSH server.

## End-to-end scenarios

The shared harness uses SSH to build real kubeadm clusters on container hosts,
Lima VMs or libvirt VMs. See the [scenario catalog](../test/scenarios/SCENARIOS.md)
for prerequisites, assertions, management acceptance IDs and driver settings.

```console
$ make e2e            # Docker host fleet on Linux; VM-only cases are skipped
$ make vm-test        # Lima VM fleet on macOS; all ten scenarios
$ DRIVER=libvirt ./test/scenarios/run.sh   # KVM fleet on Linux
```

The host running the harness must reach the fleet's SSH addresses. Docker
Desktop does not expose its bridge directly to macOS; use Lima for local full
E2E on a Mac. The VM drivers exercise independent kernels and VIP failover.

The harness creates and tears down its test fleet. Use a dedicated environment:
existing VMs or containers with the scenario names can be replaced. `KEEP=1`
retains the fleet after a run; `REUSE=1` reuses it. To resume a later scenario,
keep the same `KG_SCENARIO_WORKDIR` so the configuration, credentials and genesis
state match. Scenarios have state dependencies; see the catalog before selecting
individual cases.

Linux container hosts share kernel limits and swap with their runner. The
workflow loads `br_netfilter` and `overlay`, raises inotify limits and disables
runner swap before starting the tests. Consult the driver preflight if running
outside CI.

Every run saves logs, `results.tsv` and `junit.xml` under `.artifacts/e2e/`, or
the path selected by `KG_SCENARIO_REPORT_DIR`. CI uploads those reports even on
failure. State directories, kubeconfigs and SSH keys are not included in the
report artifact. The logging regression test ensures background task waits can
finish on Linux and scenario failures retain their exit status:

```console
$ bash test/scenarios/logging_test.sh
```

## Developing the provider

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
the image into the cluster's hosts and eligible spares before the controllers move. Without it the
moved controller would wait on a registry that never had the image.

`make generate` rewrites `internal/assets/provider-components.yaml`, the manifest
`kg init` applies. It is embedded in the binary so a genesis node needs no
access to a manifest registry.

## Releases

A tag cuts a release. The tag name is the version, and it goes into three places
that have to agree: the binaries, the container image, and the manifest those
binaries install. `kg version` says which image the binary in your hand will
install, which is the first thing to check when the controller will not start.

```console
$ kg version
kg v0.1.0 (commit 1a2b3c4, darwin/arm64, go1.27.1), built 2026-09-23T05:00:00Z
controller image ghcr.io/ashon/kg:v0.1.0
```

A release publishes a CLI archive per platform (including the README and manuals), `provider-components.yaml` for
reading the manifest without a CLI, and `SHA256SUMS` over both. The controller
image is built for `linux/amd64` and `linux/arm64`, because it runs on whatever
the cluster's own machines are.

`hack/release.sh` builds the same artifacts locally, which is how to see what a
tag would produce before pushing one.



## Repository layout

```
api/v1alpha1/        Host, HostCluster, HostMachine, HostMachineTemplate
cmd/kg/              the CLI (installed as kg)
cmd/manager/         the provider controllers
internal/cli/        command tree
internal/cloudinit/  CABPK cloud-config to bash
internal/config/     the genesis configuration
internal/controller/ the reconcilers
internal/inventory/  the order the host pool is read and handed out in
internal/provisioner/ the detached run on a host, and the probe
internal/render/     the genesis configuration to Cluster API objects
internal/ssh/        the transport
config/              CRDs, RBAC and the controller Deployment
docs/                the manuals a command's help is too small for
test/assets/         the stand-in host image and the vendored CNI
test/scenarios/      the scenarios, and the fleet drivers they run on
```

## CI

| Workflow | What it does |
| -------- | ------------ |
| `CI` | gofmt, vet, unit tests, build, and a check that the generated files are current |
| `Scenarios` | every scenario a container fleet can run, on main pushes and PRs |
| `Scenarios on machines` | every scenario, on KVM virtual machines, relevant PRs/main pushes and nightly |
| `Release` | archives, the multi-arch controller image and checksums, on a `v*` tag |

The generated-files check matters more than it looks: the CRDs, RBAC and the
provider manifest embedded in the CLI are all generated, and a stale copy would
install the wrong thing on a genesis node without anything failing until
`kg init`.

## Licensing

MIT. See [LICENSE](../LICENSE). Source files carry SPDX headers;
`make verify` checks the required headers and generated files.

kg builds on Cluster API and its kubeadm bootstrap and control plane providers,
which are Apache 2.0, and installs cert-manager, a CNI you supply and optionally
kube-vip. They retain their own licenses. The vendored
`test/assets/kindnet.yaml` retains kind's Apache 2.0 header.
