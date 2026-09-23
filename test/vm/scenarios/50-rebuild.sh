#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A host can be used twice. This is the path that catches a reset which looks
# complete and is not: a machine that keeps the VIP from its last life answers
# for an endpoint it no longer serves, and sends the next kubeadm join to itself.

scenario_rebuild() {
  log "Deleting the cluster"
  kg cluster delete --yes

  log "Checking every host came back clean"
  local host
  for host in $(host_names); do
    host_is_clean "${host}"
    info "${host} is clean"
  done
  hosts_in_phase Available
  info "every host is back in the pool"

  log "Building a second cluster on the same machines"
  kg cluster create --wait --timeout "${TIMEOUT}"
  kg kubeconfig
  KUBECONFIG="${WORKLOAD}" kubectl wait --for=condition=Ready nodes --all --timeout=10m

  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_COUNT + WORKER_COUNT))"
  nodes_advertise_their_own_address "${WORKLOAD}"
  nodes_carry_provider_ids "${WORKLOAD}"
  vip_answers "${WORKLOAD}"
  info "the same hardware carries a second cluster"
}
