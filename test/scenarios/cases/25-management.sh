#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT

# Registry checks share the live cluster built by `build`. Released and
# self-managed transitions are checked at their corresponding lifecycle steps.
scenario_management() {
  assert_management_mode lab/lab managed
  assert_registered_status lab/lab managed
  assert_registered_kubeconfig lab/lab

  local isolated="${WORKDIR}/offline-registry" out
  mkdir -p "${isolated}"
  cp -R "${STATE}/clusters" "${isolated}/clusters"
  printf 'invalid kubeconfig\n' > "${isolated}/bootstrap.kubeconfig"
  out="$("${KG}" --state-dir "${isolated}" clusters 2>"${WORKDIR}/offline-warning")"
  echo "${out}" | awk '$1 == "lab" && $2 == "lab" && $3 == "managed" { found=1 } END { exit !found }' ||
    fail "MGT-004: a failed genesis connection changed or lost management mode"
  grep -q 'Could not refresh' "${WORKDIR}/offline-warning" ||
    fail "MGT-004: stale observations were not identified"
  info "MGT-004: connectivity failure preserves the registered mode"
}
