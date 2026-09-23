#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Shared ground for the virtual machine scenarios: the fleet's shape, the
# configuration kgenesis is given, and the assertions the scenarios lean on.
# Sourced by run.sh, which sources the scenarios after it.

K8S_MINOR="${K8S_MINOR:-1.33}"
CONTROL_PLANE_COUNT="${CONTROL_PLANE_COUNT:-3}"
WORKER_COUNT="${WORKER_COUNT:-2}"
# One host beyond what the cluster uses, so a scale-out has somewhere to go.
SPARE_COUNT="${SPARE_COUNT:-1}"
PROVIDER_IMAGE="${PROVIDER_IMAGE:-kgenesis:vm}"
TIMEOUT="${TIMEOUT:-40m}"
KEEP="${KEEP:-0}"
REUSE="${REUSE:-0}"

# The segment socket_vmnet serves. vmnet's DHCP does not answer on every macOS
# host and the switch works regardless, so addresses are assigned rather than
# leased. The VIP sits well above the hosts.
readonly SUBNET="192.168.105"
readonly VIP="${SUBNET}.200"
readonly NETWORK_IFACE="lima0"
# Nothing is ever given this address. The inventory scenario points a host at it
# to see an unreachable machine reported as unreachable.
readonly DEAD_ADDRESS="${SUBNET}.99"

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
fail() { printf '\n\033[31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# The fleet

# worker_total is every worker machine that exists, including the spare that no
# cluster claims until the scale scenario asks for it.
worker_total() { echo $((WORKER_COUNT + SPARE_COUNT)); }

host_names() {
  local i
  for ((i = 1; i <= CONTROL_PLANE_COUNT; i++)); do echo "kg-cp-${i}"; done
  for ((i = 1; i <= $(worker_total); i++)); do echo "kg-worker-${i}"; done
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

# on_host runs a command as root on one machine over the same SSH path kgenesis
# uses, so a scenario checks what kgenesis would see rather than what limactl can
# reach through its own agent.
on_host() {
  local ip="$1"; shift
  ssh -i "${KEY}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=10 -o LogLevel=ERROR "root@${ip}" "$@"
}

# wipe_hosts undoes a kubeadm bootstrap everywhere, including the VIP that
# kube-vip put on an interface and kubeadm reset knows nothing about. Scenarios
# that need an empty fleet but no longer have a genesis node to ask use this.
wipe_hosts() {
  local host ip
  for host in $(host_names); do
    ip="$(host_ip "${host}")"
    (
      on_host "${ip}" '
        kubeadm reset --force >/dev/null 2>&1 || true
        systemctl stop kubelet >/dev/null 2>&1 || true
        crictl rm --force --all >/dev/null 2>&1 || true
        for link in cni0 flannel.1 kube-ipvs0; do ip link delete "$link" 2>/dev/null || true; done
        ip -o -4 addr show | awk -v vip="'"${VIP}"'" "\$4 ~ \"^\"vip\"/\" { print \$2, \$4 }" |
          while read -r iface addr; do ip addr del "$addr" dev "$iface" 2>/dev/null || true; done
        rm -rf /etc/cni/net.d /var/lib/cni /etc/kubernetes /var/lib/etcd \
               /run/kubeadm /run/cluster-api /var/lib/kgenesis
      ' >/dev/null 2>&1 || true
    ) &
  done
  wait

  # Every command above is best effort, so a wipe that reached nothing at all -
  # a key that is no longer authorised, a machine that is down - would look
  # exactly like one that worked. The scenario after this one would then fail
  # somewhere far away from the cause.
  for host in $(host_names); do
    host_is_clean "${host}"
  done
}

# ---------------------------------------------------------------------------
# Configuration

# write_cni renders the CNI manifest for one control plane endpoint. kindnet has
# to be told the address it should reach the API server on, so a second cluster
# needs a second copy.
write_cni() {
  local path="$1" endpoint="$2"
  sed "s#__CONTROL_PLANE_ENDPOINT__#${endpoint}#" \
    "${ROOT}/test/e2e/kindnet.yaml" > "${path}"
  grep -q '__CONTROL_PLANE_ENDPOINT__' "${path}" &&
    fail "the CNI manifest still has an unsubstituted placeholder"
  return 0
}

# write_config renders a genesis configuration.
#
#   write_config <path> <name> <vip> <cni> <cp first> <cp count> <worker first> <worker count>
#
# The host ranges are what let two clusters draw from disjoint parts of one
# fleet, which is the only way to tell a namespace boundary that works from one
# that merely has not been tested.
write_config() {
  local path="$1" name="$2" vip="$3" cni="$4"
  local cp_first="$5" cp_count="$6" worker_first="$7" worker_count="$8" i
  {
    cat <<EOF
apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig

cluster:
  name: ${name}
  kubernetesVersion: ${K8S_VERSION}
  controlPlaneEndpoint:
    host: ${vip}
    port: 6443
  controlPlaneReplicas: ${cp_count}
  network:
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12

  # The whole reason for running on virtual machines: control planes electing a
  # VIP between them, which a shared kernel cannot show.
  virtualIP:
    enabled: true
    interface: ${NETWORK_IFACE}

  cni:
    manifests:
      - ${cni}

workers:
  - name: default
    replicas: ${worker_count}

ssh:
  user: root
  port: 22
  privateKeyPath: ${KEY}
  hostKeyPolicy: TOFU

hosts:
EOF
    for ((i = cp_first; i < cp_first + cp_count; i++)); do
      echo "  - {name: kg-cp-${i}, address: $(host_ip "kg-cp-${i}"), role: control-plane}"
    done
    for ((i = worker_first; i < worker_first + worker_count; i++)); do
      echo "  - {name: kg-worker-${i}, address: $(host_ip "kg-worker-${i}"), role: worker}"
    done
  } > "${path}"
}

kgc() { local cfg="$1"; shift; "${KG}" "$@" -c "${cfg}" --state-dir "${STATE}"; }
kg() { kgc "${CONFIG}" "$@"; }

# ---------------------------------------------------------------------------
# Assertions

# every_node_ready fails unless the workload cluster reports exactly n Ready
# nodes.
every_node_ready() {
  local kubeconfig="$1" want="$2" got
  got="$(KUBECONFIG="${kubeconfig}" kubectl get nodes --no-headers 2>/dev/null |
    grep -c ' Ready')" || got=0
  [[ "${got}" == "${want}" ]] ||
    fail "expected ${want} Ready node(s), found ${got}"
}

# nodes_advertise_their_own_address is the check that a shared kernel cannot
# make. Behind a per-machine NAT every node's default route carries the same
# address, so a kubelet left to choose reports an address that belongs to
# nobody, and Nodes collide on it.
nodes_advertise_their_own_address() {
  local kubeconfig="$1" addresses
  addresses="$(KUBECONFIG="${kubeconfig}" kubectl get nodes \
    -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')"
  local total distinct
  total="$(echo "${addresses}" | grep -c .)"
  distinct="$(echo "${addresses}" | sort -u | grep -c .)"
  [[ "${total}" == "${distinct}" ]] ||
    fail "nodes share an internal address, so the kubelet picked the wrong interface:
$(echo "${addresses}" | sed 's/^/      /')"
  info "${distinct} node(s), each on its own address"
}

# nodes_carry_provider_ids fails unless Cluster API can pair every Node with its
# Machine.
nodes_carry_provider_ids() {
  local kubeconfig="$1" node provider_id missing=0
  while read -r node provider_id; do
    [[ -z "${node}" ]] && continue
    if [[ "${provider_id}" != kgenesis://* ]]; then
      info "${node}: provider ID is ${provider_id:-<empty>}"
      missing=1
    fi
  done < <(KUBECONFIG="${kubeconfig}" kubectl get nodes \
    -o jsonpath='{range .items[*]}{.metadata.name} {.spec.providerID}{"\n"}{end}')
  ((missing == 0)) || fail "a node is missing its kgenesis provider ID"
}

# vip_answers fails unless the API server is reachable on the VIP.
vip_answers() {
  local kubeconfig="$1" attempts="${2:-1}" attempt
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if KUBECONFIG="${kubeconfig}" kubectl --server "https://${VIP}:6443" \
        get --raw /healthz >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
  done
  fail "the API server did not answer on the VIP ${VIP}"
}

# host_is_clean fails unless a host carries nothing from a previous cluster.
host_is_clean() {
  local name="$1" ip leftovers
  ip="$(host_ip "${name}")"
  leftovers="$(on_host "${ip}" '
    for path in /etc/kubernetes/admin.conf /etc/kubernetes/kubelet.conf; do
      [ -e "$path" ] && echo "$path"
    done
    [ -n "$(ls -A /var/lib/etcd 2>/dev/null)" ] && echo /var/lib/etcd
    ip -o -4 addr show | awk "\$4 ~ /^'"${VIP//./\\.}"'\\// { print \"vip \" \$2 }"
    true
  ' 2>/dev/null)"
  [[ -z "${leftovers}" ]] ||
    fail "${name} still carries state from its last cluster:
$(echo "${leftovers}" | sed 's/^/      /')"
}

# hosts_in_phase fails unless every host in the pool reports the phase given.
hosts_in_phase() {
  local want="$1" line name phase bad=0
  while read -r name phase; do
    [[ -z "${name}" ]] && continue
    if [[ "${phase}" != "${want}" ]]; then
      info "${name} is ${phase:-<none>}, not ${want}"
      bad=1
    fi
  done < <(KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl get hosts -A \
    -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase}{"\n"}{end}' 2>/dev/null)
  ((bad == 0)) || fail "not every host is ${want}"
}

# pinned_key reports the SSH host key a Host currently pins, which is empty when
# the next connection is free to pin whatever it sees.
pinned_key() {
  KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl get host "$1" -n lab \
    -o jsonpath='{.status.observedPublicKey}' 2>/dev/null
}
