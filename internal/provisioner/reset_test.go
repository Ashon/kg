// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package provisioner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResetCleansOldIdentityWhenKubeadmTimesOut(t *testing.T) {
	host := newFakeHost()
	conn := connect(t, host)
	defer conn.Close()
	if err := Reset(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	script := host.files["/tmp/kgenesis-reset.sh"]
	if script == "" {
		t.Fatal("reset script was not sent")
	}
	root := t.TempDir()
	// Run the actual shell cleanup against an isolated filesystem and fake host
	// commands. The kubeadm timeout must not leave the previous cluster's CA identity.
	script = strings.NewReplacer("/etc/", root+"/etc/", "/var/lib/", root+"/var/lib/", "/run/", root+"/run/").Replace(script)
	files := []string{"etc/kubernetes/kubelet.conf", "var/lib/kubelet/pki/kubelet-client-current.pem", "var/lib/kubelet/config.yaml", "var/lib/kubelet/kubeadm-flags.env", "var/lib/kubelet/instance-config.yaml", "var/lib/etcd/member/data", "var/lib/kgenesis/bootstrap.exit"}
	for _, name := range append(files, "var/lib/kubelet/pods/mounted-volume/data") {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old state"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kubeadm", "timeout", "systemctl", "crictl", "ip", "iptables"} {
		body := "#!/bin/sh\nexit 0\n"
		if name == "timeout" {
			body = "#!/bin/sh\n[ \"$*\" = '--kill-after=5s 60s kubeadm reset --force' ] || exit 99\nprintf timed-out > \"$RESET_TRACE\"\nexit 124\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "RESET_TRACE="+filepath.Join(root, "trace"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reset failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "trace")); err != nil {
		t.Fatal("kubeadm timeout was not exercised", err)
	}
	for _, name := range files {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("old cluster state remains: %s (%v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "var/lib/kubelet/pods/mounted-volume/data")); err != nil {
		t.Fatal("cleanup traversed pod volumes", err)
	}
}
