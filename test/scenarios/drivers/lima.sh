#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A fleet of Lima virtual machines on macOS. Separate kernels and separate
# network stacks, which is what kube-vip needs and what a shared kernel cannot
# give.
#
# The machines need a network that is both reachable from this Mac and able to
# carry traffic between themselves. vzNAT gives the first without the second and
# user-v2 the second without the first, so this means socket_vmnet:
#
#   brew install socket_vmnet
#   sudo install -d -o root -g wheel -m 755 /opt/socket_vmnet/bin
#   sudo install -o root -g wheel -m 755 \
#     "$(brew --prefix)/opt/socket_vmnet/bin/socket_vmnet" /opt/socket_vmnet/bin/
#   limactl sudoers > /tmp/lima-sudoers
#   sudo install -o root -g wheel -m 644 /tmp/lima-sudoers /etc/sudoers.d/lima

# The segment socket_vmnet serves. vmnet's DHCP does not answer on every macOS
# host and the switch works regardless, so addresses are assigned rather than
# leased. The VIPs sit well above the hosts.
CONTROL_PLANE_HOSTS="${CONTROL_PLANE_HOSTS:-4}"
CONTROL_PLANE_REPLICAS="${CONTROL_PLANE_REPLICAS:-3}"
WORKER_HOSTS="${WORKER_HOSTS:-2}"
WORKER_REPLICAS="${WORKER_REPLICAS:-1}"
K8S_MINOR="${K8S_MINOR:-1.32}"
K8S_UPGRADE_TO="${K8S_UPGRADE_TO:-1.33}"

readonly SUBNET="192.168.105"
readonly NETWORK_IFACE="lima0"

driver_name() { echo lima; }

# Real machines, so kube-vip can hold an address and move it.
driver_capabilities() { echo "vip reboot"; }

driver_host_ip() {
  local name="$1" index
  case "${name}" in
    kg-cp-*)     index="${name##*-}"; echo "${SUBNET}.$((10 + index))" ;;
    kg-worker-*) index="${name##*-}"; echo "${SUBNET}.$((10 + CONTROL_PLANE_HOSTS + index))" ;;
    *)           fail "unknown host ${name}" ;;
  esac
}

driver_node_name() { echo "lima-$1"; }

# Each cluster elects its own VIP, so they are numbered rather than shared.
driver_endpoint() { echo "${SUBNET}.$((199 + ${1:-1}))"; }

# Addresses kube-vip may have put on an interface, which kubeadm reset knows
# nothing about and a wipe has to take back off.
driver_vip_addresses() { local i; for ((i = 1; i <= 3; i++)); do driver_endpoint "${i}"; done; }

# The kernel is real and the packages are the distribution's own, so nothing has
# to be relaxed beyond what kubeadm cannot check inside a virtual machine.
driver_cluster_extra() {
  cat <<EOF
  virtualIP:
    enabled: true
    interface: ${NETWORK_IFACE}
  ignorePreflightErrors:
    - SystemVerification
EOF
}

driver_preflight() {
  command -v limactl >/dev/null || fail "limactl is not installed (brew install lima)"
  [[ -x /opt/socket_vmnet/bin/socket_vmnet ]] ||
    fail "socket_vmnet is not installed; see the note at the top of drivers/lima.sh"
}

lima_exists() {
  local existing="$1" name="$2"
  # Compared in the shell rather than with grep: whether `grep -qx` matches on a
  # pipe turned out to depend on which grep is on PATH.
  case $'\n'"${existing}"$'\n' in
    *$'\n'"${name}"$'\n'*) return 0 ;;
    *) return 1 ;;
  esac
}

driver_provision() {
  local existing host pid failed=0
  local pids=()
  existing="$(limactl list --format '{{.Name}}' 2>/dev/null)"

  if [[ "${REUSE}" == "1" ]]; then
    log "Reusing the running VMs"
    for host in $(host_names); do
      lima_exists "${existing}" "${host}" ||
        fail "REUSE=1 but ${host} is not among the instances: $(echo "${existing}" | tr '\n' ' ')"
      limactl start "${host}" >/dev/null 2>&1 || true
    done
  else
    log "Creating $(host_names | wc -l | tr -d ' ') VM(s)"
    for host in $(host_names); do
      limactl delete -f "${host}" >/dev/null 2>&1 || true
      limactl create --name "${host}" --tty=false "${ROOT}/test/scenarios/lima/host.yaml" >/dev/null 2>&1
    done

    # Started in parallel: each one spends most of its time in apt.
    for host in $(host_names); do
      limactl start "${host}" >"${REPORT_DIR}/start-${host}.log" 2>&1 &
      pids+=("$!")
    done
    for pid in "${pids[@]}"; do wait "${pid}" || failed=1; done
    ((failed == 0)) || fail "a VM failed to start; see ${REPORT_DIR}/start-*.log"
    pids=()

    log "Provisioning the hosts"
    for host in $(host_names); do
      (
        limactl shell "${host}" -- sudo bash -s -- \
          "$(driver_host_ip "${host}")" "${NETWORK_IFACE}" "${K8S_MINOR}" "${PUBKEY}" \
          < "${ROOT}/test/scenarios/provision-host.sh" >"${REPORT_DIR}/provision-${host}.log" 2>&1
      ) &
      pids+=("$!")
    done
    for pid in "${pids[@]}"; do wait "${pid}" || failed=1; done
    ((failed == 0)) || fail "host provisioning failed; see ${REPORT_DIR}/provision-*.log"
    return
  fi

  # The key is regenerated whenever the work directory is new, so it still has
  # to be installed on machines that were already there.
  for host in $(host_names); do
    limactl shell "${host}" -- sudo sh -c \
      "install -d -m 0700 /root/.ssh && printf '%s\n' '${PUBKEY}' > /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys"
  done
  info "$(host_names | wc -l | tr -d ' ') host(s)"
}

driver_kubernetes_version() {
  limactl shell kg-cp-1 -- kubeadm version -o short 2>/dev/null | tr -d '[:space:]'
}

driver_stop()  { limactl stop -f "$1" >/dev/null 2>&1; }
driver_start() { limactl start "$1" >/dev/null 2>&1; }

driver_teardown() {
  local host
  for host in $(host_names); do
    limactl delete -f "${host}" >/dev/null 2>&1 || true
  done
}

# vip_holder reports which control plane currently has the address on its
# interface, which is the only way to tell an election apart from a lucky route.
vip_holder() {
  local vip="${1:-$(driver_endpoint 1)}" host
  for host in $(host_names | grep cp); do
    if limactl shell "${host}" -- ip -4 -o addr show "${NETWORK_IFACE}" 2>/dev/null |
        grep -q "${vip}"; then
      echo "${host}"
      return 0
    fi
  done
  return 1
}

driver_diagnostics() {
  local existing host
  existing="$(limactl list --format '{{.Name}}' 2>/dev/null)"
  for host in $(host_names); do
    lima_exists "${existing}" "${host}" || continue
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
