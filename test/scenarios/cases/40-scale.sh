#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# The worker pool grows onto the spare host and gives it back.

scenario_scale() {
  local spare="kg-worker-${WORKER_HOSTS}"
  local grown="${WORKDIR}/grown.yaml"

  log "Growing the worker pool onto ${spare}"
  # The same pool, one more worker asked of it.
  write_config "${grown}" lab "${LAB_ENDPOINT}" "${WORKDIR}/kindnet.yaml" \
    1 "${CONTROL_PLANE_HOSTS}" 1 "${WORKER_HOSTS}" \
    "${CONTROL_PLANE_REPLICAS}" "${WORKER_HOSTS}"
  kgc "${grown}" cluster create --wait --timeout "${TIMEOUT}"

  # The machine is Running once it has joined; the node turns Ready once the CNI
  # has started on it, which is a moment later.
  KUBECONFIG="${WORKLOAD}" kubectl wait --for=condition=Ready nodes --all --timeout=10m

  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_REPLICAS + WORKER_HOSTS))"
  KUBECONFIG="${WORKLOAD}" kubectl get node "$(driver_node_name "${spare}")" --no-headers |
    grep -q ' Ready' || fail "${spare} did not join the cluster"
  nodes_advertise_their_own_address "${WORKLOAD}"
  info "${spare} joined and carries its own address"

  log "Shrinking the pool back"
  kg cluster create --wait --timeout "${TIMEOUT}"

  local attempt nodes want="$((CONTROL_PLANE_REPLICAS + WORKER_REPLICAS))"
  for ((attempt = 1; attempt <= 60; attempt++)); do
    nodes="$(KUBECONFIG="${WORKLOAD}" kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    [[ "${nodes}" == "${want}" ]] && break
    ((attempt == 60)) &&
      fail "the cluster still has ${nodes} nodes ten minutes after the scale down"
    sleep 10
  done
  every_node_ready "${WORKLOAD}" "${want}"

  # Which machine a MachineDeployment removes is its own choice, so the host that
  # comes back is not necessarily the one that went in last. What has to be true
  # is that exactly one came back, and that it is usable again.
  local freed count
  freed="$(KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl get hosts -A \
    -l kgenesis.io/role=worker \
    -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase}{"\n"}{end}' 2>/dev/null |
    awk '$2 == "Available" { print $1 }')"
  count="$(echo "${freed}" | grep -c . || true)"
  [[ "${count}" == "1" ]] ||
    fail "expected exactly one worker host back in the pool, found ${count}: ${freed}"
  info "${freed} came back to the pool"

  # The point of scaling back is that the host is usable again, which means the
  # provider reset it rather than merely forgetting it.
  host_is_clean "${freed}"
  info "${freed} came back clean"
}
