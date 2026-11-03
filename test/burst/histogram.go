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
// The simple-loadtest replacement: the existing tool does
// `mu.Lock(); slice = append(slice, ns); mu.Unlock()` on every CheckIn,
// which adds lock contention + GC pressure on the client side and
// distorts the measurement it's trying to take. This histogram does a
// single atomic.Int64.Add to the matching bucket — sub-microsecond,
// allocation-free, no shared mutex. Percentiles are computed from
// bucket counts at snapshot time.
//
// Bucket-edge interpolation: percentiles are reported as the geometric
// mid-point of the bucket they land in, so resolution is roughly ±50%
// of the bucket width. For latencies in the 10-100 ms band that's a
// resolution of ~10 ms — fine for steady-state percentile tracking,
// not for sub-millisecond p99 distinctions. The trade is exact bucket
// accuracy + lock-free hot path against bucket-edge granularity at the
// reporting layer.
type Histogram struct {
	buckets [64]atomic.Int64
}

// Record adds one observation. ns must be non-negative; negative or
// zero values are clamped to bucket 0.
func (h *Histogram) Record(ns int64) {
	if ns <= 0 {
		h.buckets[0].Add(1)
		return
	}
	// Highest bit set = floor(log2(ns)). 1 ns → bucket 0, 1023 ns →
	// bucket 9, 1024 ns → bucket 10, etc.
	b := 63 - bits.LeadingZeros64(uint64(ns))
	h.buckets[b].Add(1)
}

// Snapshot returns a copy of the bucket counts at this instant. The
// histogram continues accumulating new observations afterward; this is
// the per-second-tick read path. Caller may compute percentiles from
// the returned counts.
func (h *Histogram) Snapshot() Snapshot {
	var s Snapshot
	for i := range h.buckets {
		s.buckets[i] = h.buckets[i].Load()
	}
	return s
}

// SnapshotAndReset returns the current counts and atomically resets
// every bucket to zero. Used by the per-second time-series writer:
// each tick reads + resets, so the next second's percentiles cover
// only that second's traffic.
func (h *Histogram) SnapshotAndReset() Snapshot {
	var s Snapshot
	for i := range h.buckets {
		s.buckets[i] = h.buckets[i].Swap(0)
	}
	return s
}

// Snapshot is a point-in-time copy of the histogram's bucket counts.
// Decoupled from the live Histogram so percentile computation doesn't
// race with new Record calls.
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
// nanoseconds. Returns 0 when the snapshot is empty. The reported
// value is the geometric mid-point of the bucket the percentile lands
// in — resolution is ~±50% of bucket width.
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
			// Geometric mid of [2^i, 2^(i+1)) is 2^(i+0.5) ≈ 1.414 * 2^i.
			if i == 0 {
				return 1
			}
			return int64(math.Round(math.Sqrt2 * math.Pow(2, float64(i))))
		}
	}
	// Shouldn't reach here when total > 0; fall back to the top bucket
	// mid-point.
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

// Merge accumulates other into s. Used to fold per-trial snapshots
// into aggregate baseline / burst / cooldown buckets.
func (s *Snapshot) Merge(other Snapshot) {
	for i := range s.buckets {
		s.buckets[i] += other.buckets[i]
	}
}
