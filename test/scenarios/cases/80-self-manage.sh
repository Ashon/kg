#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A cluster that manages itself.
#
# clusterctl move recreates every object with a new UID and carries no status, so
# the provider has to recognise the hosts it is already running rather than claim
# fresh ones. That is the whole of this path, and it is the one that used to end
# with kubeadm running again on machines that were serving.

# Three control planes for etcd quorum, a host free for each role to roll onto,
# and an endpoint the control planes elect between: none of which a container
# fleet can give.
requires_self_manage() { echo "vip"; }

scenario_self_manage() {
  log "Emptying the fleet"
  wipe_hosts
  info "$(host_names | wc -l | tr -d ' ') host(s) wiped"

  log "Building a cluster that can carry its own management"
  kg init --timeout "${TIMEOUT}"
  kg cluster create --wait --timeout "${TIMEOUT}"
  kg kubeconfig
  KUBECONFIG="${WORKLOAD}" kubectl wait --for=condition=Ready nodes --all --timeout=10m
  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"

  # What the handover needs, and what it is refused for: a host free of each role
  # to roll onto, because KubeadmControlPlane and MachineDeployment both add a
  # machine before they remove one.
  local free_before
  free_before="$(free_hosts_by_role)"
  echo "${free_before}" | sed 's/^/    /'
  echo "${free_before}" | grep -q "^control-plane " ||
    fail "no control plane host is free, so the handover would be refused"
  echo "${free_before}" | grep -q "^worker " ||
    fail "no worker host is free, so the handover would be refused"

  log "Handing the cluster its own management"
  kg eject --self-manage --timeout "${TIMEOUT}"

  log "Checking the cluster carries the whole of it"
  local counts
  counts="$(KUBECONFIG="${WORKLOAD}" kubectl get -n lab \
    hosts,hostmachines,hostclusters,hostmachinetemplates --no-headers 2>/dev/null | wc -l | tr -d ' ')"
  ((counts > 0)) || fail "the cluster has none of the kg objects it was handed"
  KUBECONFIG="${WORKLOAD}" kubectl get -n lab hosts,hostmachines,hostclusters --no-headers |
    awk '{print $1}' | sed 's/^/    /'

  # The credential has to travel with the objects, or nothing in the cluster can
  # reach the machines underneath it.
  KUBECONFIG="${WORKLOAD}" kubectl get secret -n lab \
    -l clusterctl.cluster.x-k8s.io/move --no-headers 2>/dev/null | grep -q . ||
    fail "the SSH credential did not follow the objects that need it"

  KUBECONFIG="${WORKLOAD}" kubectl -n kgenesis-system \
    rollout status deploy/kgenesis-controller-manager --timeout=5m
  info "the provider runs in the cluster it manages"

  # The point of the whole path: the provider recognises the hosts it inherited
  # rather than claiming fresh ones, and does not run kubeadm again on machines
  # that are serving.
  log "Checking the cluster reconciles what it inherited"
  local attempt running
  for ((attempt = 1; attempt <= 60; attempt++)); do
    running="$(KUBECONFIG="${WORKLOAD}" kubectl get machine -n lab --no-headers 2>/dev/null |
      grep -c Running)" || running=0
    [[ "${running}" == "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))" ]] && break
    ((attempt == 60)) &&
      fail "only ${running} machine(s) came back to Running ten minutes after the handover"
    sleep 10
  done
  KUBECONFIG="${WORKLOAD}" kubectl get machine -n lab
  info "every machine is Running again, on the host it already had"

  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"

  # Claiming a fresh host would show up here: the same hosts are claimed, and the
  # ones that were free still are.
  local free_after
  free_after="$(KUBECONFIG="${WORKLOAD}" kubectl get hosts -n lab \
    -o jsonpath='{range .items[*]}{.metadata.labels.kgenesis\.io/role} {.metadata.labels.kgenesis\.io/claimed-by}{"\n"}{end}' 2>/dev/null |
    awk '$2 == "" { print $1 }' | sort | uniq -c | awk '{print $2, $1}' | sort)"
  [[ "${free_after}" == "${free_before}" ]] ||
    fail "the pool changed hands across the move:
      before: ${free_before//$'\n'/, }
      after:  ${free_after//$'\n'/, }"
  info "the same hosts are free, so nothing was claimed twice"

  kind get clusters 2>/dev/null | grep -q '^kgenesis-bootstrap$' &&
    fail "the genesis node survived a handover that left nothing for it to do"
  info "the genesis node is gone"
}
