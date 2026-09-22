#!/usr/bin/env bash
# End-to-end test: build a real Kubernetes cluster with kgenesis and take it
# through a pivot.
#
# kgenesis reaches hosts over SSH and nothing else, so containers running sshd
# stand in for pre-provisioned machines. They are built from kindest/node, which
# already carries systemd, containerd, kubeadm, kubelet and the control plane
# images, so kubeadm genuinely runs and the test needs no network once the
# images are local.
#
# What this does exercise: claiming hosts from the pool, rendering CABPK's
# cloud-config, pushing and running it over SSH, kubeadm init and join, node
# registration by provider ID, CNI install, and clusterctl move.
#
# What it does not: real hardware, BIOS and firmware variation, multi-control
# plane VIP failover, or anything about network partitions.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.33.1}"
K8S_VERSION="${K8S_VERSION:-v1.33.1}"
CONTROL_PLANE_COUNT="${CONTROL_PLANE_COUNT:-1}"
WORKER_COUNT="${WORKER_COUNT:-1}"
PROVIDER_IMAGE="${PROVIDER_IMAGE:-ghcr.io/ashon/kgenesis:e2e}"
# kind puts its nodes on this network, and the hosts have to share it so the
# controller running inside the bootstrap cluster can reach them.
NETWORK="${NETWORK:-kind}"
HOST_IMAGE="kgenesis-e2e-host:${K8S_VERSION}"
KEEP="${KEEP:-0}"
TIMEOUT="${TIMEOUT:-30m}"

WORKDIR="$(mktemp -d)"
readonly WORKDIR
readonly KEY="${WORKDIR}/id_ed25519"
readonly CONFIG="${WORKDIR}/kgenesis.yaml"
readonly STATE="${WORKDIR}/state"
readonly KG="${ROOT}/bin/kgenesis"

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
fail() { printf '\n\033[31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

host_names() {
  local i
  for ((i = 1; i <= CONTROL_PLANE_COUNT; i++)); do echo "e2e-cp-${i}"; done
  for ((i = 1; i <= WORKER_COUNT; i++)); do echo "e2e-worker-${i}"; done
}

collect_diagnostics() {
  log "Diagnostics"

  if [[ -f "${STATE}/bootstrap.kubeconfig" ]]; then
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl get cluster,machines,hosts,hostmachines -A 2>&1 | head -40 || true
    echo
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl -n kgenesis-system logs deploy/kgenesis-controller-manager --tail=60 2>&1 | tail -60 || true
  fi

  # Once the control plane answers, the interesting failures move into the
  # cluster being built. Nothing else in this test looks there.
  if [[ -f "${STATE}/e2e.kubeconfig" ]]; then
    echo
    info "--- workload cluster: nodes"
    KUBECONFIG="${STATE}/e2e.kubeconfig" kubectl get nodes -o wide 2>&1 | sed 's/^/    /' || true

    echo
    info "--- workload cluster: why the nodes are not ready"
    KUBECONFIG="${STATE}/e2e.kubeconfig" kubectl get nodes \
      -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .status.conditions[*]}  {.type}={.status} {.reason}: {.message}{"\n"}{end}{end}' \
      2>&1 | sed 's/^/    /' || true

    echo
    info "--- workload cluster: kube-system"
    KUBECONFIG="${STATE}/e2e.kubeconfig" kubectl -n kube-system get pods -o wide 2>&1 | sed 's/^/    /' || true

    echo
    info "--- workload cluster: CNI"
    KUBECONFIG="${STATE}/e2e.kubeconfig" kubectl -n kube-system describe daemonset kindnet 2>&1 |
      sed -n '/Events:/,$p' | sed 's/^/    /' || true
    KUBECONFIG="${STATE}/e2e.kubeconfig" kubectl -n kube-system logs daemonset/kindnet --tail=40 2>&1 |
      sed 's/^/    /' || true

    echo
    info "--- workload cluster: recent events"
    KUBECONFIG="${STATE}/e2e.kubeconfig" kubectl get events -A \
      --sort-by=.lastTimestamp 2>&1 | tail -25 | sed 's/^/    /' || true
  fi

  # Everything below has to be collected here, before cleanup: the containers
  # are gone by the time any later CI step could look at them, and a kubeadm
  # failure is almost always explained by the kubelet rather than by kubeadm.
  local host
  for host in $(host_names); do
    docker inspect "${host}" >/dev/null 2>&1 || continue

    echo
    info "--- ${host}: bootstrap log"
    docker exec "${host}" tail -n 60 /var/log/kgenesis-bootstrap.log 2>&1 | sed 's/^/    /' || true

    echo
    info "--- ${host}: kubelet"
    docker exec "${host}" systemctl status kubelet --no-pager 2>&1 | sed 's/^/    /' || true
    docker exec "${host}" journalctl -u kubelet --no-pager -n 120 2>&1 | sed 's/^/    /' || true

    echo
    info "--- ${host}: containerd"
    docker exec "${host}" journalctl -u containerd --no-pager -n 40 2>&1 | sed 's/^/    /' || true

    echo
    info "--- ${host}: containers"
    docker exec "${host}" crictl ps -a 2>&1 | sed 's/^/    /' || true

    echo
    info "--- ${host}: CNI on disk"
    docker exec "${host}" sh -c 'ls -l /etc/cni/net.d 2>&1; ls /opt/cni/bin 2>&1 | head -20' 2>&1 |
      sed 's/^/    /' || true

    echo
    info "--- ${host}: cgroup and kernel facts"
    docker exec "${host}" sh -c '
      echo "cgroup version: $([ -f /sys/fs/cgroup/cgroup.controllers ] && echo v2 || echo v1)"
      echo "controllers: $(cat /sys/fs/cgroup/cgroup.controllers 2>/dev/null)"
      echo "/dev/kmsg: $(ls -l /dev/kmsg 2>&1)"
      echo "swap: $(swapon --show 2>/dev/null | tail -n +2 | wc -l) entries"
      echo "kubelet cgroup: $(ls -d /sys/fs/cgroup/kubelet 2>&1)"
    ' 2>&1 | sed 's/^/    /' || true
  done
}

cleanup() {
  local status=$?
  if ((status != 0)); then
    collect_diagnostics || true
  fi

  if [[ "${KEEP}" == "1" ]]; then
    log "KEEP=1, leaving everything in place"
    info "config:     ${CONFIG}"
    info "kubeconfig: ${STATE}/bootstrap.kubeconfig"
    return
  fi

  log "Cleaning up"
  "${KG}" reset --name kgenesis-bootstrap --state-dir "${STATE}" --yes >/dev/null 2>&1 || true
  kind delete cluster --name kgenesis-bootstrap >/dev/null 2>&1 || true

  local host
  for host in $(host_names); do
    docker rm -f "${host}" >/dev/null 2>&1 || true
  done
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------

# kubelet, containerd and systemd all watch files, and this test runs a
# bootstrap cluster plus one systemd container per host. The kernel default of
# 128 inotify instances is not enough for that, and the way it fails is opaque:
# kube-proxy dies with "too many open files", pods lose their route to the API
# server, and cert-manager simply never becomes ready. Checking here turns ten
# wasted minutes into one line.
readonly MIN_INOTIFY_INSTANCES=512

read_inotify_instances() {
  docker run --rm --network host --entrypoint sh "${NODE_IMAGE}" \
    -c 'cat /proc/sys/fs/inotify/max_user_instances' 2>/dev/null | tr -d '[:space:]'
}

log "Checking kernel limits"
instances="$(read_inotify_instances)"
info "fs.inotify.max_user_instances = ${instances:-unknown}"

if [[ -z "${instances}" ]] || ((instances < MIN_INOTIFY_INSTANCES)); then
  info "raising it to 8192"
  docker run --rm --privileged --network host --entrypoint sh "${NODE_IMAGE}" \
    -c 'sysctl -w fs.inotify.max_user_instances=8192 fs.inotify.max_user_watches=1048576' >/dev/null 2>&1 || true

  instances="$(read_inotify_instances)"
  if [[ -z "${instances}" ]] || ((instances < MIN_INOTIFY_INSTANCES)); then
    fail "fs.inotify.max_user_instances is ${instances:-unknown}, below ${MIN_INOTIFY_INSTANCES}.
    On Linux:          sudo sysctl -w fs.inotify.max_user_instances=8192
    On Docker Desktop: docker run --rm --privileged --network host ${NODE_IMAGE} \\
                         sysctl -w fs.inotify.max_user_instances=8192"
  fi
  info "now ${instances}"
fi

log "Building the CLI and the provider image"
make -C "${ROOT}" build >/dev/null
make -C "${ROOT}" docker-build IMAGE_TAG=e2e IMAGE="${PROVIDER_IMAGE%:*}" >/dev/null
info "$(${KG} version)"

log "Building the host image from ${NODE_IMAGE}"
ssh-keygen -t ed25519 -N '' -f "${KEY}" -q
docker build -q \
  --build-arg "NODE_IMAGE=${NODE_IMAGE}" \
  --build-arg "AUTHORIZED_KEY=$(cat "${KEY}.pub")" \
  -t "${HOST_IMAGE}" "${ROOT}/test/e2e/host" >/dev/null
info "${HOST_IMAGE}"

log "Starting ${CONTROL_PLANE_COUNT} control plane and ${WORKER_COUNT} worker host(s)"
docker network create "${NETWORK}" >/dev/null 2>&1 || true

for host in $(host_names); do
  docker rm -f "${host}" >/dev/null 2>&1 || true
  # These are the flags kind runs its own nodes with. /lib/modules lets the
  # bootstrap script load br_netfilter, exactly as it would on a real host.
  # These are the flags kind runs its own nodes with, and they are not
  # interchangeable: --tty is what the entrypoint logs through, --init=false
  # keeps the entrypoint as PID 1 instead of docker-init, and /lib/modules is
  # what lets the bootstrap script load br_netfilter as it would on a real host.
  docker run --detach --tty \
    --name "${host}" --hostname "${host}" --network "${NETWORK}" \
    --privileged \
    --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
    --cgroupns=private --init=false \
    --tmpfs /tmp --tmpfs /run --volume /var \
    --volume /lib/modules:/lib/modules:ro \
    --restart=on-failure:1 \
    "${HOST_IMAGE}" >/dev/null
done

log "Waiting for sshd on every host"
for host in $(host_names); do
  for attempt in {1..60}; do
    if docker exec "${host}" systemctl is-active ssh >/dev/null 2>&1; then
      info "${host} $(docker inspect -f "{{.NetworkSettings.Networks.${NETWORK}.IPAddress}}" "${host}")"
      break
    fi
    ((attempt == 60)) && fail "sshd never came up on ${host}"
    sleep 2
  done
done

host_ip() {
  docker inspect -f "{{.NetworkSettings.Networks.${NETWORK}.IPAddress}}" "$1"
}

# After the pivot the provider runs in the workload cluster, whose nodes are
# these containers. Their containerd has never seen an image built on this
# machine, and the tag is not published anywhere, so it has to be imported the
# same way kind loads images into its own nodes.
log "Loading the provider image into the hosts"
docker save "${PROVIDER_IMAGE}" -o "${WORKDIR}/provider.tar"
for host in $(host_names); do
  # Piped rather than copied in: /tmp inside these containers is a tmpfs, which
  # docker cp does not write through.
  docker exec -i "${host}" ctr --namespace k8s.io images import - \
    < "${WORKDIR}/provider.tar" >/dev/null
  info "${host}"
done

# Whether this machine can open a TCP connection to a container on the docker
# bridge. On Linux it can, so the test runs end to end. On Docker Desktop the
# bridge lives inside a VM and it cannot, which rules out the steps that talk to
# the workload cluster: its API server only listens on a bridge address.
can_reach() {
  local ip="$1" port="$2" waited=0

  (exec 3<>"/dev/tcp/${ip}/${port}") >/dev/null 2>&1 &
  local probe=$!

  # There is no portable connect timeout here: macOS nc ignores -w when the
  # packets are simply dropped, and `timeout` is not installed. Watch the probe
  # rather than trusting a flag.
  while kill -0 "${probe}" 2>/dev/null; do
    if [[ "${waited}" -ge 50 ]]; then
      kill -9 "${probe}" 2>/dev/null || true
      wait "${probe}" 2>/dev/null || true
      return 1
    fi
    sleep 0.1
    waited=$((waited + 1))
  done

  wait "${probe}"
}

log "Writing the configuration"
# With a single control plane there is no VIP to elect, so the endpoint is that
# host's own address. Multi-control-plane needs kube-vip, which is out of scope
# for a test running on one docker bridge.
CP_ENDPOINT="$(host_ip e2e-cp-1)"

ROUTABLE=0
if can_reach "${CP_ENDPOINT}" 22; then
  ROUTABLE=1
fi
readonly ROUTABLE

# kgenesis installs the CNI from wherever the CLI runs, so it is only configured
# when this machine can reach the cluster being built.
if [[ "${ROUTABLE}" == "1" ]]; then
  # kindnet has to be told the real API server address; see the note in the
  # vendored manifest.
  CNI_MANIFEST="${WORKDIR}/kindnet.yaml"
  sed "s#__CONTROL_PLANE_ENDPOINT__#${CP_ENDPOINT}:6443#" \
    "${ROOT}/test/e2e/kindnet.yaml" > "${CNI_MANIFEST}"

  # An unsubstituted placeholder would install a CNI that cannot reach the API
  # server, and the only symptom would be nodes that never turn Ready.
  if grep -q '__CONTROL_PLANE_ENDPOINT__' "${CNI_MANIFEST}"; then
    fail "the CNI manifest still has an unsubstituted placeholder"
  fi

  CNI_BLOCK="  cni:
    manifests:
      - ${CNI_MANIFEST}"
else
  CNI_BLOCK="  cni:
    manifests: []"
fi

{
  cat <<EOF
apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig

cluster:
  name: e2e
  kubernetesVersion: ${K8S_VERSION}
  controlPlaneEndpoint:
    host: ${CP_ENDPOINT}
    port: 6443
  controlPlaneReplicas: ${CONTROL_PLANE_COUNT}
  network:
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12
  virtualIP:
    enabled: false
  # The hosts are containers, so kubeadm cannot read the kernel config to verify
  # it. Nothing else here is relaxed.
  ignorePreflightErrors:
    - SystemVerification
${CNI_BLOCK}

ssh:
  user: root
  port: 22
  privateKeyPath: ${KEY}
  hostKeyPolicy: TOFU

hosts:
EOF

  for ((i = 1; i <= CONTROL_PLANE_COUNT; i++)); do
    echo "  - {name: e2e-cp-${i}, address: $(host_ip "e2e-cp-${i}"), role: control-plane}"
  done
  for ((i = 1; i <= WORKER_COUNT; i++)); do
    echo "  - {name: e2e-worker-${i}, address: $(host_ip "e2e-worker-${i}"), role: worker}"
  done
} > "${CONFIG}"

"${KG}" config validate -c "${CONFIG}"

if [[ "${ROUTABLE}" == "1" ]]; then
  log "Preflight against the hosts"
  "${KG}" inventory check -c "${CONFIG}"
else
  log "Skipping the CLI preflight"
  info "This machine cannot route to the ${NETWORK} bridge. The controller"
  info "reaches the hosts from inside the bootstrap cluster, so the same"
  info "preflight still runs there, against the Host objects."
fi

log "Bringing up the genesis node"
"${KG}" init -c "${CONFIG}" --state-dir "${STATE}" \
  --provider-image "${PROVIDER_IMAGE}" --load-image --timeout "${TIMEOUT}"

log "Waiting for every host to be probed Available"
export KUBECONFIG="${STATE}/bootstrap.kubeconfig"
expected="$((CONTROL_PLANE_COUNT + WORKER_COUNT))"
for attempt in {1..60}; do
  available="$(kubectl get hosts -o jsonpath='{range .items[*]}{.status.phase}{"\n"}{end}' | grep -c '^Available$' || true)"
  info "${available}/${expected} available"
  [[ "${available}" == "${expected}" ]] && break
  ((attempt == 60)) && { kubectl get hosts; fail "hosts never became Available"; }
  sleep 5
done

log "Creating the cluster"
"${KG}" cluster create -c "${CONFIG}" --state-dir "${STATE}" --wait --timeout "${TIMEOUT}"

if [[ "${ROUTABLE}" != "1" ]]; then
  log "Stopping before the workload cluster checks"
  info "Every machine reached Running, so host claiming, the cloud-config push,"
  info "kubeadm init and join, and node registration all worked."
  info ""
  info "What did not run, because this machine cannot reach the workload API"
  info "server on the ${NETWORK} bridge: the CNI install, the node readiness and"
  info "provider ID assertions, and the pivot. CI runs on Linux and covers them."
  exit 0
fi

log "Checking the workload cluster"
"${KG}" kubeconfig -c "${CONFIG}" --state-dir "${STATE}"
WORKLOAD="${STATE}/e2e.kubeconfig"

KUBECONFIG="${WORKLOAD}" kubectl wait --for=condition=Ready nodes --all --timeout=5m
KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide

# Every Node must carry the provider ID kgenesis assigned, or Cluster API cannot
# pair it with its Machine. This is the part a unit test cannot reach.
log "Verifying provider IDs reached the nodes"
missing=0
while read -r node provider_id; do
  if [[ "${provider_id}" != kgenesis://* ]]; then
    echo "    ${node}: provider ID is ${provider_id:-<empty>}"
    missing=1
  else
    info "${node} ${provider_id}"
  fi
done < <(KUBECONFIG="${WORKLOAD}" kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.name} {.spec.providerID}{"\n"}{end}')
((missing == 0)) || fail "a node is missing its kgenesis provider ID"

nodes="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes --no-headers | wc -l | tr -d ' ')"
[[ "${nodes}" == "${expected}" ]] || fail "expected ${expected} nodes, found ${nodes}"

log "Pivoting"
"${KG}" pivot -c "${CONFIG}" --state-dir "${STATE}" \
  --provider-image "${PROVIDER_IMAGE}" --timeout "${TIMEOUT}"

log "Verifying the cluster now manages itself"
KUBECONFIG="${WORKLOAD}" kubectl get cluster,machines -A
KUBECONFIG="${WORKLOAD}" kubectl -n kgenesis-system rollout status deploy/kgenesis-controller-manager --timeout=5m

kind get clusters 2>/dev/null | grep -q '^kgenesis-bootstrap$' \
  && fail "the bootstrap cluster is still running after the pivot"

log "End to end test passed"
