#!/usr/bin/env bash
# Checks that every file kgenesis owns carries an SPDX header, and adds the
# missing ones with --fix.
#
# The headers follow the REUSE specification, so `SPDX-FileCopyrightText` and
# `SPDX-License-Identifier` are machine readable rather than a prose notice
# somebody has to interpret.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly COPYRIGHT="SPDX-FileCopyrightText: 2026 Ashon"
readonly LICENSE="SPDX-License-Identifier: MIT"

FIX=0
[[ "${1:-}" == "--fix" ]] && FIX=1

# Files that must not carry this header, and why.
#
#   zz_generated.deepcopy.go   controller-gen writes it from hack/boilerplate.go.txt
#   config/crd, config/rbac    controller-gen output, derived from headered sources
#   internal/assets/*.yaml     assembled by hack/build-components.sh, which writes its own
#   test/e2e/kindnet.yaml      vendored from kind, which is Apache 2.0, not ours to relicense
is_excluded() {
  case "$1" in
    *zz_generated*) return 0 ;;
    config/crd/*|config/rbac/role.yaml) return 0 ;;
    internal/assets/*.yaml) return 0 ;;
    test/e2e/kindnet.yaml) return 0 ;;
    *) return 1 ;;
  esac
}

# Files whose first line has to stay first.
has_shebang() {
  head -1 "$1" | grep -q '^#!'
}

missing=()

check_or_fix() {
  local file="$1" comment="$2" blank_after="$3"

  if grep -q "${LICENSE}" "${file}"; then
    return 0
  fi
  if ((FIX == 0)); then
    missing+=("${file}")
    return 0
  fi

  # Written through a temporary file and moved back, so the original's mode has
  # to be restored: mktemp creates 0600, and mv would carry that over and strip
  # the executable bit off every script this touches.
  local tmp mode
  tmp="$(mktemp)"
  mode="$(stat -f '%OLp' "${file}" 2>/dev/null || stat -c '%a' "${file}")"
  {
    if has_shebang "${file}"; then
      head -1 "${file}"
      echo "${comment} ${COPYRIGHT}"
      echo "${comment} ${LICENSE}"
      [[ "${blank_after}" == "blank" ]] && echo
      tail -n +2 "${file}"
    else
      echo "${comment} ${COPYRIGHT}"
      echo "${comment} ${LICENSE}"
      [[ "${blank_after}" == "blank" ]] && echo
      cat "${file}"
    fi
  } > "${tmp}"
  mv "${tmp}" "${file}"
  chmod "${mode}" "${file}"
  echo "  added: ${file}"
}

cd "${ROOT}"

while read -r file; do
  is_excluded "${file}" && continue
  case "${file}" in
    # Go needs the blank line: without it the header would be read as the
    # package doc comment, or merge into the one already there.
    *.go)                       check_or_fix "${file}" "//" blank ;;
    *.sh|*.py|Makefile|Dockerfile) check_or_fix "${file}" "#" blank ;;
    *.yaml|*.yml)               check_or_fix "${file}" "#" none ;;
  esac
done < <(git ls-files '*.go' '*.sh' '*.py' '*.yaml' '*.yml' Makefile Dockerfile)

if ((${#missing[@]} > 0)); then
  echo "These files have no SPDX header. Run 'make license-headers' to add them:"
  printf '  %s\n' "${missing[@]}"
  exit 1
fi

echo "Every file kgenesis owns carries an SPDX header."
