// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// runRun is the long-running daemon — modeled on the burstwave load
// harness's proven per-envoy engine, but shaped for a persistent demo
// fleet rather than a timed burst. It loads the manifest once, holds one
// persistent mTLS connection per envoy, and checks each in on the
// --cadence interval (default 1s) until SIGINT/SIGTERM.
//
// The two RPCs run on independent tracks, which is the whole point:
//
//   - CheckIn (the freshness signal) fires every --cadence. After the
//     first contact it echoes the server's policy_version, so the server
//     short-circuits on a cheap int compare (no_change) with no bundle
//     rebuild — the same steady-state path a real agent uses.
//   - ReportResult (a convergence run = an SQLite write) fires only every
//     --report-cadence, and is FIRE-AND-FORGET in a bounded worker pool so
//     a slow or shed write can never stall the 1s check-in loop. A run per
//     envoy per second at fleet scale buries the single-writer pool; the
//     fleet only needs occasional reports to keep the convergence pills
//     populated, and freshness doesn't need them at all.
//
// Each envoy's first tick is staggered across a startup --ramp window so
// the initial full-bundle builds (one per envoy, before the cheap path
// engages) spread at a rate the server absorbs instead of storming it.
//
// One persistent connection means one file descriptor per envoy; the
// systemd unit raises LimitNOFILE accordingly, and a manual run needs
// `ulimit -n` above the fleet size.
func runRun(cfg config) {
	if cfg.stream {
		runStream(cfg)
		return
	}
	st, err := loadState(cfg.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			die("no state manifest at %s — run `create` first", cfg.statePath)
		}
		die("load state: %v", err)
	}
	loadPayloads(st, cfg.inventoryKB)
	n := len(st.Hosts)
	if n == 0 {
		die("manifest has no hosts — run `create` first")
	}
	if cfg.limit > 0 && cfg.limit < n {
		n = cfg.limit
	}

	ramp := cfg.ramp
	if ramp < cfg.cadence {
		ramp = cfg.cadence
	}

	// Multi-VM fan-out: each harness VM drives a disjoint [start,end) shard of
	// the same manifest. Empty --slice → the whole fleet.
	start, end, err := sliceBounds(n, cfg.slice)
	if err != nil {
		die("%v", err)
	}
	shardN := end - start

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	deltaMs := uint32(cfg.cadence.Milliseconds())
	var checkins, checkinFails, reports, reportFails, reportSkips, gone atomic.Int64

	// Bound concurrent in-flight reports across the whole fleet so a writer
	// backlog can't spawn unbounded goroutines; when it's full the envoy
	// skips that report round (freshness is unaffected) and tries again
	// next report-cadence.
	reportSem := make(chan struct{}, cfg.reportConcurrency)

	fmt.Printf("run: driving %d of %d envoys%s — checkin every %s, report every %s (≤%d in flight), %s startup ramp, persistent conn per envoy; Ctrl-C to stop\n",
		shardN, n, sliceLabel(cfg.slice), cfg.cadence, cfg.reportCadence, cfg.reportConcurrency, ramp)

	var wg sync.WaitGroup
	for i := start; i < end; i++ {
		wg.Add(1)
		offset := time.Duration(int64(ramp) * int64(i-start) / int64(shardN))
		go func(h *hostState, off time.Duration) {
			defer wg.Done()
			conn, client, err := dialAgent(cfg, h)
			if err != nil {
				checkinFails.Add(1)
				return
			}
			defer conn.Close()

			// Spread the first tick across the ramp window.
			select {
			case <-ctx.Done():
				return
			case <-time.After(off):
			}

			// Per-envoy steady-state cache: echo the server's last policy
			// version and Merkle fingerprint so unchanged check-ins
			// short-circuit on the server's cheap path. fakefleet checks in
			// in PolicyMode, so the resolved policy ships in the policy_bundle
			// (proto field 7/26), never the legacy field-1 `configcrates` —
			// `GetConfigcrates()` is always empty here, and reportOnce relies
			// on buildResults' documented fallback to emit a recordable
			// per-envoy result. We still capture field-1 in case a
			// non-PolicyMode path ever populates it.
			var pv uint64
			var fp string
			var bundle []*pb.Configcrate
			// ADR-033 inventory gate: ship the full inventory on first contact,
			// then only when the server asks for a resend (cache miss / restart /
			// desync). Mirrors the real agent's InventoryGate — only a successful
			// check-in advances it, so a failed send re-ships next tick.
			sendInv := true
			ticker := time.NewTicker(cfg.cadence)
			defer ticker.Stop()
			var lastReport time.Time // zero → report on the first tick
			for {
				resp, err := checkInOnce(ctx, client, h, deltaMs, pv, fp, sendInv)
				switch {
				case err == nil:
					checkins.Add(1)
					sendInv = resp.GetResendInventory()
					if v := resp.GetPolicyVersion(); v != 0 {
						pv = v
					}
					if nfp := resp.GetResponseFingerprint(); nfp != "" {
						fp = nfp
					}
					if cc := resp.GetConfigcrates(); len(cc) > 0 {
						bundle = cc
					}
					// Fire reports on the report-cadence regardless of field-1
					// configcrates (always empty in PolicyMode); buildResults
					// falls back to a synthetic result so the run is recordable.
					if now := time.Now(); now.Sub(lastReport) >= cfg.reportCadence {
						lastReport = now
						b := bundle // capture for the fire-and-forget report
						select {
						case reportSem <- struct{}{}:
							go func() {
								defer func() { <-reportSem }()
								if rerr := reportOnce(ctx, client, h, b); rerr == nil {
									reports.Add(1)
								} else if ctx.Err() == nil {
									reportFails.Add(1)
								}
							}()
						default:
							reportSkips.Add(1) // writer backlog — skip; freshness unaffected
						}
					}
				case status.Code(err) == codes.NotFound:
					gone.Add(1)
				case ctx.Err() != nil:
					return // shutting down — the aborted call isn't a real miss
				default:
					checkinFails.Add(1)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(&st.Hosts[i], offset)
	}

	go reportRunStats(ctx, shardN, &runCounters{&checkins, &checkinFails, &reports, &reportFails, &reportSkips, &gone})

	wg.Wait()
	fmt.Printf("\nrun: stopped — %d checkins (%d failed), %d reports (%d failed, %d skipped), %d gone\n",
		checkins.Load(), checkinFails.Load(), reports.Load(), reportFails.Load(), reportSkips.Load(), gone.Load())
}

// runCounters bundles the daemon's atomic counters for the stats logger.
type runCounters struct {
	checkins, checkinFails, reports, reportFails, reportSkips, gone *atomic.Int64
}

// reportRunStats logs the per-interval check-in rate plus cumulative
// report/failure counts every 10s, so a watcher can confirm the fleet is
// live and the cadence is holding.
func reportRunStats(ctx context.Context, n int, c *runCounters) {
	const window = 10 * time.Second
	t := time.NewTicker(window)
	defer t.Stop()
	var last int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := c.checkins.Load()
			rate := float64(now-last) / window.Seconds()
			last = now
			fmt.Printf("run: %d envoys | %.0f checkins/s | checkin_fail=%d | reports=%d (fail=%d skip=%d) | gone=%d\n",
				n, rate, c.checkinFails.Load(), c.reports.Load(), c.reportFails.Load(), c.reportSkips.Load(), c.gone.Load())
		}
	}
}
