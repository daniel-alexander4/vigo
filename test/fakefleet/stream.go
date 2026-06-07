// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

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

// runStream is the --stream variant of the run daemon. Instead of unary
// CheckIn/ReportResult calls, each envoy opens and HOLDS a persistent bidi
// AgentStream — the same RPC a real agent promotes — so the server registers
// it in the held-stream set and vigo_streams_active reflects the fleet. This
// is the only path that exercises the per-stream connection-RSS ceiling
// (checkin.max_connections, ~220 KB of server RSS per held stream); the unary
// path drives the freshness/check-in load but never holds a stream, so it
// leaves streams_active at zero. Use --stream to load-test the connection
// ceiling; use the default unary path to load-test the check-in/report rate.
func runStream(cfg config) {
	st, err := loadState(cfg.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			die("no state manifest at %s — run `create` first", cfg.statePath)
		}
		die("load state: %v", err)
	}
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	deltaMs := uint32(cfg.cadence.Milliseconds())
	var checkins, checkinFails, reports, reportFails, reportSkips, gone atomic.Int64
	c := &runCounters{&checkins, &checkinFails, &reports, &reportFails, &reportSkips, &gone}

	fmt.Printf("run --stream: holding %d bidi AgentStreams — checkin every %s, report every %s, %s startup ramp, one held stream per envoy; Ctrl-C to stop\n",
		n, cfg.cadence, cfg.reportCadence, ramp)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		offset := time.Duration(int64(ramp) * int64(i) / int64(n))
		go func(h *hostState, off time.Duration) {
			defer wg.Done()
			streamEnvoy(ctx, cfg, h, deltaMs, off, c)
		}(&st.Hosts[i], offset)
	}

	go reportRunStats(ctx, n, c)

	wg.Wait()
	fmt.Printf("\nrun --stream: stopped — %d checkins (%d failed), %d reports (%d failed), %d gone\n",
		checkins.Load(), checkinFails.Load(), reports.Load(), reportFails.Load(), gone.Load())
}

// streamEnvoy opens one envoy's bidi AgentStream and drives it until ctx is
// cancelled or the stream errors. The handshake is an initial CheckIn — the
// server registers the held stream on the first message (streams_active++) and
// replies with the initial CheckInResponse. A recv goroutine drains
// server→agent messages (so the stream never stalls on backpressure) and
// captures the echoed policy_version + fingerprint, while the send loop fires a
// CheckIn every --cadence and a ReportResult every --report-cadence over the
// same stream. gRPC bidi streams allow one concurrent sender and one concurrent
// receiver: the send loop is the only sender, the recv goroutine the only
// receiver.
func streamEnvoy(ctx context.Context, cfg config, h *hostState, deltaMs uint32, off time.Duration, c *runCounters) {
	conn, client, err := dialAgent(cfg, h)
	if err != nil {
		c.checkinFails.Add(1)
		return
	}
	defer conn.Close()

	// Spread the first connect across the ramp window so the initial
	// full-bundle builds (one per envoy) don't storm the server at t=0.
	select {
	case <-ctx.Done():
		return
	case <-time.After(off):
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.AgentStream(sctx)
	if err != nil {
		c.checkinFails.Add(1)
		return
	}

	// Handshake: the first message must be a CheckIn. The server validates it,
	// registers the held stream, and replies with the initial CheckInResponse.
	if err := stream.Send(&pb.AgentStreamMessage{
		Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: buildCheckInReq(h, deltaMs, 0, "")},
	}); err != nil {
		if status.Code(err) == codes.NotFound {
			c.gone.Add(1)
		} else {
			c.checkinFails.Add(1)
		}
		return
	}
	c.checkins.Add(1)

	// Steady-state cache, written by the recv goroutine and read by the send
	// loop, so subsequent check-ins echo the server's version+fingerprint and
	// hit the cheap no_change path (the same shape a real agent uses).
	var mu sync.Mutex
	var pv uint64
	var fp string

	go func() {
		for {
			msg, rerr := stream.Recv()
			if rerr != nil {
				cancel() // stream dead → unblock the send loop
				return
			}
			if r := msg.GetCheckInResponse(); r != nil {
				mu.Lock()
				if v := r.GetPolicyVersion(); v != 0 {
					pv = v
				}
				if nfp := r.GetResponseFingerprint(); nfp != "" {
					fp = nfp
				}
				mu.Unlock()
			}
		}
	}()

	ticker := time.NewTicker(cfg.cadence)
	defer ticker.Stop()
	var lastReport time.Time
	for {
		select {
		case <-sctx.Done():
			return
		case <-ticker.C:
		}

		mu.Lock()
		cpv, cfp := pv, fp
		mu.Unlock()
		if err := stream.Send(&pb.AgentStreamMessage{
			Msg: &pb.AgentStreamMessage_CheckIn{CheckIn: buildCheckInReq(h, deltaMs, cpv, cfp)},
		}); err != nil {
			c.checkinFails.Add(1)
			return
		}
		c.checkins.Add(1)

		// Reports ride the same stream on the report-cadence. configcrates is
		// nil under PolicyMode (the policy ships in policy_bundle, not field-1);
		// buildResults falls back to a synthetic result so the run is recordable.
		if now := time.Now(); now.Sub(lastReport) >= cfg.reportCadence {
			lastReport = now
			if err := stream.Send(&pb.AgentStreamMessage{
				Msg: &pb.AgentStreamMessage_ReportResult{ReportResult: buildReportReq(h, nil)},
			}); err != nil {
				c.reportFails.Add(1)
				return
			}
			c.reports.Add(1)
		}
	}
}
