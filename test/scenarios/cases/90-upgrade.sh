#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A self-managed cluster rolled to a newer Kubernetes.
#
# kgenesis does not manage the kubeadm and kubelet on a host: it bootstraps
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
# image rebuild would. The hold has to come off first: the provisioning that put
# these packages there pinned them.
upgrade_packages_to() {
  local minor="$1" host ip
  for host in $(host_names); do
    ip="$(driver_host_ip "${host}")"
    (
      on_host "${ip}" "
        set -e
        export DEBIAN_FRONTEND=noninteractive
        apt-mark unhold kubelet kubeadm kubectl >/dev/null
        echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v${minor}/deb/ /' \
          > /etc/apt/sources.list.d/kubernetes.list
        curl -fsSL https://pkgs.k8s.io/core:/stable:/v${minor}/deb/Release.key |
          gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
        apt-get update -qq
        apt-get install -y -qq --allow-change-held-packages kubelet kubeadm kubectl
        apt-mark hold kubelet kubeadm kubectl >/dev/null
        kubeadm version -o short
      " 2>&1 | tail -1 | sed "s/^/    ${host} /"
    ) &
  done
  wait
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

  # kgenesis does not do this, and says so. A fleet upgrades its machines the way
  # it built them, which here is apt.
  log "Putting ${minor} on the machines"
  upgrade_packages_to "${minor}"
  local landed
  landed="$(on_host "$(driver_host_ip kg-cp-1)" "kubeadm version -o short" 2>/dev/null | tr -d '[:space:]')"
  [[ "${landed}" == v${minor}.* ]] ||
    fail "the hosts report kubeadm ${landed} after the upgrade, not a ${minor} release"
  info "the machines carry kubeadm ${landed}"

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

  log "Checking what it rolled to"
  KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide
  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"
  nodes_all_at_version "${WORKLOAD}" "${landed}"
  nodes_advertise_their_own_address "${WORKLOAD}"
  nodes_carry_provider_ids "${WORKLOAD}"
  endpoint_answers "${WORKLOAD}" "${LAB_ENDPOINT}" 6
  info "every node is on ${landed}, still on its own address, still behind the VIP"
}
