#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# End-to-end scenarios on real virtual machines, with this Mac as the genesis
# node. test/vm/SCENARIOS.md describes each path and what it asserts.
#
# The container test covers the common path far more cheaply and is what CI runs.
# These exist for what containers cannot show, because they share a kernel and a
# network stack with everything else:
#
#   - etcd quorum across separate machines
#   - kube-vip holding the control plane VIP, and moving it when a node goes
#   - a real kubelet on a real kernel, with the distribution's own packages
#   - a kubelet that would otherwise advertise an address shared by every node
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
# free, and the better part of an hour.
#
#   test/vm/run.sh                    every scenario, in order
#   test/vm/run.sh build release      two of them
#   REUSE=1 test/vm/run.sh scale      skip creating and provisioning the VMs
#   KEEP=1 test/vm/run.sh             leave the VMs running afterwards
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# KG_VM_WORKDIR reuses the directory an earlier run left behind, which is what
# makes running a later scenario on its own possible: the genesis node's
# kubeconfig lives there, and without it every command reads as "run kg init
# first".
WORKDIR="${KG_VM_WORKDIR:-$(mktemp -d)}"
readonly WORKDIR
mkdir -p "${WORKDIR}"
readonly KEY="${WORKDIR}/id_ed25519"
readonly CONFIG="${WORKDIR}/kgenesis.yaml"
readonly STATE="${WORKDIR}/state"
readonly KG="${ROOT}/bin/kgenesis"
readonly WORKLOAD="${STATE}/lab.kubeconfig"

# shellcheck source=test/vm/lib.sh
source "${ROOT}/test/vm/lib.sh"
for file in "${ROOT}"/test/vm/scenarios/*.sh; do
  # shellcheck source=/dev/null
  source "${file}"
done

# The order they run in. Each one depends on the state the one before it leaves,
# which is why this is a list and not a directory listing.
readonly ALL_SCENARIOS=(inventory build vip-failover scale rebuild release multi-cluster)

selected=("$@")
((${#selected[@]} == 0)) && selected=("${ALL_SCENARIOS[@]}")

for name in "${selected[@]}"; do
  case " ${ALL_SCENARIOS[*]} " in
    *" ${name} "*) ;;
    *) fail "unknown scenario ${name}; try: ${ALL_SCENARIOS[*]}" ;;
  esac
done

collect_diagnostics() {
  log "Diagnostics"

  if [[ -f "${STATE}/bootstrap.kubeconfig" ]]; then
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" \
      kubectl get cluster,machines,hosts,hostmachines -A 2>&1 |
      head -40 | sed 's/^/    /' || true
    echo
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl -n kgenesis-system \
      logs deploy/kgenesis-controller-manager --tail=40 2>&1 | tail -40 | sed 's/^/    /' || true
  fi

  local kubeconfig
  for kubeconfig in "${STATE}"/*.kubeconfig; do
    [[ -f "${kubeconfig}" ]] || continue
    [[ "${kubeconfig}" == *bootstrap.kubeconfig ]] && continue
    echo
    info "--- $(basename "${kubeconfig}" .kubeconfig)"
    KUBECONFIG="${kubeconfig}" kubectl get nodes -o wide 2>&1 | sed 's/^/    /' || true
  done

  local existing host
  existing="$(limactl list --format '{{.Name}}' 2>/dev/null)"
  for host in $(host_names); do
    vm_exists "${existing}" "${host}" || continue
    echo
    info "--- ${host}: bootstrap log"
    limactl shell "${host}" -- sudo tail -n 40 /var/log/kgenesis-bootstrap.log 2>&1 |
      sed 's/^/    /' || true
    echo
    info "--- ${host}: kubelet"
    limactl shell "${host}" -- sudo journalctl -u kubelet --no-pager -n 40 2>&1 |
      sed 's/^/    /' || true
  done
}

cleanup() {
  local status=$?
  ((status != 0)) && collect_diagnostics || true

  if [[ "${KEEP}" == "1" || "${REUSE}" == "1" ]]; then
    log "Leaving the VMs running"
    info "config:     ${CONFIG}"
    info "state:      ${STATE}"
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
# Preflight

command -v limactl >/dev/null || fail "limactl is not installed (brew install lima)"
[[ -x /opt/socket_vmnet/bin/socket_vmnet ]] ||
  fail "socket_vmnet is not installed; see the note at the top of this script"

# A reused work directory keeps its key. Generating a new one would leave the
# genesis node holding the old one in a Secret, and every SSH the provider makes
# would fail - including the reset that returns a host to the pool.
if [[ ! -f "${KEY}" ]]; then
  ssh-keygen -t ed25519 -N '' -f "${KEY}" -q
fi
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
  log "Creating $(host_names | wc -l | tr -d ' ') VM(s)"
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
if ! go run "${WORKDIR}/localnet.go" "$(host_ip kg-cp-1):22" >/dev/null 2>&1; then
  fail "nc reaches the hosts but a freshly built binary cannot.

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
readonly K8S_VERSION
[[ "${K8S_VERSION}" == v${K8S_MINOR}.* ]] ||
  fail "the hosts report kubeadm ${K8S_VERSION}, which is not a ${K8S_MINOR} release"
info "the hosts have Kubernetes ${K8S_VERSION}"

log "Writing the configuration"
write_cni "${WORKDIR}/kindnet.yaml" "${VIP}:6443"
write_config "${CONFIG}" lab "${VIP}" "${WORKDIR}/kindnet.yaml" \
  1 "${CONTROL_PLANE_COUNT}" 1 "${WORKER_COUNT}"
"${KG}" config validate -c "${CONFIG}"

# ---------------------------------------------------------------------------
# Scenarios

started="$(date +%s)"
for name in "${selected[@]}"; do
  printf '\n\033[1m%s\033[0m\n' "############ scenario: ${name}"
  scenario_started="$(date +%s)"
  "scenario_${name//-/_}"
  info "scenario ${name} passed in $(( $(date +%s) - scenario_started ))s"
done

log "Virtual machine scenarios passed"
info "${#selected[@]} scenario(s) in $(( ($(date +%s) - started) / 60 ))m: ${selected[*]}"
