#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A cluster from nothing: the genesis node, the machines, and the CNI.

scenario_build() {
  log "Bringing up the genesis node"
  kg init --timeout "${TIMEOUT}"

  log "Creating the cluster"
  kg cluster create --wait --timeout "${TIMEOUT}"

  log "Checking the cluster"
  kg kubeconfig
  KUBECONFIG="${WORKLOAD}" kubectl wait --for=condition=Ready nodes --all --timeout=10m
  KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide

  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"
  nodes_advertise_their_own_address "${WORKLOAD}"
  nodes_carry_provider_ids "${WORKLOAD}"
  info "every node carries a kg provider ID"

  local members
  members="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes \
    -l node-role.kubernetes.io/control-plane --no-headers | wc -l | tr -d ' ')"
  [[ "${members}" == "${CONTROL_PLANE_REPLICAS}" ]] ||
    fail "expected ${CONTROL_PLANE_REPLICAS} control plane nodes, found ${members}"
  info "${members} control plane node(s)"

  endpoint_answers "${WORKLOAD}" "${LAB_ENDPOINT}"
  info "the API server answers on ${LAB_ENDPOINT}"

  # The pool holds more hosts than the cluster asked for, and which ones it took
  # is its own choice. What has to be true is that the rest are still free, and
  # that nothing ran on them.
  local spares host want
  want="$((WORKER_HOSTS - WORKER_REPLICAS))"
  spares="$(free_host_names worker)"
  [[ "$(echo "${spares}" | grep -c .)" == "${want}" ]] ||
    fail "expected ${want} worker host(s) still free, got: ${spares//$'\n'/ }"
  for host in ${spares}; do
    host_is_clean "${host}"
  done
  info "the spare host(s) are untouched: ${spares//$'\n'/ }"
}
