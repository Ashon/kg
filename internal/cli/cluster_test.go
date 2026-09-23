// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/Ashon/kgenesis/api/v1alpha1"
)

func hostMachine(name string, condition *metav1.Condition, host string) *infrav1.HostMachine {
	hm := &infrav1.HostMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
	}
	if host != "" {
		hm.Status.HostRef = &corev1.LocalObjectReference{Name: host}
	}
	if condition != nil {
		hm.Status.Conditions = []metav1.Condition{*condition}
	}
	return hm
}

func condition(status metav1.ConditionStatus, reason, message string) *metav1.Condition {
	return &metav1.Condition{
		Type:    infrav1.HostMachineProvisionedCondition,
		Status:  status,
		Reason:  reason,
		Message: message,
	}
}

func TestBootstrapFailureDetection(t *testing.T) {
	cases := map[string]struct {
		machine   *infrav1.HostMachine
		wantFound bool
		wantHost  string
	}{
		"failed bootstrap": {
			machine: hostMachine("cp-0",
				condition(metav1.ConditionFalse, infrav1.ReasonBootstrapFailed, "exited 1"), "e2e-cp-1"),
			wantFound: true,
			wantHost:  "e2e-cp-1",
		},
		// Still working through the normal sequence, not a failure.
		"waiting for bootstrap data": {
			machine: hostMachine("cp-0",
				condition(metav1.ConditionFalse, infrav1.ReasonWaitingForBootstrapData, "running"), "e2e-cp-1"),
			wantFound: false,
		},
		"no matching host yet": {
			machine: hostMachine("cp-0",
				condition(metav1.ConditionFalse, infrav1.ReasonNoMatchingHost, "none available"), ""),
			wantFound: false,
		},
		"provisioned": {
			machine: hostMachine("cp-0",
				condition(metav1.ConditionTrue, infrav1.ReasonBootstrapSucceeded, "ok"), "e2e-cp-1"),
			wantFound: false,
		},
		"no conditions at all": {
			machine:   hostMachine("cp-0", nil, ""),
			wantFound: false,
		},
		// A failure before any host was claimed still has to be reported.
		"failed with no host": {
			machine: hostMachine("cp-0",
				condition(metav1.ConditionFalse, infrav1.ReasonBootstrapFailed, "cannot render"), ""),
			wantFound: true,
			wantHost:  "-",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			failure, found := bootstrapFailure(tc.machine)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if !found {
				return
			}
			if failure.host != tc.wantHost {
				t.Errorf("host = %q, want %q", failure.host, tc.wantHost)
			}
			if failure.machine != "cp-0" {
				t.Errorf("machine = %q, want cp-0", failure.machine)
			}
		})
	}
}

// The error is what an operator reads instead of waiting out the timeout, so it
// has to name every failed machine and carry the log the provider captured.
func TestBootstrapFailedErrorNamesEveryMachine(t *testing.T) {
	err := newBootstrapFailedError([]machineFailure{
		{machine: "cp-0", host: "rack1-01", message: "Bootstrap script exited 1. Last lines:\nkubelet not healthy"},
		{machine: "md-0", host: "rack1-02", message: "Bootstrap script exited 2"},
	})

	got := err.Error()
	for _, want := range []string{
		"2 machine(s)",
		"cp-0 on host rack1-01",
		"md-0 on host rack1-02",
		"kubelet not healthy",
		"/var/log/kgenesis-bootstrap.log",
		"will not retry",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("error is missing %q:\n%s", want, got)
		}
	}
}

// ready() must not be satisfied while nothing has been created yet.
func TestSummaryReadyRequiresMachines(t *testing.T) {
	cases := map[string]struct {
		summary summary
		want    bool
	}{
		"nothing yet": {
			summary: summary{controlPlaneDesired: 3},
		},
		"machines pending": {
			summary: summary{
				controlPlaneDesired: 1, controlPlaneReady: 1,
				machinesDesired: 2, machinesRunning: 1,
			},
		},
		"all running": {
			summary: summary{
				controlPlaneDesired: 1, controlPlaneReady: 1,
				machinesDesired: 2, machinesRunning: 2,
			},
			want: true,
		},
		// A provider creates its machines one at a time. Counting the ones that
		// exist makes a wait end early: every machine so far is running, and the
		// one still to be created is not counted as missing.
		"a machine has not been created yet": {
			summary: summary{
				controlPlaneDesired: 3, controlPlaneReady: 3,
				machines: make([]clusterv1.Machine, 5), machinesDesired: 6, machinesRunning: 5,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.summary.ready(); got != tc.want {
				t.Errorf("ready() = %v, want %v", got, tc.want)
			}
		})
	}
}

// "Nothing has been built yet" is an answer to `cluster status`, not a failure
// of it. Reporting it as an error makes a normal starting point look broken,
// and makes the command useless in a script that checks where things stand.
func TestBootstrapClusterExists(t *testing.T) {
	dir := t.TempDir()
	opts := &Options{StateDir: dir}

	if opts.bootstrapClusterExists() {
		t.Error("an empty state directory should not look initialised")
	}

	if err := os.WriteFile(opts.BootstrapKubeconfig(), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	if !opts.bootstrapClusterExists() {
		t.Error("a state directory holding a bootstrap kubeconfig should look initialised")
	}
}
