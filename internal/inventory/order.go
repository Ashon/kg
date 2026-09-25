// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Package inventory holds the order the host pool is read and handed out in.
//
// A pool that hands out hosts in an arbitrary order still builds a working
// cluster, but nobody can say which host a rollout moves onto next, and two runs
// of the same scenario land on different machines. One order, used everywhere a
// host list is shown or walked, is what makes that predictable.
package inventory

import (
	"sort"
	"strings"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
)

// SortHosts puts hosts into the order they are listed and claimed in.
func SortHosts(hosts []infrav1.Host) {
	sort.SliceStable(hosts, func(i, j int) bool {
		return CompareNames(hosts[i].Name, hosts[j].Name) < 0
	})
}

// CompareNames orders two names the way a person reading the fleet would: the
// digits in a name count as a number, so kg-worker-2 comes before kg-worker-10
// rather than after it.
func CompareNames(a, b string) int {
	for a != "" && b != "" {
		ahead, arest := chunk(a)
		bhead, brest := chunk(b)

		if isDigits(ahead) && isDigits(bhead) {
			if c := compareNumbers(ahead, bhead); c != 0 {
				return c
			}
		} else if c := strings.Compare(ahead, bhead); c != 0 {
			return c
		}

		a, b = arest, brest
	}

	// Whichever name ran out first is the shorter of two that agree so far.
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

// chunk splits off the leading run of digits, or the leading run of everything
// that is not a digit.
func chunk(s string) (head, rest string) {
	digits := isDigit(s[0])
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) != digits {
			return s[:i], s[i:]
		}
	}
	return s, ""
}

// compareNumbers compares two runs of digits as numbers, without converting
// them: a fleet's names are short, but nothing stops one from carrying a number
// too large to parse. Leading zeros decide nothing but a tie.
func compareNumbers(a, b string) int {
	na, nb := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(na) != len(nb) {
		if len(na) < len(nb) {
			return -1
		}
		return 1
	}
	if c := strings.Compare(na, nb); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return s != ""
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
