#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# End-to-end test on real virtual machines: three control planes and two
# workers, built from this Mac acting as the genesis node.
#
# The container test covers the same path far more cheaply and is what CI runs.
# This one exists for what containers cannot show, because they share a kernel
# and a network stack with everything else:
#
#   - etcd quorum across three separate machines
#   - kube-vip holding the control plane VIP, and moving it when a node goes
#   - a real kubelet on a real kernel, with the distribution's own packages
#
# The Mac is the genesis node, which is how anyone would actually use kgenesis
# here. That needs a network which is both reachable from the Mac and able to
# carry traffic between the VMs. vzNAT gives the first without the second and
# user-v2 the second without the first, so on macOS this means socket_vmnet:
#
#   brew install socket_vmnet
#   sudo install -d -o root -g wheel -m 755 /opt/socket_vmnet/bin
#   sudo install -o root -g wheel -m 755 \
#     "$(brew --prefix)/opt/socket_vmnet/bin/socket_vmnet" /opt/socket_vmnet/bin/
#   limactl sudoers > /tmp/lima-sudoers
#   sudo install -o root -g wheel -m 644 /tmp/lima-sudoers /etc/sudoers.d/lima
#
# Not run in CI: it needs a Mac with Virtualization.framework, several gigabytes
# free, and twenty minutes.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

K8S_MINOR="${K8S_MINOR:-1.33}"
CONTROL_PLANE_COUNT="${CONTROL_PLANE_COUNT:-3}"
WORKER_COUNT="${WORKER_COUNT:-2}"
PROVIDER_IMAGE="${PROVIDER_IMAGE:-kgenesis:vm}"
TIMEOUT="${TIMEOUT:-40m}"
KEEP="${KEEP:-0}"
# REUSE=1 keeps whatever VMs are already there and skips creating and
# provisioning them. Provisioning is most of the wall clock, so iterating on the
# later steps is otherwise painfully slow.
REUSE="${REUSE:-0}"

# The segment socket_vmnet serves. vmnet's DHCP does not answer on every macOS
# host and the switch works regardless, so addresses are assigned rather than
# leased. The VIP sits well above the hosts.
readonly SUBNET="192.168.105"
readonly VIP="${SUBNET}.200"
readonly NETWORK_IFACE="lima0"

WORKDIR="$(mktemp -d)"
readonly WORKDIR
readonly KEY="${WORKDIR}/id_ed25519"
readonly CONFIG="${WORKDIR}/kgenesis.yaml"
readonly STATE="${WORKDIR}/state"
readonly KG="${ROOT}/bin/kgenesis"
readonly WORKLOAD="${STATE}/lab.kubeconfig"

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
fail() { printf '\n\033[31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

host_names() {
  local i
  for ((i = 1; i <= CONTROL_PLANE_COUNT; i++)); do echo "kg-cp-${i}"; done
  for ((i = 1; i <= WORKER_COUNT; i++)); do echo "kg-worker-${i}"; done
}

# Control planes take .11 upwards, workers continue after them.
host_ip() {
  local name="$1" index
  case "${name}" in
    kg-cp-*)     index="${name##*-}"; echo "${SUBNET}.$((10 + index))" ;;
    kg-worker-*) index="${name##*-}"; echo "${SUBNET}.$((10 + CONTROL_PLANE_COUNT + index))" ;;
    *)           fail "unknown host ${name}" ;;
  esac
}

vm_exists() {
  local existing="$1" name="$2"
  # Compared in the shell rather than with grep: whether `grep -qx` matches on a
  # pipe turned out to depend on which grep is on PATH.
  case $'\n'"${existing}"$'\n' in
    *$'\n'"${name}"$'\n'*) return 0 ;;
    *) return 1 ;;
  esac
}

vip_holder() {
  local host
  for host in $(host_names | grep cp); do
    if limactl shell "${host}" -- ip -4 -o addr show "${NETWORK_IFACE}" 2>/dev/null |
        grep -q "${VIP}"; then
      echo "${host}"
      return 0
    fi
  done
  return 1
}

collect_diagnostics() {
  log "Diagnostics"

  if [[ -f "${STATE}/bootstrap.kubeconfig" ]]; then
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl get cluster,machines,hosts,hostmachines -A 2>&1 |
      head -40 | sed 's/^/    /' || true
    echo
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl -n kgenesis-system \
      logs deploy/kgenesis-controller-manager --tail=40 2>&1 | tail -40 | sed 's/^/    /' || true
  fi

  if [[ -f "${WORKLOAD}" ]]; then
    echo
    info "--- workload cluster"
    KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide 2>&1 | sed 's/^/    /' || true
    KUBECONFIG="${WORKLOAD}" kubectl -n kube-system get pods -o wide 2>&1 | sed 's/^/    /' || true
  fi

  local existing host
  existing="$(limactl list --format '{{.Name}}' 2>/dev/null)"
  for host in $(host_names); do
    vm_exists "${existing}" "${host}" || continue
    echo
    info "--- ${host}: bootstrap log"
    limactl shell "${host}" -- sudo tail -n 40 /var/log/kgenesis-bootstrap.log 2>&1 | sed 's/^/    /' || true
    echo
    info "--- ${host}: kubelet"
    limactl shell "${host}" -- sudo journalctl -u kubelet --no-pager -n 40 2>&1 | sed 's/^/    /' || true
  done
}

cleanup() {
  local status=$?
  ((status != 0)) && collect_diagnostics || true

  if [[ "${KEEP}" == "1" || "${REUSE}" == "1" ]]; then
    log "Leaving the VMs running"
    info "config:     ${CONFIG}"
    info "kubeconfig: ${STATE}/bootstrap.kubeconfig"
    return
  fi

  log "Cleaning up"
  "${KG}" reset --name kgenesis-bootstrap --state-dir "${STATE}" --yes >/dev/null 2>&1 || true
  kind delete cluster --name kgenesis-bootstrap >/dev/null 2>&1 || true

  local host
  for host in $(host_names); do
    limactl delete -f "${host}" >/dev/null 2>&1 || true
  done
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------

command -v limactl >/dev/null || fail "limactl is not installed (brew install lima)"
[[ -x /opt/socket_vmnet/bin/socket_vmnet ]] ||
  fail "socket_vmnet is not installed; see the note at the top of this script"

ssh-keygen -t ed25519 -N '' -f "${KEY}" -q
readonly PUBKEY="$(cat "${KEY}.pub")"

existing="$(limactl list --format '{{.Name}}' 2>/dev/null)"

if [[ "${REUSE}" == "1" ]]; then
  log "Reusing the running VMs"
  for host in $(host_names); do
    vm_exists "${existing}" "${host}" ||
      fail "REUSE=1 but ${host} is not among the instances: $(echo "${existing}" | tr '\n' ' ')"
    limactl start "${host}" >/dev/null 2>&1 || true
  done

  # The key is regenerated every run, so it still has to be installed.
  for host in $(host_names); do
    limactl shell "${host}" -- sudo sh -c \
      "install -d -m 0700 /root/.ssh && printf '%s\n' '${PUBKEY}' > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys"
  done
  info "$(host_names | wc -l | tr -d ' ') host(s)"
else
  log "Creating ${CONTROL_PLANE_COUNT} control plane and ${WORKER_COUNT} worker VM(s)"
  for host in $(host_names); do
    limactl delete -f "${host}" >/dev/null 2>&1 || true
    limactl create --name "${host}" --tty=false "${ROOT}/test/vm/host.yaml" >/dev/null 2>&1
  done

  # Started in parallel: each one spends most of its time in apt.
  for host in $(host_names); do
    limactl start "${host}" >/dev/null 2>&1 &
  done
  wait
  info "$(host_names | wc -l | tr -d ' ') VM(s) booted"

  log "Provisioning the hosts"
  for host in $(host_names); do
    (
      limactl shell "${host}" -- sudo bash -s -- \
        "$(host_ip "${host}")" "${K8S_MINOR}" "${PUBKEY}" \
        < "${ROOT}/test/vm/provision-host.sh" 2>&1 | tail -1 | sed 's/^/    /'
    ) &
  done
  wait
fi

log "Checking that this Mac can reach every host"
for host in $(host_names); do
  ip="$(host_ip "${host}")"
  nc -z -G 5 "${ip}" 22 >/dev/null 2>&1 || fail "cannot reach ${host} at ${ip}:22"
  info "${host} ${ip}"
done

# macOS 15 gates access to the local network per application, and reports a
# denial as "no route to host" rather than a permission error. Apple's own
# binaries are exempt, so nc above succeeds while kgenesis cannot connect at
# all. Catching it here turns a baffling preflight failure into one sentence.
first_host_ip="$(host_ip kg-cp-1)"
cat > "${WORKDIR}/localnet.go" <<'GO'
package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if _, err := net.DialTimeout("tcp", os.Args[1], 5*time.Second); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
GO
if ! go run "${WORKDIR}/localnet.go" "${first_host_ip}:22" >/dev/null 2>&1; then
  fail "nc reaches ${first_host_ip}:22 but a freshly built binary cannot.

    macOS 15 gates local network access per application and reports the denial
    as \"no route to host\". Apple's own tools are exempt, which is why nc works
    and kgenesis does not.

    Open System Settings -> Privacy & Security -> Local Network and enable the
    terminal you are running this from, then run this again."
fi
info "local network access is granted"

# Something answering on the VIP is expected when reusing a cluster that already
# raised it. A collision with anything else would make the failover check
# meaningless, and the difference is whether one of these control planes holds it.
if nc -z -G 3 "${VIP}" 6443 >/dev/null 2>&1; then
  if holder="$(vip_holder)"; then
    info "${VIP} is already held by ${holder}, from an earlier run"
  else
    fail "${VIP} answers but no control plane here holds it; pick a different VIP"
  fi
fi

log "Building kgenesis and the provider image"
make -C "${ROOT}" build >/dev/null
make -C "${ROOT}" docker-build IMAGE="${PROVIDER_IMAGE%:*}" IMAGE_TAG="${PROVIDER_IMAGE##*:}" >/dev/null
info "$(${KG} version)"

K8S_VERSION="$(limactl shell kg-cp-1 -- kubeadm version -o short 2>/dev/null | tr -d '[:space:]')"
[[ "${K8S_VERSION}" == v${K8S_MINOR}.* ]] ||
  fail "the hosts report kubeadm ${K8S_VERSION}, which is not a ${K8S_MINOR} release"
info "the hosts have Kubernetes ${K8S_VERSION}"

log "Writing the configuration"
sed "s#__CONTROL_PLANE_ENDPOINT__#${VIP}:6443#" \
  "${ROOT}/test/e2e/kindnet.yaml" > "${WORKDIR}/kindnet.yaml"
grep -q '__CONTROL_PLANE_ENDPOINT__' "${WORKDIR}/kindnet.yaml" &&
  fail "the CNI manifest still has an unsubstituted placeholder"

{
  cat <<EOF
apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig

cluster:
  name: lab
  kubernetesVersion: ${K8S_VERSION}
  controlPlaneEndpoint:
    host: ${VIP}
    port: 6443
  controlPlaneReplicas: ${CONTROL_PLANE_COUNT}
  network:
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12

  # The whole reason for running on virtual machines: three control planes
  # electing a VIP between them, which a shared kernel cannot show.
  virtualIP:
    enabled: true
    interface: ${NETWORK_IFACE}

  cni:
    manifests:
      - ${WORKDIR}/kindnet.yaml

ssh:
  user: root
  port: 22
  privateKeyPath: ${KEY}
  hostKeyPolicy: TOFU

hosts:
EOF
  for ((i = 1; i <= CONTROL_PLANE_COUNT; i++)); do
    echo "  - {name: kg-cp-${i}, address: $(host_ip "kg-cp-${i}"), role: control-plane}"
  done
  for ((i = 1; i <= WORKER_COUNT; i++)); do
    echo "  - {name: kg-worker-${i}, address: $(host_ip "kg-worker-${i}"), role: worker}"
  done
} > "${CONFIG}"

"${KG}" config validate -c "${CONFIG}"

log "Preflight against the hosts"
"${KG}" inventory check -c "${CONFIG}"

log "Bringing up the genesis node"
"${KG}" init -c "${CONFIG}" --state-dir "${STATE}" \
  --provider-image "${PROVIDER_IMAGE}" --load-image --timeout "${TIMEOUT}"

log "Creating the cluster"
"${KG}" cluster create -c "${CONFIG}" --state-dir "${STATE}" --wait --timeout "${TIMEOUT}"

log "Checking the cluster"
"${KG}" kubeconfig -c "${CONFIG}" --state-dir "${STATE}"
KUBECONFIG="${WORKLOAD}" kubectl wait --for=condition=Ready nodes --all --timeout=10m
KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide

log "Verifying provider IDs reached the nodes"
missing=0
while read -r node provider_id; do
  if [[ "${provider_id}" != kgenesis://* ]]; then
    echo "    ${node}: provider ID is ${provider_id:-<empty>}"
    missing=1
  else
    info "${node} ${provider_id}"
  fi
done < <(KUBECONFIG="${WORKLOAD}" kubectl get nodes \
  -o jsonpath='{range .items[*]}{.metadata.name} {.spec.providerID}{"\n"}{end}')
((missing == 0)) || fail "a node is missing its kgenesis provider ID"

log "Checking etcd quorum across ${CONTROL_PLANE_COUNT} machines"
members="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes \
  -l node-role.kubernetes.io/control-plane --no-headers 2>/dev/null | wc -l | tr -d ' ')"
[[ "${members}" == "${CONTROL_PLANE_COUNT}" ]] ||
  fail "expected ${CONTROL_PLANE_COUNT} control plane nodes, found ${members}"
info "${members} control plane nodes"

log "Checking that the VIP answers"
KUBECONFIG="${WORKLOAD}" kubectl --server "https://${VIP}:6443" get --raw /healthz ||
  fail "the API server did not answer on the VIP ${VIP}"
echo

holder="$(vip_holder)" || fail "no control plane holds ${VIP}"
info "${VIP} is held by ${holder}"

log "Taking ${holder} down to see the VIP move"
limactl stop -f "${holder}" >/dev/null 2>&1

for attempt in {1..60}; do
  if new_holder="$(vip_holder)" && [[ "${new_holder}" != "${holder}" ]]; then
    info "${VIP} moved to ${new_holder} after $((attempt * 5))s"
    break
  fi
  ((attempt == 60)) && fail "${VIP} did not move off ${holder} within 5 minutes"
  sleep 5
done

log "Checking the API server still answers on the VIP"
for attempt in {1..30}; do
  if KUBECONFIG="${WORKLOAD}" kubectl --server "https://${VIP}:6443" \
      get --raw /healthz >/dev/null 2>&1; then
    info "the surviving control plane answers on ${VIP}"
    break
  fi
  ((attempt == 30)) && fail "the API server never came back on ${VIP}"
  sleep 10
done

log "Pivoting from this Mac"
"${KG}" pivot -c "${CONFIG}" --state-dir "${STATE}" \
  --provider-image "${PROVIDER_IMAGE}" --timeout "${TIMEOUT}"

log "Verifying the cluster now manages itself"
KUBECONFIG="${WORKLOAD}" kubectl get cluster,machines -A
KUBECONFIG="${WORKLOAD}" kubectl -n kgenesis-system \
  rollout status deploy/kgenesis-controller-manager --timeout=5m

log "Virtual machine test passed"
info "${CONTROL_PLANE_COUNT} control planes with etcd quorum, a VIP that survives"
info "losing its holder, and ${WORKER_COUNT} workers, all built from this Mac."
