#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# One genesis node, two clusters, released one at a time.
#
# The namespace per cluster is what makes this possible, and the only way to tell
# a boundary that works from one that has merely not been tested is to build two
# clusters from disjoint halves of one fleet and let one of them go.

scenario_multi_cluster() {
  # Whatever ran before left a cluster on these machines and no genesis node to
  # ask about it, so the fleet is emptied here rather than in the harness.
  log "Emptying the fleet"
  wipe_hosts
  info "$(host_names | wc -l | tr -d ' ') host(s) wiped"

  local alpha="${WORKDIR}/alpha.yaml" beta="${WORKDIR}/beta.yaml"
  local alpha_endpoint="$(driver_endpoint 2 kg-cp-1)" beta_endpoint="$(driver_endpoint 3 kg-cp-2)"
  local alpha_kubeconfig="${STATE}/alpha.kubeconfig"
  local beta_kubeconfig="${STATE}/beta.kubeconfig"

  write_cni "${WORKDIR}/kindnet-alpha.yaml" "${alpha_endpoint}:6443"
  write_cni "${WORKDIR}/kindnet-beta.yaml" "${beta_endpoint}:6443"
  write_config "${alpha}" alpha "${alpha_endpoint}" "${WORKDIR}/kindnet-alpha.yaml" 1 1 1 1
  write_config "${beta}"  beta  "${beta_endpoint}"  "${WORKDIR}/kindnet-beta.yaml"  2 1 2 1

  log "Bringing up the genesis node and building alpha"
  kgc "${alpha}" init --timeout "${TIMEOUT}"
  kgc "${alpha}" cluster create --wait --timeout "${TIMEOUT}"
  kgc "${alpha}" kubeconfig
  KUBECONFIG="${alpha_kubeconfig}" kubectl wait --for=condition=Ready nodes --all --timeout=10m
  every_node_ready "${alpha_kubeconfig}" 2

  # init adopts the genesis node that is already running and loads this
  # cluster's inventory beside the other one.
  log "Building beta on the same genesis node"
  kgc "${beta}" init --timeout "${TIMEOUT}"
  kgc "${beta}" cluster create --wait --timeout "${TIMEOUT}"
  kgc "${beta}" kubeconfig
  KUBECONFIG="${beta_kubeconfig}" kubectl wait --for=condition=Ready nodes --all --timeout=10m
  every_node_ready "${beta_kubeconfig}" 2

  log "Checking the genesis node manages both"
  local listed
  listed="$(kgc "${alpha}" clusters)"
  echo "${listed}" | sed 's/^/    /'
  echo "${listed}" | grep -q '^alpha[[:space:]]' || fail "alpha is missing from kg clusters"
  echo "${listed}" | grep -q '^beta[[:space:]]'  || fail "beta is missing from kg clusters"

  # Neither cluster may have reached into the other's half of the fleet.
  local alpha_nodes beta_nodes
  alpha_nodes="$(KUBECONFIG="${alpha_kubeconfig}" kubectl get nodes -o name | sort | tr '\n' ' ')"
  beta_nodes="$(KUBECONFIG="${beta_kubeconfig}" kubectl get nodes -o name | sort | tr '\n' ' ')"
  [[ "${alpha_nodes}" == "node/$(driver_node_name kg-cp-1) node/$(driver_node_name kg-worker-1) " ]] ||
    fail "alpha took hosts it was not given: ${alpha_nodes}"
  [[ "${beta_nodes}" == "node/$(driver_node_name kg-cp-2) node/$(driver_node_name kg-worker-2) " ]] ||
    fail "beta took hosts it was not given: ${beta_nodes}"
  info "each cluster drew only from its own namespace"

  # alpha is one control plane and one worker with nothing spare, which is
  # exactly the shape that cannot replace a machine it is running on.
  log "Checking that alpha is refused its own management"
  local refusal status=0
  refusal="$(kgc "${alpha}" eject --self-manage --timeout "${TIMEOUT}" 2>&1)" || status=$?
  echo "${refusal}" | sed 's/^/    /'
  ((status != 0)) || fail "alpha was handed its own management with nothing to roll onto"
  echo "${refusal}" | grep -q 'etcd needs 3 to keep quorum' ||
    fail "the refusal does not say the control plane is too small"
  echo "${refusal}" | grep -q 'no control plane host is free' ||
    fail "the refusal does not say there is no host to roll onto"
  kind get clusters 2>/dev/null | grep -q '^kgenesis-bootstrap$' ||
    fail "the refused handover took the genesis node with it"
  every_node_ready "${alpha_kubeconfig}" 2
  info "refused, and alpha is untouched"

  log "Releasing alpha while beta stays"
  kgc "${alpha}" eject --timeout "${TIMEOUT}"

  kind get clusters 2>/dev/null | grep -q '^kgenesis-bootstrap$' ||
    fail "the genesis node was deleted while it still manages beta"
  info "the genesis node is still running"

  every_node_ready "${alpha_kubeconfig}" 2
  every_node_ready "${beta_kubeconfig}" 2
  info "both clusters still serve"

  listed="$(kgc "${beta}" clusters)"
  echo "${listed}" | sed 's/^/    /'
  echo "${listed}" | grep -qE '^alpha[[:space:]]+alpha[[:space:]]+released' ||
    fail "alpha is not reported as Released"
  echo "${listed}" | grep -qE '^beta[[:space:]]+beta[[:space:]]+released' &&
    fail "beta was released along with alpha"
  info "alpha reads as Released, beta does not"
  assert_registered_status alpha/alpha released
  assert_registered_kubeconfig alpha/alpha
  assert_registered_status beta/beta managed
  info "MGT-007: released alpha is queried while beta still uses genesis"

  log "Releasing beta, which is the last one"
  kgc "${beta}" eject --timeout "${TIMEOUT}"

  kind get clusters 2>/dev/null | grep -q '^kgenesis-bootstrap$' &&
    fail "the genesis node survived releasing the last cluster"
  info "the genesis node is gone"

  every_node_ready "${alpha_kubeconfig}" 2
  every_node_ready "${beta_kubeconfig}" 2
  info "both clusters serve with nothing managing them"

  listed="$(kgc "${beta}" clusters --offline)"
  echo "${listed}" | grep -qE '^alpha[[:space:]]+alpha[[:space:]]+released' ||
    fail "alpha was forgotten when the genesis node was deleted"
  echo "${listed}" | grep -qE '^beta[[:space:]]+beta[[:space:]]+released' ||
    fail "beta was forgotten when the genesis node was deleted"
  kgc "${beta}" cluster status --cluster alpha/alpha
  kgc "${alpha}" kubeconfig --cluster beta/beta --stdout >/dev/null
  info "both released clusters remain addressable through kg"
  "${KG}" --state-dir "${STATE}" cluster forget --cluster alpha/alpha --yes
  assert_management_mode beta/beta released
  every_node_ready "${alpha_kubeconfig}" 2
  [[ -f "${alpha_kubeconfig}" ]] || fail "MGT-008: forget deleted the kubeconfig"
  local forgotten_status=0
  "${KG}" --state-dir "${STATE}" cluster status --cluster alpha/alpha >/dev/null 2>&1 || forgotten_status=$?
  ((forgotten_status != 0)) || fail "MGT-008: forgotten record still resolves"
  info "MGT-008: forget keeps the workload, kubeconfig and other records"
}
