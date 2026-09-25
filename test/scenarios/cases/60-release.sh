#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Letting a cluster go: it keeps serving, and nothing kg installed stays
# behind with the power to reset the hosts underneath it.

scenario_release() {
  log "Ejecting from this Mac"
  kg eject --timeout "${TIMEOUT}"

  log "Checking the released cluster still serves"
  KUBECONFIG="${WORKLOAD}" kubectl get nodes -o wide
  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"
  endpoint_answers "${WORKLOAD}" "${LAB_ENDPOINT}"
  info "every node is still Ready and the endpoint still answers"

  KUBECONFIG="${WORKLOAD}" kubectl get namespace kgenesis-system >/dev/null 2>&1 &&
    fail "the released cluster is running the kg provider"
  KUBECONFIG="${WORKLOAD}" kubectl get secret -A \
    -l clusterctl.cluster.x-k8s.io/move --no-headers 2>/dev/null | grep -q . &&
    fail "an SSH credential followed the cluster it was supposed to stay behind"
  info "nothing kg installed is running in it"

  kind get clusters 2>/dev/null | grep -q '^kgenesis-bootstrap$' &&
    fail "the genesis node is still running after the release"
  [[ -f "${STATE}/bootstrap.kubeconfig" ]] &&
    fail "the genesis node is gone but its kubeconfig was left behind"
  info "the genesis node is gone"

  # A leftover kubeconfig used to make every later command report a client-go
  # parse failure instead of saying there is no genesis node.
  local out
  out="$(kg clusters 2>&1 || true)"
  echo "${out}" | sed 's/^/    /'
  echo "${out}" | grep -qi 'invalid configuration' &&
    fail "kg clusters reported a parse failure rather than the plain fact"
  echo "${out}" | grep -qE '^lab[[:space:]]+lab[[:space:]]+released' ||
    fail "kg forgot the released cluster"
  assert_management_mode lab/lab released
  assert_registered_status lab/lab released
  assert_registered_kubeconfig lab/lab
  assert_genesis_mutations_refused "${CONFIG}" lab/lab released
  info "kg remembers the release and can still query the cluster"
}
