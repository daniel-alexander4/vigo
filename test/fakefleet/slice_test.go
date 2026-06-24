// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import "testing"

// TestSliceBoundsTiling is the load-bearing property: across all N shards of a
// fleet, the [start,end) ranges must tile [0,n) exactly — every envoy driven by
// exactly one VM, none dropped, none doubled. A gap silently under-drives the
// fleet; an overlap double-drives envoys (duplicate identities → server churn).
func TestSliceBoundsTiling(t *testing.T) {
	for _, n := range []int{0, 1, 4, 5, 7, 250000, 250003} {
		for _, total := range []int{1, 3, 5, 8} {
			covered := make([]int, n)
			prevEnd := 0
			for i := 1; i <= total; i++ {
				spec := itoa(i) + "/" + itoa(total)
				start, end, err := sliceBounds(n, spec)
				if err != nil {
					t.Fatalf("n=%d %s: unexpected err %v", n, spec, err)
				}
				if start != prevEnd {
					t.Errorf("n=%d %s: start %d != previous end %d (gap or overlap)", n, spec, start, prevEnd)
				}
				if end < start || end > n {
					t.Errorf("n=%d %s: bad range [%d,%d) for n=%d", n, spec, start, end, n)
				}
				for j := start; j < end; j++ {
					covered[j]++
				}
				prevEnd = end
			}
			if prevEnd != n {
				t.Errorf("n=%d total=%d: last shard ends at %d, want n=%d", n, total, prevEnd, n)
			}
			for j, c := range covered {
				if c != 1 {
					t.Errorf("n=%d total=%d: envoy %d covered %d times, want exactly 1", n, total, j, c)
				}
			}
		}
	}
}

func TestSliceBoundsEmptyAndInvalid(t *testing.T) {
	// Empty spec → whole fleet.
	if s, e, err := sliceBounds(100, ""); s != 0 || e != 100 || err != nil {
		t.Errorf(`sliceBounds(100,"") = (%d,%d,%v), want (0,100,nil)`, s, e, err)
	}
	for _, bad := range []string{"5", "abc", "2/", "/5", "0/5", "6/5", "-1/5", "3/0", "2/3/4"} {
		if _, _, err := sliceBounds(100, bad); err == nil {
			t.Errorf("sliceBounds(100,%q): want error, got nil", bad)
		}
	}
}

// itoa is a tiny local helper so the test needs no strconv import noise.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
