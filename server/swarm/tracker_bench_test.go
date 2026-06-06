// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package swarm

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkPeerTrackerIngest measures the throughput ceiling of the single
// PeerTracker mutex under the concurrent-check-in shape that
// ingestSwarmTraits (server/grpc/agentstream.go:1015–1064) drives on every
// swarm-carrying check-in.
//
// WHY THIS EXISTS
//
// PeerTracker guards four maps with one sync.RWMutex. On a swarm-heavy fleet
// — many envoys, each holding several blobs, all checking in fast — every
// check-in takes that lock K× (once per held blob, via ReportComplete +
// SetChunkSources) plus a full-O(seeds) RevokedSeeds() scan. That is the same
// single-global-mutex shape that made TraitsBatcher.Mark the throughput wall
// before 3b83080f. The open question (pending: "Shard PeerTracker.mu") is
// whether this lock is actually contended enough to matter, and if so whether
// batching the per-blob calls into one lock-once ReportBlobs suffices or a
// sha256-sharded restructure is needed.
//
// This benchmark answers that question in isolation — no network, no gRPC, no
// SQLite — so the lock's own ceiling is the only thing measured. The fakefleet
// route would bury this signal under the network/DB costs we already know
// dominate end-to-end.
//
// HOW TO RUN
//
//	# Headline: per-check-in ingest cost + sustained check-ins/s at GOMAXPROCS.
//	go test ./server/swarm/ -run '^$' -bench BenchmarkPeerTrackerIngest -benchmem
//
//	# Contention signature: ns/op should stay flat as cores rise if the lock
//	# isn't the wall, and balloon if it is. This is the decisive read.
//	go test ./server/swarm/ -run '^$' -bench BenchmarkPeerTrackerIngest -cpu 1,2,4,8,16
//
//	# Quantify blocked-on-lock time directly.
//	go test ./server/swarm/ -run '^$' -bench BenchmarkPeerTrackerIngest \
//	    -mutexprofile mu.out && go tool pprof -top mu.out
//
// HOW TO READ IT
//
// Each iteration is one envoy's full swarm ingest for one check-in. The
// reported "checkins/s" metric is the sustained ingest rate at the tested
// concurrency. Compare it to the scale target: the fleet must sustain
// ~25k check-ins/s, and each swarm-carrying check-in does K blob updates, so
// the lock must clear ~25_000×K blob-update operations per second.
//
//   - checkins/s comfortably above 25k at the realistic K, and ns/op roughly
//     flat across -cpu → the lock is NOT the wall. Close the pending item;
//     build neither the batch nor the shard.
//   - checkins/s near/below target, but ns/op flat across -cpu → throughput is
//     bounded by raw work-per-check-in, not contention → batch first
//     (ReportBlobs locks once: K acquisitions → 1), shard only if that doesn't
//     clear it.
//   - ns/op climbs sharply with cores and the mutex profile shows PeerTracker
//     dominating → genuine contention → the sha256-shard restructure is
//     justified.
//
// The seedBlobs=1000 cases additionally surface the RevokedSeeds() full-scan
// cost: that O(seeds) loop runs under RLock on EVERY check-in regardless of K,
// so if the 1000-blob rows are much slower than the 100-blob rows at the same
// K, the scan — not the per-blob Lock — is the amplifier, and the fix is to
// stop rescanning all seeds per check-in rather than to shard.
func BenchmarkPeerTrackerIngest(b *testing.B) {
	// (held blobs per envoy per check-in, total distinct blobs in the fleet)
	for _, seedBlobs := range []int{100, 1000} {
		for _, k := range []int{1, 5, 20} {
			if k > seedBlobs {
				continue
			}
			name := fmt.Sprintf("blobs=%d/held=%d", seedBlobs, k)
			b.Run(name, func(b *testing.B) {
				benchIngest(b, seedBlobs, k)
			})
		}
	}
}

// benchIngest pre-seeds a tracker to a realistic resting state, then has every
// parallel goroutine replay one envoy's steady-state swarm ingest per
// iteration. Goroutines reuse a fixed envoyID for their whole run (an envoy
// re-reports the same blobs each cycle), so the maps stay bounded and the
// benchmark measures lock cost, not unbounded map growth.
func benchIngest(b *testing.B, seedBlobs, held int) {
	const (
		preEnvoysPerBlob = 20 // resting fleet already holding each blob
		revokedSeeds     = 8  // a handful revoked at any moment, but RevokedSeeds scans ALL seeds
	)

	pt := NewPeerTracker(2 * time.Minute)

	// Distinct, valid 64-char hex hashes spread across the keyspace so a
	// hypothetical sha256-sharded tracker would distribute them — keeping the
	// benchmark fair to both the current and the proposed implementation.
	blobs := make([]string, seedBlobs)
	for i := range blobs {
		blobs[i] = fmt.Sprintf("%064x", i)
	}

	// Resting state: existing peers per blob, and a seed registered per blob so
	// the RevokedSeeds() scan walks a realistically sized seeds map.
	for _, h := range blobs {
		for e := 0; e < preEnvoysPerBlob; e++ {
			pt.ReportComplete(fmt.Sprintf("rest-envoy-%d", e), h, "10.0.0.1:1531")
		}
		pt.RegisterSeed(h, SeedState{Label: h[:8], SeederID: "rest-envoy-0", DistributedAt: time.Now()})
	}
	for i := 0; i < revokedSeeds && i < seedBlobs; i++ {
		pt.RevokeSeed(blobs[i])
	}

	var goroutineID int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		gid := atomic.AddInt64(&goroutineID, 1)
		envoyID := fmt.Sprintf("bench-envoy-%d", gid)

		// This envoy's held window, spread across the keyspace.
		myBlobs := make([]string, held)
		present := make(map[string]bool, held)
		for j := 0; j < held; j++ {
			h := blobs[(int(gid)*held+j)%len(blobs)]
			myBlobs[j] = h
			present[h] = true
		}
		sources := map[string]int{"10.0.0.2:1531": 4, "10.0.0.3:1531": 4}

		for pb.Next() {
			// Mirror ingestSwarmTraits: per held blob, ReportComplete then
			// SetChunkSources — two Lock() acquisitions each.
			for _, h := range myBlobs {
				pt.ReportComplete(envoyID, h, "10.0.0.9:1531")
				pt.SetChunkSources(envoyID, h, sources, nil, 5)
			}
			// Eviction detection: one RevokedSeeds() RLock scan over all seeds,
			// then HasPeerRecord (+ MarkEvicted on a hit) per revoked hash the
			// envoy no longer reports.
			for _, rh := range pt.RevokedSeeds() {
				if present[rh] {
					continue
				}
				if pt.HasPeerRecord(envoyID, rh) {
					pt.MarkEvicted(envoyID, rh)
				}
			}
		}
	})
	b.StopTimer()

	// Headline metric: sustained whole-check-in ingests per second at this
	// concurrency. Compare against the ~25k check-ins/s scale target.
	if secs := b.Elapsed().Seconds(); secs > 0 {
		b.ReportMetric(float64(b.N)/secs, "checkins/s")
	}
}
