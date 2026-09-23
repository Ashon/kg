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
  info "every node carries a kgenesis provider ID"

  local members
  members="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes \
    -l node-role.kubernetes.io/control-plane --no-headers | wc -l | tr -d ' ')"
  [[ "${members}" == "${CONTROL_PLANE_REPLICAS}" ]] ||
    fail "expected ${CONTROL_PLANE_REPLICAS} control plane nodes, found ${members}"
  info "${members} control plane node(s)"

  endpoint_answers "${WORKLOAD}" "${LAB_ENDPOINT}"
  info "the API server answers on ${LAB_ENDPOINT}"

  # The spare is in the fleet but not in this cluster's configuration, so it
  # should not have been touched.
  host_is_clean "kg-worker-${WORKER_HOSTS}"
  info "the spare host is untouched"
}
