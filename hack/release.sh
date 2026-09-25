#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Builds everything a release hands over, into dist/.
#
#   hack/release.sh v0.1.0
#   VERSION=v0.1.0 hack/release.sh
#
# A CLI archive per platform, the provider manifest the CLI has embedded, and a
# checksum file covering both. The container image is not built here: it is
# multi-architecture and belongs to the workflow that can push it.
set -euo pipefail

readonly ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

VERSION="${1:-${VERSION:-}}"
[[ -n "${VERSION}" ]] || {
  echo "usage: $0 <version>   (for example v0.1.0)" >&2
  exit 2
}

IMAGE="${IMAGE:-ghcr.io/ashon/kg}"
readonly MANAGER_IMAGE="${IMAGE}:${VERSION}"
readonly DIST="${ROOT}/dist"

GIT_COMMIT="${GIT_COMMIT:-$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
# Taken from the commit rather than the clock, so building the same commit twice
# produces the same bytes.
BUILD_DATE="${BUILD_DATE:-$(git -C "${ROOT}" show -s --format=%cI HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)}"

# The platforms a genesis node is plausibly run from: an operator's laptop and a
# machine in the same rack as the hosts.
PLATFORMS="${PLATFORMS:-darwin/arm64 darwin/amd64 linux/amd64 linux/arm64}"

log()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
info() { printf '    %s\n' "$*"; }

rm -rf "${DIST}"
mkdir -p "${DIST}"

log "Building kg ${VERSION}"
info "controller image ${MANAGER_IMAGE}"

for platform in ${PLATFORMS}; do
  goos="${platform%%/*}"
  goarch="${platform##*/}"
  stage="${DIST}/stage/kg_${VERSION}_${goos}_${goarch}"
  mkdir -p "${stage}"

  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go build \
    -C "${ROOT}" -trimpath \
    -ldflags "-s -w \
      -X github.com/Ashon/kg/internal/version.Version=${VERSION} \
      -X github.com/Ashon/kg/internal/version.GitCommit=${GIT_COMMIT} \
      -X github.com/Ashon/kg/internal/version.BuildDate=${BUILD_DATE} \
      -X github.com/Ashon/kg/internal/version.Image=${MANAGER_IMAGE}" \
    -o "${stage}/kg" ./cmd/kg

  cp "${ROOT}/LICENSE" "${ROOT}/README.md" "${stage}/"
  # README links to the manuals; include them so release archives work offline.
  cp -R "${ROOT}/docs" "${stage}/docs"
  mkdir -p "${stage}/test/scenarios"
  cp "${ROOT}/test/scenarios/SCENARIOS.md" "${stage}/test/scenarios/"

  tar -czf "${DIST}/$(basename "${stage}").tar.gz" -C "${DIST}/stage" "$(basename "${stage}")"
  info "$(basename "${stage}").tar.gz"
done
rm -rf "${DIST}/stage"

# The manifest the CLI has embedded, with the image this release publishes, so
# it can be read - or applied - without the CLI.
log "Writing the provider manifest"
sed "s#^\( *image: \)${IMAGE}:.*#\1${MANAGER_IMAGE}#" \
  "${ROOT}/internal/assets/provider-components.yaml" > "${DIST}/provider-components.yaml"
grep -q "${MANAGER_IMAGE}" "${DIST}/provider-components.yaml" ||
  { echo "the manifest still names no ${MANAGER_IMAGE}" >&2; exit 1; }
info "provider-components.yaml"

log "Checksums"
(cd "${DIST}" && shasum -a 256 -- *.tar.gz provider-components.yaml > SHA256SUMS)
sed 's/^/    /' "${DIST}/SHA256SUMS"

log "Done"
info "${DIST}"
