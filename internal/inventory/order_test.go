// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package inventory

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
)

// A fleet numbers its machines, and past the ninth a plain string order stops
// matching the numbering: kg-worker-10 would come before kg-worker-2 and the
// rollout would look like it skipped a machine.
func TestCompareNamesReadsDigitsAsNumbers(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kg-worker-2", "kg-worker-10", -1},
		{"kg-worker-10", "kg-worker-2", 1},
		{"kg-cp-1", "kg-cp-1", 0},
		{"kg-cp-1", "kg-worker-1", -1},
		{"kg-worker-9", "kg-worker-9a", -1},
		{"host-007", "host-8", -1},
		// Same number, written two ways. Something has to decide, and whatever
		// it decides has to be the same on every read.
		{"host-07", "host-7", -1},
		{"host", "host-1", -1},
		{"", "a", -1},
	}

	for _, tc := range cases {
		if got := CompareNames(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareNames(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortHostsPutsThePoolInReadingOrder(t *testing.T) {
	hosts := []infrav1.Host{
		{ObjectMeta: metav1.ObjectMeta{Name: "kg-worker-10"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kg-cp-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kg-worker-2"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "kg-cp-1"}},
	}

	SortHosts(hosts)

	want := []string{"kg-cp-1", "kg-cp-2", "kg-worker-2", "kg-worker-10"}
	for i, name := range want {
		if hosts[i].Name != name {
			t.Fatalf("position %d is %s, want %s (got %v)", i, hosts[i].Name, name, names(hosts))
		}
	}
}

func names(hosts []infrav1.Host) []string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Name)
	}
	return out
}
