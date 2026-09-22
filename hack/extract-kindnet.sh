#!/usr/bin/env bash
# Extracts kindnet from a throwaway kind cluster into test/e2e/kindnet.yaml.
#
# The end-to-end test needs a CNI for the cluster it builds. kindnet suits it
# better than a chart: kindest/node already carries the kindnetd image, so the
# test installs a CNI without reaching the network, and it routes between node
# pod CIDRs on a shared bridge, which is exactly what the fake hosts are on.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="${root}/test/e2e/kindnet.yaml"
node_image="${NODE_IMAGE:-kindest/node:v1.33.1}"
cluster="kindnet-extract-$$"

cleanup() { kind delete cluster --name "${cluster}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "Creating a throwaway cluster from ${node_image}"
kind create cluster --name "${cluster}" --image "${node_image}" --wait 120s >/dev/null

ctx="kind-${cluster}"
tmp="$(mktemp -d)"

kubectl --context "${ctx}" -n kube-system get serviceaccount kindnet -o yaml  > "${tmp}/01-sa.yaml"
kubectl --context "${ctx}" get clusterrole kindnet -o yaml                    > "${tmp}/02-cr.yaml"
kubectl --context "${ctx}" get clusterrolebinding kindnet -o yaml             > "${tmp}/03-crb.yaml"
kubectl --context "${ctx}" -n kube-system get daemonset kindnet -o yaml       > "${tmp}/04-ds.yaml"

{
  echo "# kindnet, extracted from a kind cluster by hack/extract-kindnet.sh."
  echo "# Vendored so the end-to-end test needs no network: the kindnetd image is"
  echo "# already inside the kindest/node image the fake hosts are built from."
  echo "# Source image: ${node_image}"
  python3 "${root}/hack/strip-cluster-fields.py" "${tmp}"/*.yaml
} > "${out}"

echo "Wrote ${out}"
