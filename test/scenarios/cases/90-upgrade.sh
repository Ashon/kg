#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A self-managed cluster rolled to a newer Kubernetes.
#
# kg does not manage the kubeadm and kubelet on a host: it bootstraps
# machines that are already provisioned, so the host decides which Kubernetes it
# can build. KubeadmControlPlane upgrades by replacing machines, which means the
# new version has to be on the machines before the rollout reaches them. Putting
# it there is what re-imaging a host does in a real fleet, and what this case
# does by hand.
#
# It runs after self-manage and on the cluster that left behind, because a
# cluster that upgrades itself is the whole point of having handed it its own
# management.

# Machines, for the same reasons self-manage needs them, and a fleet whose
# packages this case can move between two minors.
requires_upgrade() { echo "vip"; }

# upgrade_packages_to moves every host to another Kubernetes minor, the way an
# image rebuild would, and then checks that every one of them actually moved.
# The hold has to come off first: the provisioning that put these packages there
# pinned them.
upgrade_packages_to() {
  local minor="$1" host ip stragglers=""

  for host in $(host_names); do
    ip="$(driver_host_ip "${host}")"
    (
      # Kept, not discarded. These run in the background so their exit codes go
      # nowhere, and a machine that did not move is a rollout that never
      # finishes an hour later.
      on_host "${ip}" "
        set -e
        export DEBIAN_FRONTEND=noninteractive

        # A cloud image runs unattended-upgrades on a timer, and it holds the
        # dpkg lock while it does. Waiting is what an operator would do.
        apt_get() { apt-get -o DPkg::Lock::Timeout=300 \"\$@\"; }

        apt-mark unhold kubelet kubeadm kubectl

        # Each Kubernetes minor signs its packages with its own key, so the
        # keyring is replaced rather than added to. Removed first: gpg stops on
        # a prompt when the file it is asked to write already exists, and there
        # is nobody here to answer it.
        rm -f /etc/apt/keyrings/kubernetes-apt-keyring.gpg
        curl -fsSL https://pkgs.k8s.io/core:/stable:/v${minor}/deb/Release.key |
          gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
        chmod 0644 /etc/apt/keyrings/kubernetes-apt-keyring.gpg
        echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v${minor}/deb/ /' \
          > /etc/apt/sources.list.d/kubernetes.list

        apt_get update
        apt_get install -y --allow-change-held-packages kubelet kubeadm kubectl
        apt-mark hold kubelet kubeadm kubectl
      " > "${WORKDIR}/upgrade-${host}.log" 2>&1 || true
    ) &
  done
  wait

  for host in $(host_names); do
    local got
    got="$(on_host "$(driver_host_ip "${host}")" "kubeadm version -o short" 2>/dev/null | tr -d '[:space:]')"
    if [[ "${got}" == v${minor}.* ]]; then
      info "${host} ${got}"
      continue
    fi
    stragglers="${stragglers} ${host}=${got:-unreachable}"
    info "--- ${host} did not move:"
    tail -n 12 "${WORKDIR}/upgrade-${host}.log" 2>/dev/null | sed 's/^/      /'
  done
  [[ -z "${stragglers}" ]] ||
    fail "these machines are not on ${minor}:${stragglers}"
}

# record_retiring notes which machines are marked for deletion at this moment. A
# rollout replaces one machine at a time and each replacement takes minutes, so
# polling sees them; whatever it misses is left out of the check below rather
# than guessed at.
record_retiring() {
  KUBECONFIG="${WORKLOAD}" kubectl get machine -n lab \
    -o jsonpath='{range .items[?(@.metadata.deletionTimestamp)]}{.metadata.name}{"\n"}{end}' \
    2>/dev/null >> "${WORKDIR}/retiring" || true
}

# retired_in_creation_order fails if a machine was replaced before an older one
# in the same set. Which machine goes next is the question an upgrade has to be
# able to answer before it starts, and a random pick shows up here as a machine
# retiring out of turn.
#
# What it holds the rollout to is the order recorded in this cluster, which is
# the one clusterctl move wrote: a move recreates every machine, and the API
# server stamps a fresh creationTimestamp on each. The order the cluster was
# originally built in is not it, and cannot be checked from here - the
# timestamps that held it went with the genesis node.
retired_in_creation_order() {
  local order_file="$1" what="$2" name index last=0 seen=""
  while read -r name; do
    index="$(grep -n -x -F -- "${name}" "${order_file}" | cut -d: -f1)" || true
    [[ -n "${index}" ]] || continue
    ((index > last)) ||
      fail "the ${what} rollout replaced ${name} after a younger machine (${seen})"
    last="${index}"
    seen="${seen}${seen:+, }${name}"
  done < <(awk '!seen[$0]++' "${WORKDIR}/retiring")

  [[ -n "${seen}" ]] || {
    info "no ${what} machine was caught between the polls"
    return 0
  }
  info "the ${what} machines went oldest first: ${seen}"
}

scenario_upgrade() {
  local from minor attempt

  # What the cluster was built at, and the minor to roll it to.
  from="${K8S_VERSION}"
  minor="${K8S_UPGRADE_TO:-}"
  [[ -n "${minor}" ]] ||
    fail "K8S_UPGRADE_TO is not set, so there is no version to roll to"
  [[ "${from}" != v${minor}.* ]] ||
    fail "the cluster is already on ${minor}, so there is nothing to roll"

  log "Checking the cluster it is starting from"
  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"
  nodes_all_at_version "${WORKLOAD}" "${from}"
  info "every node is on ${from}"

  # kg does not do this, and says so. A fleet upgrades its machines the way
  # it built them, which here is apt.
  # Upgrading the packages under a running cluster is a shortcut. A fleet would
  # replace the machine, which is what the rollout below does; doing it in place
  # is how this case gets the new version onto the machines the rollout will move
  # onto. It restarts every kubelet at once, so the control plane goes with them.
  log "Putting ${minor} on the machines"
  upgrade_packages_to "${minor}"
  local landed
  landed="$(on_host "$(driver_host_ip kg-cp-1)" "kubeadm version -o short" 2>/dev/null | tr -d '[:space:]')"
  info "the machines carry kubeadm ${landed}"

  log "Waiting for the cluster to come back"
  endpoint_answers "${WORKLOAD}" "${LAB_ENDPOINT}" 30
  for ((attempt = 1; attempt <= 60; attempt++)); do
    local ready
    ready="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes --no-headers 2>/dev/null |
      grep -c ' Ready')" || ready=0
    [[ "${ready}" == "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))" ]] && break
    ((attempt == 60)) &&
      fail "only ${ready} node(s) came back ten minutes after the kubelets restarted"
    sleep 10
  done
  info "every node is back"

  # The order to hold the rollout to, read from this cluster after the handover
  # rather than assumed from how it was built. See retired_in_creation_order.
  machines_by_age "${WORKLOAD}" control-plane > "${WORKDIR}/machines-cp"
  machines_by_age "${WORKLOAD}" worker > "${WORKDIR}/machines-worker"
  : > "${WORKDIR}/retiring"

  # From inside the cluster, because there is no genesis node any more. This is
  # the difference a handover makes: the cluster rolls itself.
  log "Asking the cluster to roll itself to ${landed}"
  KUBECONFIG="${WORKLOAD}" kubectl patch kubeadmcontrolplane -n lab lab-control-plane \
    --type merge -p "{\"spec\":{\"version\":\"${landed}\"}}"
  KUBECONFIG="${WORKLOAD}" kubectl patch machinedeployment -n lab lab-default \
    --type merge -p "{\"spec\":{\"template\":{\"spec\":{\"version\":\"${landed}\"}}}}"

  log "Waiting for the rollout"
  for ((attempt = 1; attempt <= 120; attempt++)); do
    local old ready
    record_retiring
    old="$(KUBECONFIG="${WORKLOAD}" kubectl get machine -n lab --no-headers 2>/dev/null |
      grep -cv "${landed}")" || old=0
    ready="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes --no-headers 2>/dev/null |
      grep -c ' Ready')" || ready=0
    if [[ "${old}" == "0" && "${ready}" == "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))" ]]; then
      info "the rollout finished after $((attempt * 15))s"
      break
    fi
    if ((attempt % 8 == 0)); then
      info "${old} machine(s) still on the old version, ${ready} node(s) Ready"
    fi
    ((attempt == 120)) && fail "the rollout did not finish in thirty minutes"
    sleep 15
  done

  log "Checking the order it went in"
  retired_in_creation_order "${WORKDIR}/machines-cp" "control plane"
  retired_in_creation_order "${WORKDIR}/machines-worker" "worker"

  log "Checking what it rolled to"
  KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide
  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"
  nodes_all_at_version "${WORKLOAD}" "${landed}"
  nodes_advertise_their_own_address "${WORKLOAD}"
  nodes_carry_provider_ids "${WORKLOAD}"
  endpoint_answers "${WORKLOAD}" "${LAB_ENDPOINT}" 6
  info "every node is on ${landed}, still on its own address, still behind the VIP"
}
