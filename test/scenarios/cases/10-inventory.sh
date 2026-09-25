#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# The pool is described truthfully, including the parts of it that are not there.

scenario_inventory() {
  # The pool is the whole fleet, including the host no cluster has been given
  # yet. The configuration a cluster is built from lists only the hosts that
  # cluster may draw on, so this renders its own.
  local pool="${WORKDIR}/pool.yaml"
  # Every host, and one replica of each role: this configuration exists to list
  # the pool, not to build anything from it.
  write_config "${pool}" pool "${LAB_ENDPOINT}" "${WORKDIR}/kindnet.yaml" \
    1 "${CONTROL_PLANE_HOSTS}" 1 "${WORKER_HOSTS}" 1 1

  log "Checking the inventory"
  local report
  report="$(kgc "${pool}" inventory check)"
  echo "${report}" | sed 's/^/    /'

  local host
  for host in $(host_names); do
    echo "${report}" | grep -q "^${host}[[:space:]]" ||
      fail "${host} is missing from the inventory report"
  done

  # Read by where the header says the column is, rather than by counting fields
  # from the end: the cells hold spaces, and a column added later would silently
  # move the ones after it.
  local runtimes kubeadms
  runtimes="$(column_of "${report}" RUNTIME KUBEADM)"
  kubeadms="$(column_of "${report}" KUBEADM "")"

  # The runtime is a name and a version. It used to carry the whole --version
  # line, which is how a broken quoting in the probe script stayed invisible.
  echo "${runtimes}" | grep -qE '^[a-z]+ v?[0-9]+\.[0-9]+' ||
    fail "the runtime column does not read as a name and a version:
$(echo "${runtimes}" | sed 's/^/      /')"
  info "runtime: $(echo "${runtimes}" | head -1)"

  # kg does not install kubeadm, so the host decides which Kubernetes it
  # can build and the report has to say which that is.
  echo "${kubeadms}" | grep -qE '^v[0-9]+\.[0-9]+' ||
    fail "the kubeadm column does not read as a version:
$(echo "${kubeadms}" | sed 's/^/      /')"
  info "kubeadm: $(echo "${kubeadms}" | head -1)"

  log "Checking that an unreachable host is reported as unreachable"
  local probe="${WORKDIR}/dead.yaml"
  sed "s#^hosts:#hosts:\n  - {name: kg-ghost, address: ${DEAD_ADDRESS}, role: worker}#" \
    "${pool}" > "${probe}"

  local out status=0
  out="$(kgc "${probe}" inventory check 2>&1)" || status=$?
  echo "${out}" | sed 's/^/    /'

  ((status != 0)) ||
    fail "inventory check reported success with a host that does not exist"
  echo "${out}" | grep -q '^kg-ghost' ||
    fail "the unreachable host is missing from the report entirely"
  echo "${out}" | grep -qiE 'kg-ghost.*(unreachable|error|fail)' ||
    fail "kg-ghost is listed but not marked unreachable"
  info "the host that is not there is reported, not skipped"
}
