// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
)

func releasedHost(name, address string, reset metav1.ConditionStatus, reason, message string) *infrav1.Host {
	return &infrav1.Host{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "lab"},
		Spec:       infrav1.HostSpec{Address: address},
		Status: infrav1.HostStatus{
			Phase: infrav1.HostPhasePending,
			Conditions: []metav1.Condition{{
				Type:    infrav1.HostResetCondition,
				Status:  reset,
				Reason:  reason,
				Message: message,
			}},
		},
	}
}

// A host is released whether or not kgenesis could reach it, so "back in the
// pool" has to distinguish the two. A host that still carries the last cluster's
// certificates and etcd data will send the next kubeadm join somewhere strange,
// and the operator has no way to know unless the delete says so.
func TestReportReleasedHostsNamesTheOnesItCouldNotReset(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			releasedHost("kg-cp-1", "10.0.0.1", metav1.ConditionTrue, infrav1.ReasonResetSucceeded, "removed"),
			releasedHost("kg-cp-2", "10.0.0.2", metav1.ConditionFalse, infrav1.ReasonHostUnreachable,
				"dial 10.0.0.2:22: i/o timeout"),
			releasedHost("kg-cp-3", "10.0.0.3", metav1.ConditionFalse, infrav1.ReasonResetIncomplete,
				"reset host: exit status 1"),
		).
		Build()

	var out bytes.Buffer
	key := types.NamespacedName{Namespace: "lab", Name: "lab"}
	if err := reportReleasedHosts(context.Background(), c, key, &out); err != nil {
		t.Fatalf("reportReleasedHosts: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"1 host(s) back in the pool",
		"2 host(s) were released without being reset",
		"kg-cp-2 (10.0.0.2): dial 10.0.0.2:22: i/o timeout",
		"kg-cp-3 (10.0.0.3): reset host: exit status 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "kg-cp-1 (") {
		t.Errorf("a host that was reset was reported as a problem:\n%s", got)
	}
}

// Nothing to warn about should read as nothing to warn about.
func TestReportReleasedHostsStaysQuietWhenEveryResetWorked(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(
			releasedHost("kg-cp-1", "10.0.0.1", metav1.ConditionTrue, infrav1.ReasonResetSucceeded, "removed"),
			releasedHost("kg-cp-2", "10.0.0.2", metav1.ConditionTrue, infrav1.ReasonResetSucceeded, "removed"),
		).
		Build()

	var out bytes.Buffer
	key := types.NamespacedName{Namespace: "lab", Name: "lab"}
	if err := reportReleasedHosts(context.Background(), c, key, &out); err != nil {
		t.Fatalf("reportReleasedHosts: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "2 host(s) back in the pool") {
		t.Errorf("expected both hosts reported as back:\n%s", got)
	}
	if strings.Contains(got, "without being reset") {
		t.Errorf("a clean teardown carried a warning:\n%s", got)
	}
}
