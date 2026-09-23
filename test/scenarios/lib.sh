#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Shared ground for the scenarios: the fleet's shape, the configuration kgenesis
# is given, and the assertions the cases lean on. Everything machine-specific is
# behind the driver, so the same case runs against virtual machines and against
# containers and asserts the same things.
#
# Sourced by run.sh, after the driver and before the cases.

K8S_MINOR="${K8S_MINOR:-1.33}"

# How many machines of each role exist, and how many of them the lab cluster
# asks for. They are not the same number: a scenario needs a host free to scale
# onto, and multi-cluster needs a control plane host per cluster.
CONTROL_PLANE_HOSTS="${CONTROL_PLANE_HOSTS:-3}"
CONTROL_PLANE_REPLICAS="${CONTROL_PLANE_REPLICAS:-3}"
WORKER_HOSTS="${WORKER_HOSTS:-3}"
WORKER_REPLICAS="${WORKER_REPLICAS:-2}"

PROVIDER_IMAGE="${PROVIDER_IMAGE:-kgenesis:scenarios}"
TIMEOUT="${TIMEOUT:-40m}"
KEEP="${KEEP:-0}"
REUSE="${REUSE:-0}"

# Nothing is ever given this address. The inventory case points a host at it to
# see an unreachable machine reported as unreachable.
DEAD_ADDRESS="${DEAD_ADDRESS:-203.0.113.9}"

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }
skip() { printf '    \033[33mskipped: %s\033[0m\n' "$*"; }
fail() { printf '\n\033[31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# The fleet

host_names() {
  local i
  for ((i = 1; i <= CONTROL_PLANE_HOSTS; i++)); do echo "kg-cp-${i}"; done
  for ((i = 1; i <= WORKER_HOSTS; i++)); do echo "kg-worker-${i}"; done
}

# driver_supports reports whether the fleet can do something a case needs. A
# case that needs what this fleet cannot give is skipped and said to be skipped,
# because a case that quietly asserts less is worse than no case.
driver_supports() {
  case " $(driver_capabilities) " in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
}

# on_host runs a command as root over the same SSH path kgenesis uses, so a case
# checks what kgenesis would see rather than what the driver can reach through
# its own agent.
on_host() {
  local ip="$1"; shift
  ssh -i "${KEY}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o ConnectTimeout=10 -o LogLevel=ERROR "root@${ip}" "$@"
}

# wipe_hosts undoes a kubeadm bootstrap everywhere, including an address kube-vip
# put on an interface that kubeadm reset knows nothing about. Cases that need an
# empty fleet but no longer have a genesis node to ask use this.
wipe_hosts() {
  local host ip vips
  vips="$(driver_vip_addresses | paste -sd'|' -)"
  [[ -z "${vips}" ]] && vips="__no_vip_here__"
  for host in $(host_names); do
    ip="$(driver_host_ip "${host}")"
    (
      on_host "${ip}" '
        kubeadm reset --force >/dev/null 2>&1 || true
        systemctl stop kubelet >/dev/null 2>&1 || true
        crictl rm --force --all >/dev/null 2>&1 || true
        for link in cni0 flannel.1 kube-ipvs0; do ip link delete "$link" 2>/dev/null || true; done
        ip -o -4 addr show | grep -E "'"${vips}"'" | awk "{ print \$2, \$4 }" |
          while read -r iface addr; do ip addr del "$addr" dev "$iface" 2>/dev/null || true; done
        rm -rf /etc/cni/net.d /var/lib/cni /etc/kubernetes /var/lib/etcd \
               /run/kubeadm /run/cluster-api /var/lib/kgenesis
      ' >/dev/null 2>&1 || true
    ) &
  done
  wait

  # Every command above is best effort, so a wipe that reached nothing at all -
  # a key that is no longer authorised, a machine that is down - would look
  # exactly like one that worked. The case after this one would then fail
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
    "${ROOT}/test/assets/kindnet.yaml" > "${path}"
  grep -q '__CONTROL_PLANE_ENDPOINT__' "${path}" &&
    fail "the CNI manifest still has an unsubstituted placeholder"
  return 0
}

# write_config renders a genesis configuration.
#
#   write_config <path> <name> <endpoint> <cni> \
#       <cp first> <cp count> <worker first> <worker count> [cp replicas] [worker replicas]
#
# The host ranges are what let two clusters draw from disjoint parts of one
# fleet, which is the only way to tell a namespace boundary that works from one
# that merely has not been tested. The replica counts default to the host counts
# and are given separately when a configuration exists to list hosts rather than
# to build anything.
write_config() {
  local path="$1" name="$2" endpoint="$3" cni="$4"
  local cp_first="$5" cp_count="$6" worker_first="$7" worker_count="$8" i
  local cp_replicas="${9:-$6}" worker_replicas="${10:-$8}"
  {
    cat <<EOF
apiVersion: kgenesis.io/v1alpha1
kind: GenesisConfig

cluster:
  name: ${name}
  kubernetesVersion: ${K8S_VERSION}
  controlPlaneEndpoint:
    host: ${endpoint}
    port: 6443
  controlPlaneReplicas: ${cp_replicas}
  network:
    podCIDR: 10.244.0.0/16
    serviceCIDR: 10.96.0.0/12
$(driver_cluster_extra)

  cni:
    manifests:
      - ${cni}

workers:
  - name: default
    replicas: ${worker_replicas}

ssh:
  user: root
  port: 22
  privateKeyPath: ${KEY}
  hostKeyPolicy: TOFU

hosts:
EOF
    for ((i = cp_first; i < cp_first + cp_count; i++)); do
      echo "  - {name: kg-cp-${i}, address: $(driver_host_ip "kg-cp-${i}"), role: control-plane}"
    done
    for ((i = worker_first; i < worker_first + worker_count; i++)); do
      echo "  - {name: kg-worker-${i}, address: $(driver_host_ip "kg-worker-${i}"), role: worker}"
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

# nodes_advertise_their_own_address is what a kubelet left to itself gets wrong.
# It picks the address of the default route, and behind a per-machine NAT that
# is the same address on every node, so the Nodes collide on it.
nodes_advertise_their_own_address() {
  local kubeconfig="$1" addresses total distinct
  addresses="$(KUBECONFIG="${kubeconfig}" kubectl get nodes \
    -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')"
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

# endpoint_answers fails unless the API server is reachable on the address every
# node joins through.
endpoint_answers() {
  local kubeconfig="$1" endpoint="$2" attempts="${3:-1}" attempt
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if KUBECONFIG="${kubeconfig}" kubectl --server "https://${endpoint}:6443" \
        get --raw /healthz >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
  done
  fail "the API server did not answer on ${endpoint}"
}

# host_is_clean fails unless a host carries nothing from a previous cluster.
host_is_clean() {
  local name="$1" ip leftovers vips
  ip="$(driver_host_ip "${name}")"
  vips="$(driver_vip_addresses | paste -sd'|' -)"
  [[ -z "${vips}" ]] && vips="__no_vip_here__"
  leftovers="$(on_host "${ip}" '
    for path in /etc/kubernetes/admin.conf /etc/kubernetes/kubelet.conf; do
      [ -e "$path" ] && echo "$path"
    done
    [ -n "$(ls -A /var/lib/etcd 2>/dev/null)" ] && echo /var/lib/etcd
    ip -o -4 addr show | grep -E "'"${vips}"'" | awk "{ print \"vip \" \$2 }"
    true
  ' 2>/dev/null)"
  [[ -z "${leftovers}" ]] ||
    fail "${name} still carries state from its last cluster:
$(echo "${leftovers}" | sed 's/^/      /')"
}

# hosts_in_phase fails unless every host in the pool reports the phase given.
hosts_in_phase() {
  local want="$1" name phase bad=0
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

# column_of prints one column of a padded table, found by where its header
# starts. Counting fields would not do: the cells hold spaces, and a column
# added later moves every one after it.
#
#   column_of "<report>" RUNTIME KUBEADM   the cell between the two headers
#   column_of "<report>" KUBEADM ""        the last column
column_of() {
  echo "$1" | awk -v from="$2" -v to="$3" '
    NR == 1 {
      start = index($0, from)
      stop = (to == "") ? 0 : index($0, to)
      next
    }
    start > 0 && NF > 4 {
      cell = (stop > 0) ? substr($0, start, stop - start) : substr($0, start)
      sub(/ +$/, "", cell)
      if (cell != "") print cell
    }
  ' | sort -u
}
