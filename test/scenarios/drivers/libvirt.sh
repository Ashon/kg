#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A fleet of KVM virtual machines on Linux, which is what lets the scenarios that
# need a real machine run somewhere other than a Mac.
#
# GitHub's Linux runners expose /dev/kvm, so the same separate kernels and
# separate network stacks the lima fleet gives are available in CI. The workflow
# installs libvirt and grants access to the device; this driver assumes both and
# says so if either is missing.
#
# The addresses match the lima fleet on purpose: same subnet, same hosts, same
# VIP, so a scenario cannot come to depend on one of them.

readonly SUBNET="192.168.105"
readonly NET_NAME="kgenesis"
readonly BRIDGE="virbr-kg"
# kind puts its node on this docker network, and the provider reaches the hosts
# from inside that node.
readonly KIND_NETWORK="kind"

# A runner has four cores and sixteen gigabytes for the fleet, the bootstrap
# cluster and the build. Three control planes is the smallest number that keeps
# etcd quorum while one machine is stopped, which is the whole reason for
# running on machines rather than containers.
# Four control plane hosts for three replicas: a handover is refused without one
# free, because KubeadmControlPlane adds a machine before it removes one.
CONTROL_PLANE_HOSTS="${CONTROL_PLANE_HOSTS:-4}"
CONTROL_PLANE_REPLICAS="${CONTROL_PLANE_REPLICAS:-3}"
WORKER_HOSTS="${WORKER_HOSTS:-2}"
WORKER_REPLICAS="${WORKER_REPLICAS:-1}"

# Six machines have to fit beside the bootstrap cluster on a sixteen gigabyte
# runner, so these are the smallest a control plane and a worker come up on.
CP_MEMORY_MB="${CP_MEMORY_MB:-1792}"
WORKER_MEMORY_MB="${WORKER_MEMORY_MB:-1280}"
# kubeadm refuses to bring up a control plane on fewer than two CPUs, and that
# is a check worth keeping rather than relaxing. Three of them on a four core
# runner is oversubscribed, which costs time and nothing else: a control plane
# is mostly idle once it is up.
CP_VCPUS="${CP_VCPUS:-2}"
WORKER_VCPUS="${WORKER_VCPUS:-1}"

# Images live on the runner's large ephemeral disk; the root filesystem has
# nowhere near enough room for a base image and an overlay per machine.
IMAGE_DIR="${IMAGE_DIR:-/mnt/kgenesis-images}"
BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"

driver_name() { echo libvirt; }

driver_capabilities() { echo "vip reboot"; }

driver_host_ip() {
  local name="$1" index
  case "${name}" in
    kg-cp-*)     index="${name##*-}"; echo "${SUBNET}.$((10 + index))" ;;
    kg-worker-*) index="${name##*-}"; echo "${SUBNET}.$((10 + CONTROL_PLANE_HOSTS + index))" ;;
    *)           fail "unknown host ${name}" ;;
  esac
}

driver_node_name() { echo "$1"; }

driver_endpoint() { echo "${SUBNET}.$((199 + ${1:-1}))"; }

driver_vip_addresses() { local i; for ((i = 1; i <= 3; i++)); do driver_endpoint "${i}"; done; }

# A MAC per host, so libvirt's DHCP can hand each machine the address the
# configuration says it has. Deriving it from the last octet keeps the two in
# step without a second table to forget to update.
libvirt_mac() {
  printf '52:54:00:00:01:%02x\n' "$(driver_host_ip "$1" | awk -F. '{print $4}')"
}

driver_cluster_extra() {
  cat <<EOF
  virtualIP:
    enabled: true
    interface: enp1s0
  ignorePreflightErrors:
    - SystemVerification
EOF
}

driver_preflight() {
  [[ "$(uname -s)" == "Linux" ]] || fail "the libvirt fleet runs on Linux; use the lima driver on macOS"
  [[ -w /dev/kvm ]] || fail "/dev/kvm is not writable, so the machines would be emulated rather than run.

    On a GitHub runner it is there but owned by the kvm group:

      echo 'KERNEL==\"kvm\", GROUP=\"kvm\", MODE=\"0666\", OPTIONS+=\"static_node=kvm\"' |
        sudo tee /etc/udev/rules.d/99-kvm4all.rules
      sudo udevadm control --reload-rules
      sudo udevadm trigger --name-match=kvm"
  command -v virt-install >/dev/null || fail "virt-install is not installed (apt-get install virtinst)"
  command -v virsh >/dev/null || fail "virsh is not installed (apt-get install libvirt-clients)"
  command -v dnsmasq >/dev/null || [[ -x /usr/sbin/dnsmasq ]] ||
    fail "dnsmasq is not installed, and libvirt hands a NAT network's addresses
    out with it (apt-get install dnsmasq-base)"
  virsh --connect qemu:///system list >/dev/null 2>&1 ||
    fail "cannot talk to qemu:///system; is libvirtd running and this user in the libvirt group?"
}

virsh_() { virsh --connect qemu:///system "$@"; }

# libvirt_network defines a NAT network with no addresses to hand out except the
# ones named here, so a machine always comes up on the address the configuration
# says it has.
libvirt_network() {
  local host
  cat <<EOF
<network>
  <name>${NET_NAME}</name>
  <forward mode='nat'/>
  <bridge name='${BRIDGE}' stp='on' delay='0'/>
  <ip address='${SUBNET}.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='${SUBNET}.240' end='${SUBNET}.250'/>
$(for host in $(host_names); do
    echo "      <host mac='$(libvirt_mac "${host}")' name='${host}' ip='$(driver_host_ip "${host}")'/>"
  done)
    </dhcp>
  </ip>
</network>
EOF
}

libvirt_cloud_init() {
  local host="$1"
  cat <<EOF
#cloud-config
hostname: ${host}
fqdn: ${host}
preserve_hostname: false
ssh_pwauth: false
write_files:
  - path: /root/.ssh/authorized_keys
    permissions: '0600'
    owner: 'root:root'
    content: |
      ${PUBKEY}
  - path: /etc/cloud/cloud.cfg.d/99-kgenesis-hostkeys.cfg
    content: |
      ssh_deletekeys: false
runcmd:
  - [ sh, -c, "sed -i 's/^#\\\\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config" ]
  - [ systemctl, restart, ssh ]
EOF
}

driver_provision() {
  local host base overlay memory vcpus attempt

  sudo install -d -m 0755 "${IMAGE_DIR}"
  base="${IMAGE_DIR}/base.img"
  if [[ ! -f "${base}" ]]; then
    log "Fetching the base image"
    sudo curl -fsSL "${BASE_IMAGE_URL}" -o "${base}"
    info "$(basename "${BASE_IMAGE_URL}")"
  fi

  log "Defining the ${NET_NAME} network"
  virsh_ net-destroy "${NET_NAME}" >/dev/null 2>&1 || true
  virsh_ net-undefine "${NET_NAME}" >/dev/null 2>&1 || true
  libvirt_network > "${WORKDIR}/network.xml"
  virsh_ net-define "${WORKDIR}/network.xml" >/dev/null ||
    fail "could not define the ${NET_NAME} network"
  virsh_ net-start "${NET_NAME}" >/dev/null ||
    fail "could not start the ${NET_NAME} network. libvirt hands a NAT network's
    addresses out with dnsmasq, so a missing dnsmasq-base looks exactly like
    this."
  info "${BRIDGE} on ${SUBNET}.0/24"

  libvirt_open_the_path

  log "Creating $(host_names | wc -l | tr -d ' ') machine(s)"
  for host in $(host_names); do
    virsh_ destroy "${host}" >/dev/null 2>&1 || true
    virsh_ undefine "${host}" --nvram --remove-all-storage >/dev/null 2>&1 || true

    overlay="${IMAGE_DIR}/${host}.qcow2"
    sudo rm -f "${overlay}"
    # An overlay rather than a copy: the machines differ by what they install,
    # and a base image they all share costs one download and one copy.
    sudo qemu-img create -f qcow2 -F qcow2 -b "${base}" "${overlay}" 20G >/dev/null
    sudo chmod 0644 "${overlay}"

    case "${host}" in
      kg-cp-*) memory="${CP_MEMORY_MB}"; vcpus="${CP_VCPUS}" ;;
      *)       memory="${WORKER_MEMORY_MB}"; vcpus="${WORKER_VCPUS}" ;;
    esac

    libvirt_cloud_init "${host}" > "${WORKDIR}/${host}-user-data.yaml"
    virt-install --connect qemu:///system \
      --name "${host}" --memory "${memory}" --vcpus "${vcpus}" \
      --disk "path=${overlay},format=qcow2,bus=virtio" \
      --network "network=${NET_NAME},mac=$(libvirt_mac "${host}"),model=virtio" \
      --cloud-init "user-data=${WORKDIR}/${host}-user-data.yaml,disable=on" \
      --osinfo ubuntu24.04 --graphics none --noautoconsole --import >/dev/null
    info "${host} $(driver_host_ip "${host}")"
  done

  log "Waiting for the machines to answer"
  for host in $(host_names); do
    for attempt in {1..90}; do
      if on_host "$(driver_host_ip "${host}")" true >/dev/null 2>&1; then
        break
      fi
      ((attempt == 90)) && fail "${host} never answered on $(driver_host_ip "${host}")"
      sleep 5
    done
  done

  libvirt_check_the_path

  log "Provisioning the hosts"
  for host in $(host_names); do
    (
      # The address already comes from the network's own DHCP, so the script is
      # told to leave the interface alone.
      on_host "$(driver_host_ip "${host}")" "bash -s -- '' '' '${K8S_MINOR}' '${PUBKEY}'" \
        < "${ROOT}/test/scenarios/provision-host.sh" 2>&1 | tail -1 | sed 's/^/    /'
    ) &
  done
  wait
}

# libvirt_reach_from_the_bootstrap_network opens the path between the two
# bridges on this machine.
#
# The bootstrap cluster is a kind cluster: a container on a docker bridge. The
# provider reaches the hosts from inside it, and the hosts are on a libvirt
# bridge. Docker leaves forwarding between bridges to its own rules and libvirt
# rejects what it did not expect, so without this every host reads as
# Unreachable - which looks exactly like machines that are not answering, when
# in fact they answer perfectly well from the runner itself.
libvirt_bootstrap_bridge() {
  local id
  docker network inspect "${KIND_NETWORK}" >/dev/null 2>&1 ||
    docker network create "${KIND_NETWORK}" >/dev/null
  id="$(docker network inspect "${KIND_NETWORK}" -f '{{.Id}}')"
  echo "br-${id:0:12}"
}

libvirt_open_the_path() {
  local bridge
  bridge="$(libvirt_bootstrap_bridge)"
  log "Opening the path from ${bridge} to ${BRIDGE}"
  sudo iptables -I FORWARD 1 -i "${bridge}" -o "${BRIDGE}" -j ACCEPT
  sudo iptables -I FORWARD 1 -i "${BRIDGE}" -o "${bridge}" -j ACCEPT
}

# Checked rather than assumed. A rule that landed in a table nothing consults
# looks exactly like one that worked, and the symptom arrives forty minutes
# later as a cluster that never came up.
libvirt_check_the_path() {
  local bridge ip
  bridge="$(libvirt_bootstrap_bridge)"
  ip="$(driver_host_ip kg-cp-1)"
  docker run --rm --network "${KIND_NETWORK}" alpine:3.20 \
    nc -z -w5 "${ip}" 22 >/dev/null 2>&1 ||
    fail "a container on ${bridge} cannot reach ${ip}:22, though this machine can.

    The provider runs inside the bootstrap cluster, which is a container on that
    bridge, so it would report every host as unreachable while the runner itself
    reaches them all."
  info "a container on ${bridge} reaches the fleet"
}

driver_kubernetes_version() {
  on_host "$(driver_host_ip kg-cp-1)" "kubeadm version -o short" 2>/dev/null | tr -d '[:space:]'
}

driver_stop()  { virsh_ destroy "$1" >/dev/null 2>&1; }
driver_start() { virsh_ start "$1" >/dev/null 2>&1; }

driver_teardown() {
  local host
  for host in $(host_names); do
    virsh_ destroy "${host}" >/dev/null 2>&1 || true
    virsh_ undefine "${host}" --nvram --remove-all-storage >/dev/null 2>&1 || true
  done
  virsh_ net-destroy "${NET_NAME}" >/dev/null 2>&1 || true
  virsh_ net-undefine "${NET_NAME}" >/dev/null 2>&1 || true
}

# vip_holder reports which control plane currently has the address on its
# interface, which is the only way to tell an election apart from a lucky route.
vip_holder() {
  local vip="${1:-$(driver_endpoint 1)}" host
  for host in $(host_names | grep cp); do
    if on_host "$(driver_host_ip "${host}")" "ip -4 -o addr show" 2>/dev/null |
        grep -q "${vip}"; then
      echo "${host}"
      return 0
    fi
  done
  return 1
}

driver_diagnostics() {
  local host
  virsh_ list --all 2>&1 | sed 's/^/    /' || true
  for host in $(host_names); do
    echo
    info "--- ${host}: bootstrap log"
    on_host "$(driver_host_ip "${host}")" "tail -n 40 /var/log/kgenesis-bootstrap.log" 2>&1 |
      sed 's/^/    /' || true
    echo
    info "--- ${host}: kubelet"
    on_host "$(driver_host_ip "${host}")" "journalctl -u kubelet --no-pager -n 40" 2>&1 |
      sed 's/^/    /' || true
  done
}
