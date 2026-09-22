#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Prepares the genesis node: Docker, which is all kgenesis needs to stand up its
# bootstrap cluster.
set -euo pipefail

K8S_MINOR="$1"

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends ca-certificates curl gnupg

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "${VERSION_CODENAME}") stable" \
  > /etc/apt/sources.list.d/docker.list

apt-get update -qq
apt-get install -y -qq docker-ce docker-ce-cli containerd.io
systemctl enable --now docker

# kgenesis does not need kubectl, but this test does: it inspects the cluster
# that was built, and the genesis node is the only machine that can reach it.
curl -fsSL "https://pkgs.k8s.io/core:/stable:/v${K8S_MINOR}/deb/Release.key" |
  gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v${K8S_MINOR}/deb/ /" \
  > /etc/apt/sources.list.d/kubernetes.list
apt-get update -qq
apt-get install -y -qq kubectl

# kind needs the same inotify headroom here as anywhere else: a bootstrap
# cluster exhausts the default and kube-proxy dies with "too many open files".
sysctl -w fs.inotify.max_user_instances=8192 >/dev/null
sysctl -w fs.inotify.max_user_watches=1048576 >/dev/null
printf 'fs.inotify.max_user_instances=8192\nfs.inotify.max_user_watches=1048576\n' \
  > /etc/sysctl.d/99-kgenesis-inotify.conf

echo "genesis ready: docker $(docker --version | cut -d, -f1), kubectl $(kubectl version --client -o json 2>/dev/null | grep -o '"gitVersion":"[^"]*"' | head -1 | cut -d'"' -f4)"
