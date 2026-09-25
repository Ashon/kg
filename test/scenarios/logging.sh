#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT

# Keep tee outside the scenario shell's job table: Linux bash includes process
# substitutions in a bare `wait`, which would wait forever for its own logger.
run_logged() (
  set +e
  local path="$1"
  shift
  "$@" 2>&1 | tee "${path}"
  local statuses=("${PIPESTATUS[@]}")
  ((statuses[0] == 0)) || exit "${statuses[0]}"
  exit "${statuses[1]}"
)
