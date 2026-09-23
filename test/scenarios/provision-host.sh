#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# Prepares a machine to be a kgenesis host: root SSH, and the packages kgenesis
# expects to already be there.
#
#   provision-host.sh <address> <interface> <kubernetes minor> <authorized key>
#
# An empty interface leaves the network alone, for a fleet whose addresses come
# from somewhere else.
#
# kgenesis does not install a container runtime or kubeadm. It bootstraps hosts
# that are already provisioned, so this script plays the part of whatever builds
# a site's machine image.
set -euo pipefail

# The address is assigned rather than requested where the fleet's network has no
# DHCP to answer, and left alone where it does. An empty interface means the
# machine already has the address it should have.
STATIC_IP="$1"
NETWORK_IFACE="$2"
K8S_MINOR="$3"
AUTHORIZED_KEY="$4"

if [ -n "${NETWORK_IFACE}" ]; then
  cat > /etc/netplan/99-kgenesis.yaml <<EOF
network:
  version: 2
  ethernets:
    ${NETWORK_IFACE}:
      dhcp4: false
      addresses: [${STATIC_IP}/24]
EOF
  chmod 600 /etc/netplan/99-kgenesis.yaml
  netplan apply
fi

# Some hypervisors hand the guest a fresh cloud-init instance id on every start,
# so cloud-init treats each boot as a new machine and regenerates the SSH host
# keys. Real hardware does not change identity when it reboots, and kgenesis pins
# the key it first saw, so a host that came back from a reboot would be refused
# for good. Keeping the keys is what makes the fleet behave like the thing it
# stands in for.
cat > /etc/cloud/cloud.cfg.d/99-kgenesis-hostkeys.cfg <<'EOF'
ssh_deletekeys: false
EOF

install -d -m 0700 /root/.ssh
printf '%s\n' "${AUTHORIZED_KEY}" > /root/.ssh/authorized_keys
chmod 600 /root/.ssh/authorized_keys
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config
systemctl restart ssh

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
  apt-transport-https ca-certificates curl gpg containerd conntrack socat ethtool iproute2

# containerd from Ubuntu ships a minimal config; kubeadm needs the systemd
# cgroup driver and a sandbox image it agrees with.
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl restart containerd
systemctl enable containerd

curl -fsSL "https://pkgs.k8s.io/core:/stable:/v${K8S_MINOR}/deb/Release.key" |
  gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v${K8S_MINOR}/deb/ /" \
  > /etc/apt/sources.list.d/kubernetes.list

apt-get update -qq
apt-get install -y -qq kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# kubeadm starts the kubelet itself, with a configuration it writes. Leaving it
# enabled but stopped is the state a freshly imaged host is in.
systemctl enable kubelet
systemctl stop kubelet || true

echo "host ready: $(hostname) kubeadm $(kubeadm version -o short)"
