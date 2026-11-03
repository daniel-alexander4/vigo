// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math"
	"math/bits"
	"sync/atomic"
)

// Histogram is a lock-free power-of-2 bucketed latency histogram. Each
// bucket counts observations whose latency in nanoseconds falls in
// [2^i, 2^(i+1)). 64 buckets cover the full range of int64 nanoseconds
// (1 ns to ~292 years).
//
// A single atomic.Int64.Add per observation — sub-microsecond,
// allocation-free, no shared mutex — so the measurement doesn't distort
// the thing it measures. Percentiles are computed from bucket counts at
// snapshot time. Bucket-edge interpolation reports the geometric
// mid-point of the bucket a percentile lands in (~±50% of bucket width).
type Histogram struct {
	buckets [64]atomic.Int64
}

// Record adds one observation. ns <= 0 is clamped to bucket 0.
func (h *Histogram) Record(ns int64) {
	if ns <= 0 {
		h.buckets[0].Add(1)
		return
	}
	b := 63 - bits.LeadingZeros64(uint64(ns))
	h.buckets[b].Add(1)
}

// Snapshot returns a copy of the bucket counts at this instant.
func (h *Histogram) Snapshot() Snapshot {
	var s Snapshot
	for i := range h.buckets {
		s.buckets[i] = h.buckets[i].Load()
	}
	return s
}

// SnapshotAndReset returns the current counts and atomically resets every
// bucket to zero — the per-second-tick read path.
func (h *Histogram) SnapshotAndReset() Snapshot {
	var s Snapshot
	for i := range h.buckets {
		s.buckets[i] = h.buckets[i].Swap(0)
	}
	return s
}

// Snapshot is a point-in-time copy of the histogram's bucket counts.
type Snapshot struct {
	buckets [64]int64
}

// Total returns the total number of observations in the snapshot.
func (s Snapshot) Total() int64 {
	var t int64
	for _, c := range s.buckets {
		t += c
	}
	return t
}

// Percentile returns the latency at the given percentile (0-100) in
// nanoseconds (geometric mid-point of the landing bucket). 0 when empty.
func (s Snapshot) Percentile(pct float64) int64 {
	total := s.Total()
	if total == 0 {
		return 0
	}
	target := int64(math.Ceil(float64(total) * pct / 100))
	if target < 1 {
		target = 1
	}
	var cum int64
	for i, c := range s.buckets {
		cum += c
		if cum >= target {
			if i == 0 {
				return 1
			}
			return int64(math.Round(math.Sqrt2 * math.Pow(2, float64(i))))
		}
	}
	return int64(math.Round(math.Sqrt2 * math.Pow(2, 63)))
}

// Max returns the upper bound of the highest non-empty bucket.
func (s Snapshot) Max() int64 {
	for i := len(s.buckets) - 1; i >= 0; i-- {
		if s.buckets[i] > 0 {
			if i == 63 {
				return math.MaxInt64
			}
			return int64(1) << (i + 1)
		}
	}
	return 0
}
