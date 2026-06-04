// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// runTick is one freshness pass: load the manifest and, for every
// synthetic envoy, fire a single CheckIn (traits inline) + ReportResult
// against the bundle the server hands back, then exit. It is stateless
// and idempotent — the systemd timer fires it on a cadence, and that is
// the entire durability story: a server restart, host reboot, or
// transient outage is just "this tick failed, the next one retries".
func runTick(cfg config) {
	st, err := loadState(cfg.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Nothing to resume — backout removed the manifest, or the
			// fleet was never created. Exit cleanly; the timer simply
			// fires again next interval as a harmless no-op until it's
			// disabled.
			fmt.Fprintln(os.Stderr, "no state manifest — nothing to tick")
			return
		}
		die("load state: %v", err)
	}

	var fail, gone atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.concurrency)
	deltaMs := uint32(cfg.cadence.Milliseconds())

	for i := range st.Hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(h *hostState) {
			defer wg.Done()
			defer func() { <-sem }()
			switch err := checkInAndReport(cfg, h, deltaMs); {
			case err == nil:
			case status.Code(err) == codes.NotFound:
				gone.Add(1)
			default:
				fail.Add(1)
			}
		}(&st.Hosts[i])
	}
	wg.Wait()

	n := len(st.Hosts)
	fmt.Printf("tick: %d envoys, %d failed, %d gone\n", n, fail.Load(), gone.Load())

	// Every envoy unknown to the server means the fleet was deleted out
	// from under us (e.g. a DB wipe). Exit non-zero so the operator sees
	// it; `create` re-establishes the fleet.
	if n > 0 && int(gone.Load()) == n {
		die("every synthetic envoy is gone server-side — run `create` to re-establish the fleet")
	}
}

// checkInAndReport runs one envoy's CheckIn + ReportResult over a
// short-lived mTLS connection. A NotFound from CheckIn (deleted row) is
// returned verbatim so the caller can count it; all other errors mean a
// missed beat that the next tick retries.
func checkInAndReport(cfg config, h *hostState, deltaMs uint32) error {
	tlsCfg, err := tlsConfigFor(h.CertPEM, h.KeyPEM, h.CACertPEM)
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(cfg.grpcAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := pb.NewVigoAgentClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Empty state_fingerprint forces a full bundle every tick, so the
	// returned configcrates are always present to report against — which
	// keeps the tick stateless (no fingerprint to persist).
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	checkInSig := signWith(h.signingKey(), []byte(ts))
	resp, err := client.CheckIn(ctx, &pb.CheckInRequest{
		EnvoyId:            h.EnvoyID,
		StateFingerprint:   "",
		Signature:          checkInSig,
		Timestamp:          timestamppb.New(now),
		TraitsJson:         h.TraitsJSON,
		AgentVersion:       "vigo-fakefleet",
		IpAddress:          h.IP,
		LastCheckinDeltaMs: deltaMs,
	})
	if err != nil {
		return err
	}

	results := buildResults(h, resp.GetConfigcrates())

	now = time.Now()
	ts = strconv.FormatInt(now.Unix(), 10)
	runID := h.EnvoyID + "-" + ts
	reportSig := signWith(h.signingKey(), append([]byte(runID), ts...))
	_, err = client.ReportResult(ctx, &pb.ReportResultRequest{
		RunId:     runID,
		EnvoyId:   h.EnvoyID,
		Results:   results,
		Signature: reportSig,
		Timestamp: timestamppb.New(now),
	})
	return err
}

// buildResults turns the resolved bundle into per-resource outcomes:
// everything settled-with-no-change for a healthy host, or one resource
// carrying an error for a broken host (a non-blocked action + an error
// message is what the server classifies as a failed result → Degraded /
// Failed). If the server returned no configcrates, a single fallback
// result keyed on the planned configcrate keeps the run recordable.
func buildResults(h *hostState, configcrates []*pb.Configcrate) []*pb.ConfigcrateResult {
	var out []*pb.ConfigcrateResult
	for _, cc := range configcrates {
		for _, r := range cc.GetResources() {
			out = append(out, &pb.ConfigcrateResult{
				ConfigcrateName: cc.GetName(),
				ResourceType:    r.GetType(),
				ResourceName:    r.GetName(),
				ActionTaken:     "settled",
				Changed:         false,
				DurationMs:      3,
			})
		}
	}
	if len(out) == 0 {
		out = append(out, &pb.ConfigcrateResult{
			ConfigcrateName: h.Configcrate,
			ResourceType:    "exec",
			ResourceName:    "synthetic-noop",
			ActionTaken:     "settled",
			Changed:         false,
			DurationMs:      3,
		})
	}
	if h.Broken {
		out[0].ErrorMessage = "synthetic convergence failure (demo fleet)"
	}
	return out
}
