// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

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
// # WHY THIS EXISTS
//
// PeerTracker guards four maps with one sync.RWMutex. On a swarm-heavy fleet
// — many envoys, each holding several blobs, all checking in fast — each
// check-in updates the tracker for every held blob plus runs a RevokedSeeds()
// eviction scan, all under that one lock. That is the same single-global-mutex
// shape that made TraitsBatcher.Mark the throughput wall before 3b83080f.
//
// This benchmark drove two fixes to that path, both verified here:
//   - The RevokedSeeds() full-O(seeds) scan that ran every check-in is now an
//     O(revoked) membership-index lookup (the dominant cost — it made 1000
//     seeds 6.3× slower than 100 at held=1 for identical per-blob work).
//   - The per-blob updates (formerly 1-2 separate Lock acquisitions each, via
//     ReportComplete + SetChunkSources) are now applied under a single
//     ReportBlobs lock per check-in, flattening the residual per-acquisition
//     contention that showed as negative core-scaling.
//
// It answers the "Shard PeerTracker.mu" question in isolation — no network, no
// gRPC, no SQLite — so the lock's own ceiling is the only thing measured. The
// fakefleet route would bury this signal under the network/DB costs we already
// know dominate end-to-end. Verdict: with these two fixes the lock clears the
// scale target with comfortable headroom; a sha256-sharded restructure is not
// warranted.
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
// # HOW TO READ IT
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
		updates := make([]BlobUpdate, len(myBlobs))
		for i, h := range myBlobs {
			updates[i] = BlobUpdate{SHA256: h, Complete: true, Sources: sources, ActiveDownloadMs: 5}
		}

		for pb.Next() {
			// Mirror ingestSwarmTraits: apply all of an envoy's blob updates
			// under a single ReportBlobs lock acquisition.
			pt.ReportBlobs(envoyID, "10.0.0.9:1531", updates)
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
			// Mirror ingestSwarmSeeds' seeder path: a supersede FindSeedByLabel
			// per reported seed + one SeedsForEnvoy unseed-detection call. Both
			// were O(all-seeds) under RLock (FindSeedByLabel N× per check-in);
			// the seedsByLabel/seedsByEnvoy indexes make them O(held)/O(1).
			for _, h := range myBlobs {
				_ = pt.FindSeedByLabel(h[:8])
			}
			_ = pt.SeedsForEnvoy(envoyID)
		}
	})
	b.StopTimer()

	// Headline metric: sustained whole-check-in ingests per second at this
	// concurrency. Compare against the ~25k check-ins/s scale target.
	if secs := b.Elapsed().Seconds(); secs > 0 {
		b.ReportMetric(float64(b.N)/secs, "checkins/s")
	}
}
