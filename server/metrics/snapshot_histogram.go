// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"sort"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// SnapshotHistogram is a histogram-shaped gauge: each Set replaces the whole
// distribution with the values of one moment, and a scrape exposes exactly
// that set. A plain prometheus.Histogram is cumulative, so re-observing every
// envoy on every collector tick makes _count grow by the fleet size per tick
// and ties any quantile to the tick rate. Query it without rate():
// histogram_quantile(0.5, vigo_checkin_age_seconds_bucket).
type SnapshotHistogram struct {
	desc    *prometheus.Desc
	buckets []float64 // sorted upper bounds

	mu     sync.Mutex
	counts map[float64]uint64 // cumulative, keyed by upper bound
	count  uint64
	sum    float64
}

// NewSnapshotHistogram builds an empty snapshot histogram. Register it with
// the registry; it implements prometheus.Collector.
func NewSnapshotHistogram(name, help string, buckets []float64) *SnapshotHistogram {
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	h := &SnapshotHistogram{
		desc:    prometheus.NewDesc(name, help, nil, nil),
		buckets: b,
	}
	h.Set(nil)
	return h
}

// Set replaces the distribution with values.
func (h *SnapshotHistogram) Set(values []float64) {
	counts := make(map[float64]uint64, len(h.buckets))
	for _, ub := range h.buckets {
		counts[ub] = 0
	}
	var sum float64
	for _, v := range values {
		sum += v
		for _, ub := range h.buckets {
			if v <= ub {
				counts[ub]++
			}
		}
	}
	h.mu.Lock()
	h.counts = counts
	h.count = uint64(len(values))
	h.sum = sum
	h.mu.Unlock()
}

// Describe implements prometheus.Collector.
func (h *SnapshotHistogram) Describe(ch chan<- *prometheus.Desc) { ch <- h.desc }

// Collect implements prometheus.Collector.
func (h *SnapshotHistogram) Collect(ch chan<- prometheus.Metric) {
	h.mu.Lock()
	counts := make(map[float64]uint64, len(h.counts))
	for k, v := range h.counts {
		counts[k] = v
	}
	count, sum := h.count, h.sum
	h.mu.Unlock()
	ch <- prometheus.MustNewConstHistogram(h.desc, count, sum, counts)
}
