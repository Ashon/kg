#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Runs the scenarios in test/scenarios/cases against a fleet of machines.
# test/scenarios/SCENARIOS.md describes each path and what it asserts.
#
# The fleet comes from a driver, so the same cases run against real virtual
# machines and against containers:
#
#   DRIVER=lima     Lima virtual machines on macOS. Separate kernels and
#                   network stacks, so every case runs, kube-vip included.
#   DRIVER=docker   Containers on one docker bridge. Fast enough for every
#                   push; the cases that need a real machine are skipped and
#                   said to be skipped.
#
#   test/scenarios/run.sh                    every case, in order
#   test/scenarios/run.sh build release      two of them
#   DRIVER=docker test/scenarios/run.sh      against containers
#   REUSE=1 test/scenarios/run.sh scale      skip creating the machines
#   KEEP=1 test/scenarios/run.sh             leave the fleet running afterwards
#   KG_SCENARIO_WORKDIR=... run.sh rebuild   reuse an earlier run's state
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DRIVER="${DRIVER:-lima}"
[[ -f "${ROOT}/test/scenarios/drivers/${DRIVER}.sh" ]] ||
  { echo "no such driver: ${DRIVER}" >&2; exit 1; }

# KG_SCENARIO_WORKDIR reuses the directory an earlier run left behind, which is
# what makes running a later case on its own possible: the genesis node's
# kubeconfig lives there, and without it every command reads as "run kg init
# first".
WORKDIR="${KG_SCENARIO_WORKDIR:-$(mktemp -d)}"
readonly WORKDIR
REPORT_DIR="${KG_SCENARIO_REPORT_DIR:-${ROOT}/.artifacts/e2e/${DRIVER}-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "${REPORT_DIR}"
REPORT_DIR="$(cd "${REPORT_DIR}" && pwd)"
readonly REPORT_DIR
# Run the scenario shell as a child of the logging pipeline. A process
# substitution here becomes a job that Linux bash's bare `wait` never finishes.
if [[ "${KG_SCENARIO_LOGGED:-0}" != 1 ]]; then
  source "${ROOT}/test/scenarios/logging.sh"
  run_logged "${REPORT_DIR}/run.log" env KG_SCENARIO_LOGGED=1 \
    KG_SCENARIO_WORKDIR="${WORKDIR}" KG_SCENARIO_REPORT_DIR="${REPORT_DIR}" \
    bash "${BASH_SOURCE[0]}" "$@"
  exit $?
fi
: > "${REPORT_DIR}/results.tsv"
active_scenario=setup
scenario_started="$(date +%s)"
report_result() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "${REPORT_DIR}/results.tsv"
}
write_report() {
  local name result seconds
  {
    printf '<?xml version="1.0" encoding="UTF-8"?>\n<testsuite name="kg-%s">\n' "${DRIVER}"
    while IFS=$'\t' read -r name result seconds; do
      printf '  <testcase classname="kg.%s" name="%s" time="%s">' "${DRIVER}" "${name}" "${seconds}"
      case "${result}" in
        failed) printf '<failure message="Scenario failed; see run.log and diagnostics.log"/>' ;;
        skipped) printf '<skipped message="Fleet lacks required capabilities"/>' ;;
      esac
      printf '</testcase>\n'
    done < "${REPORT_DIR}/results.tsv"
    printf '</testsuite>\n'
  } > "${REPORT_DIR}/junit.xml"
}
mkdir -p "${WORKDIR}"
readonly KEY="${WORKDIR}/id_ed25519"
readonly CONFIG="${WORKDIR}/kg.yaml"
readonly STATE="${WORKDIR}/state"
readonly KG="${ROOT}/bin/kg"
readonly WORKLOAD="${STATE}/lab.kubeconfig"

# shellcheck source=test/scenarios/drivers/lima.sh
source "${ROOT}/test/scenarios/drivers/${DRIVER}.sh"
# shellcheck source=test/scenarios/lib.sh
source "${ROOT}/test/scenarios/lib.sh"
for file in "${ROOT}"/test/scenarios/cases/*.sh; do
  # shellcheck source=/dev/null
  source "${file}"
done

# The order they run in. Each one depends on the state the one before it leaves,
# which is why this is a list and not a directory listing.
readonly ALL_SCENARIOS=(inventory build management vip-failover scale rebuild release multi-cluster self-manage upgrade)

selected=("$@")
((${#selected[@]} == 0)) && selected=("${ALL_SCENARIOS[@]}")

for name in "${selected[@]}"; do
  case " ${ALL_SCENARIOS[*]} " in
    *" ${name} "*) ;;
    *) fail "unknown scenario ${name}; try: ${ALL_SCENARIOS[*]}" ;;
  esac
done

# requires_<name> names what a case needs from the fleet. A case without one
# needs nothing in particular.
missing_capabilities() {
  local name="$1" fn="requires_${1//-/_}" capability missing=""
  declare -F "${fn}" >/dev/null || return 0
  for capability in $("${fn}"); do
    driver_supports "${capability}" || missing="${missing} ${capability}"
  done
  echo "${missing# }"
}

collect_diagnostics() {
  log "Diagnostics"

  if [[ -f "${STATE}/bootstrap.kubeconfig" ]]; then
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" \
      kubectl --request-timeout=15s get cluster,machines,hosts,hostmachines -A 2>&1 |
      head -40 | sed 's/^/    /' || true
    echo
    KUBECONFIG="${STATE}/bootstrap.kubeconfig" kubectl --request-timeout=15s -n kgenesis-system \
      logs deploy/kgenesis-controller-manager --tail=40 2>&1 | tail -40 | sed 's/^/    /' || true
  fi

  local kubeconfig
  for kubeconfig in "${STATE}"/*.kubeconfig; do
    [[ -f "${kubeconfig}" ]] || continue
    [[ "${kubeconfig}" == *bootstrap.kubeconfig ]] && continue
    echo
    info "--- $(basename "${kubeconfig}" .kubeconfig)"
    KUBECONFIG="${kubeconfig}" kubectl --request-timeout=15s get nodes -o wide 2>&1 | sed 's/^/    /' || true
    KUBECONFIG="${kubeconfig}" kubectl --request-timeout=15s get clusters,machines,hosts,hostmachines,kubeadmcontrolplanes -A -o wide 2>&1 || true
    KUBECONFIG="${kubeconfig}" kubectl --request-timeout=15s get pods -A -o wide 2>&1 || true
    KUBECONFIG="${kubeconfig}" kubectl --request-timeout=15s -n kgenesis-system logs deploy/kgenesis-controller-manager --tail=80 2>&1 || true
  done

  driver_diagnostics || true
}

cleanup() {
  local status=$?
  if ((status != 0)); then
    report_result "${active_scenario}" failed "$(( $(date +%s) - scenario_started ))"
    collect_diagnostics > "${REPORT_DIR}/diagnostics.log" 2>&1 || true
    tail -60 "${REPORT_DIR}/diagnostics.log" || true
  fi
  write_report
  info "report: ${REPORT_DIR}"

  if [[ "${KEEP}" == "1" || "${REUSE}" == "1" ]]; then
    log "Leaving the fleet running"
    info "config:     ${CONFIG}"
    info "state:      ${STATE}"
    return
  fi

  log "Cleaning up"
  "${KG}" reset --name kgenesis-bootstrap --state-dir "${STATE}" --yes >/dev/null 2>&1 || true
  kind delete cluster --name kgenesis-bootstrap >/dev/null 2>&1 || true
  driver_teardown
  rm -rf "${WORKDIR}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Preflight

driver_preflight

# A reused work directory keeps its key. Generating a new one would leave the
# genesis node holding the old one in a Secret, and every SSH the provider makes
# would fail - including the reset that returns a host to the pool.
if [[ ! -f "${KEY}" ]]; then
  ssh-keygen -t ed25519 -N '' -f "${KEY}" -q
fi
readonly PUBKEY="$(cat "${KEY}.pub")"

# Both are stamped with the same image, so the CLI installs what was just built
# and the cases exercise the path an operator walks rather than one held open by
# a flag.
log "Building kg and the provider image"
make -C "${ROOT}" build \
  IMAGE="${PROVIDER_IMAGE%:*}" IMAGE_TAG="${PROVIDER_IMAGE##*:}" >/dev/null
make -C "${ROOT}" docker-build \
  IMAGE="${PROVIDER_IMAGE%:*}" IMAGE_TAG="${PROVIDER_IMAGE##*:}" >/dev/null
info "$(${KG} version)"

driver_provision

log "Checking that this machine can reach every host"
for host in $(host_names); do
  ip="$(driver_host_ip "${host}")"
  on_host "${ip}" true >/dev/null 2>&1 || fail "cannot reach ${host} at ${ip} over SSH"
  info "${host} ${ip}"
done

K8S_VERSION="$(driver_kubernetes_version)"
readonly K8S_VERSION
[[ "${K8S_VERSION}" == v${K8S_MINOR}.* ]] ||
  fail "the hosts report kubeadm ${K8S_VERSION}, which is not a ${K8S_MINOR} release"
info "the hosts have Kubernetes ${K8S_VERSION}"

log "Writing the configuration"
LAB_ENDPOINT="$(driver_endpoint 1 kg-cp-1)"
readonly LAB_ENDPOINT
write_cni "${WORKDIR}/kindnet.yaml" "${LAB_ENDPOINT}:6443"
# Every host the fleet has, and fewer replicas than that: a pool with nothing
# spare in it is one a rollout cannot move onto and a handover is refused for.
write_config "${CONFIG}" lab "${LAB_ENDPOINT}" "${WORKDIR}/kindnet.yaml" \
  1 "$(cluster_cp_hosts)" 1 "${WORKER_HOSTS}" \
  "${CONTROL_PLANE_REPLICAS}" "${WORKER_REPLICAS}"
"${KG}" config validate -c "${CONFIG}"

# ---------------------------------------------------------------------------
# Scenarios

started="$(date +%s)"
ran=() skipped=()
for name in "${selected[@]}"; do
  printf '\n\033[1m%s\033[0m\n' "############ scenario: ${name}"

  missing="$(missing_capabilities "${name}")"
  if [[ -n "${missing}" ]]; then
    skip "the ${DRIVER} fleet gives no ${missing}"
    skipped+=("${name}")
    report_result "${name}" skipped 0
    continue
  fi

  active_scenario="${name}"
  scenario_started="$(date +%s)"
  "scenario_${name//-/_}"
  info "scenario ${name} passed in $(( $(date +%s) - scenario_started ))s"
  ran+=("${name}")
  report_result "${name}" passed "$(( $(date +%s) - scenario_started ))"
done

log "Scenarios passed"
info "${#ran[@]} on the ${DRIVER} fleet in $(( ($(date +%s) - started) / 60 ))m: ${ran[*]:-none}"
((${#skipped[@]} > 0)) && info "${#skipped[@]} skipped: ${skipped[*]}"
exit 0
