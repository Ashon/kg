#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# The VIP outlives the machine holding it, and that machine comes back.

scenario_vip_failover() {
  local holder new_holder attempt
  holder="$(vip_holder)" || fail "no control plane holds ${VIP}"
  info "${VIP} is held by ${holder}"

  log "Taking ${holder} down to see the VIP move"
  limactl stop -f "${holder}" >/dev/null 2>&1

  for ((attempt = 1; attempt <= 60; attempt++)); do
    if new_holder="$(vip_holder)" && [[ "${new_holder}" != "${holder}" ]]; then
      info "${VIP} moved to ${new_holder} after $((attempt * 5))s"
      break
    fi
    ((attempt == 60)) && fail "${VIP} did not move off ${holder} within 5 minutes"
    sleep 5
  done

  log "Checking the API server still answers on the VIP"
  vip_answers "${WORKLOAD}" 30
  info "the surviving control plane answers on ${VIP}"

  # A failover that cannot be undone is half a test: the cluster has to survive
  # the machine coming back as well as going away.
  log "Bringing ${holder} back"
  limactl start "${holder}" >/dev/null 2>&1

  # These machines get a fresh cloud-init identity on every start, so the one
  # that just came back presents a different SSH host key. Real hardware does
  # not do that, but a machine that was reinstalled does, and TOFU refuses both
  # the same way - which is the point of it. Accepting the new key by hand is
  # the operator's half of that bargain, so it is exercised here.
  log "Accepting the host key ${holder} presents now"
  [[ -n "$(pinned_key "${holder}")" ]] ||
    fail "${holder} has no pinned host key, so TOFU never pinned one"

  kg inventory trust "${holder}"
  [[ -z "$(pinned_key "${holder}")" ]] ||
    fail "${holder} still has a pinned key after being trusted again"
  info "${holder} will pin the key it presents next"

  for ((attempt = 1; attempt <= 60; attempt++)); do
    if KUBECONFIG="${WORKLOAD}" kubectl get node "lima-${holder}" \
        --no-headers 2>/dev/null | grep -q ' Ready'; then
      info "lima-${holder} rejoined after $((attempt * 10))s"
      break
    fi
    ((attempt == 60)) && fail "lima-${holder} did not return to Ready within 10 minutes"
    sleep 10
  done

  every_node_ready "${WORKLOAD}" "$((CONTROL_PLANE_COUNT + WORKER_COUNT))"
  vip_answers "${WORKLOAD}" 6
}
