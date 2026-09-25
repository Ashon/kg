#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT
#
# A fleet of containers on one docker bridge.
#
# kg reaches a host over SSH and nothing else, so a container that answers
# on port 22 and can run kubeadm is indistinguishable from a real machine as far
# as the provider is concerned. kindest/node already carries systemd, containerd,
# kubeadm, kubelet and the control plane images, so the only thing missing is
# sshd.
#
# What it cannot give is a separate kernel or a separate network stack, so the
# scenarios that need those declare it and are skipped here rather than passing
# on something that did not happen.

# A runner has four cores and sixteen gigabytes for the fleet, the bootstrap
# cluster and the build, so the fleet is the smallest that still exercises every
# case: two control plane hosts because multi-cluster needs one each, and two
# workers because a scale-out needs somewhere to go.
CONTROL_PLANE_HOSTS="${CONTROL_PLANE_HOSTS:-2}"
CONTROL_PLANE_REPLICAS="${CONTROL_PLANE_REPLICAS:-1}"
WORKER_HOSTS="${WORKER_HOSTS:-2}"
WORKER_REPLICAS="${WORKER_REPLICAS:-1}"

NODE_IMAGE="${NODE_IMAGE:-kindest/node:v1.33.1}"
HOST_IMAGE="${HOST_IMAGE:-kg-host:scenarios}"
# The bootstrap cluster is a kind cluster, and kind puts its node on a network
# of its own. The provider reaches the hosts from inside that cluster, so the
# hosts have to be on the same bridge or every one of them reads as unreachable.
NETWORK="${NETWORK:-kind}"

driver_name() { echo docker; }

# No VIP: kube-vip elects over ARP, and these share one bridge and one kernel.
# No reboot either - stopping a container is not a machine going away, because
# its state lives in a volume the next start reattaches.
driver_capabilities() { echo ""; }

driver_host_ip() {
  # index rather than a field path: a docker network name may contain a hyphen,
  # which a Go template cannot read as an identifier.
  docker inspect -f "{{(index .NetworkSettings.Networks \"${NETWORK}\").IPAddress}}" "$1"
}

driver_node_name() { echo "$1"; }

# Without a VIP the endpoint is the cluster's own first control plane, which is
# why the caller says which host that is.
driver_endpoint() { driver_host_ip "${2:-kg-cp-1}"; }

# Nothing ever raises an address here, so a wipe has none to take back.
driver_vip_addresses() { :; }

driver_cluster_extra() {
  cat <<'EOF'
  virtualIP:
    enabled: false
  # The hosts are containers, so kubeadm cannot read the kernel config to verify
  # it. Nothing else here is relaxed.
  ignorePreflightErrors:
    - SystemVerification

  # net.netfilter.nf_conntrack_max is global, not per network namespace, so a
  # container must not set it and kube-proxy dies trying. kind works around this
  # the same way. Appended only where the init configuration exists: a join
  # configuration must not carry it.
  preKubeadmCommands:
    - |
      if [ -f /run/kubeadm/kubeadm.yaml ]; then
        printf '%s\n' \
          '---' \
          'apiVersion: kubeproxy.config.k8s.io/v1alpha1' \
          'kind: KubeProxyConfiguration' \
          'conntrack:' \
          '  maxPerCore: 0' \
          >> /run/kubeadm/kubeadm.yaml
      fi
EOF
}

driver_preflight() {
  command -v docker >/dev/null || fail "docker is not installed"
  docker info >/dev/null 2>&1 || fail "the docker daemon is not reachable"

  # A bootstrap cluster plus one systemd container per host exhausts the default
  # inotify budget. kube-proxy then dies with "too many open files" and nothing
  # in the cluster can reach the API server. It is a property of the machine, so
  # it is checked rather than changed.
  local instances
  instances="$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null || echo 0)"
  if [[ "${instances}" -gt 0 && "${instances}" -lt 1024 ]]; then
    fail "fs.inotify.max_user_instances is ${instances}; a fleet of systemd containers needs
    at least 1024. Raise it with:

      sudo sysctl -w fs.inotify.max_user_instances=8192"
  fi
}

driver_provision() {
  local host attempt

  log "Building the host image from ${NODE_IMAGE}"
  docker build -q \
    --build-arg "NODE_IMAGE=${NODE_IMAGE}" \
    --build-arg "AUTHORIZED_KEY=${PUBKEY}" \
    -t "${HOST_IMAGE}" "${ROOT}/test/assets/host" >/dev/null
  info "${HOST_IMAGE}"

  log "Starting $(host_names | wc -l | tr -d ' ') host container(s)"
  docker network create "${NETWORK}" >/dev/null 2>&1 || true

  for host in $(host_names); do
    docker rm -f "${host}" >/dev/null 2>&1 || true
    # These are the flags kind runs its own nodes with, and they are not
    # interchangeable: --tty is what the entrypoint logs through, --init=false
    # keeps the entrypoint as PID 1 instead of docker-init, and /lib/modules is
    # what lets the bootstrap script load br_netfilter as it would on a host.
    docker run --detach --tty \
      --name "${host}" --hostname "${host}" --network "${NETWORK}" \
      --privileged \
      --security-opt seccomp=unconfined --security-opt apparmor=unconfined \
      --cgroupns=private --init=false \
      --tmpfs /tmp --tmpfs /run --volume /var \
      --volume /lib/modules:/lib/modules:ro \
      --restart=on-failure:1 \
      "${HOST_IMAGE}" >/dev/null
  done

  for host in $(host_names); do
    for attempt in {1..60}; do
      if docker exec "${host}" systemctl is-active ssh >/dev/null 2>&1; then
        info "${host} $(driver_host_ip "${host}")"
        break
      fi
      ((attempt == 60)) && fail "sshd never came up on ${host}"
      sleep 2
    done
  done

  # The genesis node is this machine, and it has to reach the hosts over SSH and
  # the cluster it builds over the API. On Linux the docker bridge is routable
  # and it does. On Docker Desktop the bridge lives inside a virtual machine and
  # it does not, so the fleet comes up and nothing can be done with it.
  on_host "$(driver_host_ip kg-cp-1)" true >/dev/null 2>&1 || fail \
"the hosts are up but this machine cannot reach them on the ${NETWORK} bridge.

    On Linux that bridge is routable. On Docker Desktop it lives inside a
    virtual machine, so the genesis node cannot open SSH to a host and kubectl
    cannot reach the cluster it builds.

    Run these on Linux, or use the lima driver here:

      test/scenarios/run.sh"
}

driver_kubernetes_version() {
  docker exec kg-cp-1 kubeadm version -o short 2>/dev/null | tr -d '[:space:]'
}

driver_stop()  { docker stop -t 5 "$1" >/dev/null 2>&1; }
driver_start() { docker start "$1" >/dev/null 2>&1; }

driver_teardown() {
  local host
  for host in $(host_names); do
    docker rm -f "${host}" >/dev/null 2>&1 || true
  done
  # The network is kind's, not this suite's, so it is left alone.
}

driver_diagnostics() {
  local host
  for host in $(host_names); do
    docker inspect "${host}" >/dev/null 2>&1 || continue
    echo
    info "--- ${host}: bootstrap log"
    docker exec "${host}" tail -n 40 /var/log/kgenesis-bootstrap.log 2>&1 |
      sed 's/^/    /' || true
    echo
    info "--- ${host}: kubelet"
    docker exec "${host}" journalctl -u kubelet --no-pager -n 40 2>&1 |
      sed 's/^/    /' || true
  done
}
