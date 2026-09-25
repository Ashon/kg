#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/logging.sh"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

# Bare wait is used by fleet provisioning, wiping and package upgrades. It must
# join the actual background task without waiting for the log collector.
run_logged "${test_dir}/success.log" bash -c '
  sleep 0.1 &
  wait
  echo background-finished
  echo diagnostic >&2
' > "${test_dir}/console"
grep -qx background-finished "${test_dir}/success.log"
grep -qx diagnostic "${test_dir}/success.log"
cmp "${test_dir}/success.log" "${test_dir}/console"

status=0
run_logged "${test_dir}/failure.log" bash -c 'echo failed; exit 17' >/dev/null || status=$?
[[ "${status}" == 17 ]]
grep -qx failed "${test_dir}/failure.log"

# A logger failure must not produce a green run either.
status=0
run_logged "${test_dir}/missing/run.log" bash -c 'echo complete' >/dev/null 2>&1 || status=$?
[[ "${status}" != 0 ]]
echo 'Scenario logging checks passed'
