// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// sliceBounds partitions a fleet of n hosts into the contiguous shard named by
// spec ("i/N", 1-based — VM i of N) and returns the [start, end) range this VM
// drives. An empty spec means the whole fleet ([0, n)).
//
// The partition is exact: the N shards of any n tile [0, n) with no gaps and no
// overlap (start = (i-1)·n/N, end = i·n/N), and shard sizes differ by at most 1.
// This is what lets several harness VMs each run the SAME create'd manifest with
// a different --slice and collectively drive every envoy exactly once — no
// shared state, no coordination beyond handing each VM its i/N.
func sliceBounds(n int, spec string) (start, end int, err error) {
	if strings.TrimSpace(spec) == "" {
		return 0, n, nil
	}
	idx, total, ok := parseSlice(spec)
	if !ok {
		return 0, 0, fmt.Errorf("invalid --slice %q: want \"i/N\" (1-based), e.g. 2/5", spec)
	}
	if idx < 1 || idx > total {
		return 0, 0, fmt.Errorf("invalid --slice %q: i must be in 1..N (got i=%d, N=%d)", spec, idx, total)
	}
	return (idx - 1) * n / total, idx * n / total, nil
}

// sliceLabel renders a human-readable " (slice i/N)" suffix for the run banner,
// or "" for the whole-fleet (empty) case.
func sliceLabel(spec string) string {
	if strings.TrimSpace(spec) == "" {
		return ""
	}
	return " (slice " + strings.TrimSpace(spec) + ")"
}

// parseSlice splits "i/N" into its two positive integers. ok is false on any
// malformed input (no slash, non-numeric, N < 1).
func parseSlice(spec string) (idx, total int, ok bool) {
	a, b, found := strings.Cut(spec, "/")
	if !found {
		return 0, 0, false
	}
	idx, err1 := strconv.Atoi(strings.TrimSpace(a))
	total, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || total < 1 {
		return 0, 0, false
	}
	return idx, total, true
}
