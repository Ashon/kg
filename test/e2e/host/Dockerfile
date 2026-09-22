# A stand-in for a pre-provisioned host, for end-to-end tests.
#
# kgenesis reaches hosts over SSH and nothing else, so a container that answers
# on port 22 and can run kubeadm is indistinguishable from a real machine as far
# as the provider is concerned. kindest/node already carries systemd, containerd,
# kubeadm, kubelet and the control plane images for its Kubernetes version, so
# the only thing missing is sshd.
ARG NODE_IMAGE=kindest/node:v1.33.1
FROM ${NODE_IMAGE}

RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd /root/.ssh \
 && chmod 700 /root/.ssh \
 && sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config \
 && sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && systemctl enable ssh

# The test harness injects its throwaway key here at build time.
ARG AUTHORIZED_KEY
RUN printf '%s\n' "${AUTHORIZED_KEY}" > /root/.ssh/authorized_keys \
 && chmod 600 /root/.ssh/authorized_keys

# kindest/node's entrypoint boots systemd, which then starts sshd. Keep it.
