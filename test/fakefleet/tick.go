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
		die("every envoy is gone server-side — run `create` to re-establish the fleet")
	}
}

// dialAgent opens a persistent mTLS gRPC connection for one envoy and
// returns it with a VigoAgent client. grpc.NewClient is lazy — the TLS
// handshake happens on the first RPC — so a caller that holds the
// connection across many ticks (the `run` daemon) pays the handshake once
// and reuses it, exactly as a real agent does, and grpc transparently
// reconnects with backoff if the server restarts.
func dialAgent(cfg config, h *hostState) (*grpc.ClientConn, pb.VigoAgentClient, error) {
	tlsCfg, err := tlsConfigFor(h.CertPEM, h.KeyPEM, h.CACertPEM)
	if err != nil {
		return nil, nil, err
	}
	conn, err := grpc.NewClient(cfg.grpcAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, err
	}
	return conn, pb.NewVigoAgentClient(conn), nil
}

// checkInOnce fires one envoy's CheckIn over an existing client and
// returns the response. A NotFound (deleted row) is returned verbatim so
// the caller can count it; any other error is a missed beat the next tick
// retries. deltaMs is reported as the check-in cycle so the server's
// freshness predicate sizes the stale window correctly.
//
// policyVersion is the server's last-reported config version echoed back:
// when it equals the server's current version the server returns
// no_change=true with a simple int compare and SKIPS bundle construction
// — the cheap steady-state path real agents use (the fingerprint is a
// secondary Merkle short-circuit). Passing 0/"" forces a full bundle (the
// first tick, and the one-shot `tick` command); doing that every second at
// fleet scale is what melts the server, so the daemon caches and echoes
// both the version and the fingerprint instead.
// buildCheckInReq assembles one signed CheckInRequest. Shared by the unary
// checkInOnce path and the --stream daemon (which wraps it in an
// AgentStreamMessage), so the signing / trait / version fields stay
// single-sourced. A fresh timestamp + signature is minted per call so each
// check-in lands inside the server's signature-validity window.
func buildCheckInReq(h *hostState, deltaMs uint32, policyVersion uint64, fingerprint string) *pb.CheckInRequest {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	return &pb.CheckInRequest{
		EnvoyId:            h.EnvoyID,
		PolicyVersion:      policyVersion,
		PolicyMode:         true,
		StateFingerprint:   fingerprint,
		Signature:          signWith(h.signingKey(), []byte(ts)),
		Timestamp:          timestamppb.New(now),
		TraitsJson:         h.TraitsJSON,
		AgentVersion:       agentVersion,
		IpAddress:          h.IP,
		LastCheckinDeltaMs: deltaMs,
	}
}

func checkInOnce(ctx context.Context, client pb.VigoAgentClient, h *hostState, deltaMs uint32, policyVersion uint64, fingerprint string) (*pb.CheckInResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return client.CheckIn(cctx, buildCheckInReq(h, deltaMs, policyVersion, fingerprint))
}

// reportOnce fires one envoy's ReportResult — the convergence run — for
// the given bundle over an existing client. Split from the check-in so
// the daemon can run reports on a slower cadence than check-ins (a run
// per envoy per second at fleet scale would bury the run store).
// buildReportReq assembles one signed ReportResultRequest (a convergence run).
// Shared by the unary reportOnce path and the --stream daemon.
func buildReportReq(h *hostState, configcrates []*pb.Configcrate) *pb.ReportResultRequest {
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	runID := h.EnvoyID + "-" + ts
	return &pb.ReportResultRequest{
		RunId:     runID,
		EnvoyId:   h.EnvoyID,
		Results:   buildResults(h, configcrates),
		Signature: signWith(h.signingKey(), append([]byte(runID), ts...)),
		Timestamp: timestamppb.New(now),
	}
}

func reportOnce(ctx context.Context, client pb.VigoAgentClient, h *hostState, configcrates []*pb.Configcrate) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := client.ReportResult(cctx, buildReportReq(h, configcrates))
	return err
}

// checkInAndReport runs one envoy's CheckIn + ReportResult over a fresh
// short-lived connection — the one-shot `tick` path, where each envoy is
// visited once and the process then exits. The daemon (`run`) instead
// holds the connection open and decouples the two cadences.
func checkInAndReport(cfg config, h *hostState, deltaMs uint32) error {
	conn, client, err := dialAgent(cfg, h)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := checkInOnce(context.Background(), client, h, deltaMs, 0, "")
	if err != nil {
		return err
	}
	return reportOnce(context.Background(), client, h, resp.GetConfigcrates())
}

// buildResults turns the resolved bundle into per-resource outcomes,
// keyed on the host's planned Outcome:
//   - "ok"      → everything settled, no change → Converged.
//   - "changed" → one resource re-applied (action updated, changed) → the
//     host converges but reports drift (Changed, then Diverged after
//     enough consecutive ticks).
//   - "failed"  → one resource errored on an attempted change → Failed.
//
// If the server returned no configcrates, a single fallback result keyed
// on the planned configcrate keeps the run recordable.
func buildResults(h *hostState, configcrates []*pb.Configcrate) []*pb.ResourceRunResult {
	var out []*pb.ResourceRunResult
	for _, cc := range configcrates {
		for _, r := range cc.GetResources() {
			out = append(out, &pb.ResourceRunResult{
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
		out = append(out, &pb.ResourceRunResult{
			ConfigcrateName: h.Configcrate,
			ResourceType:    "exec",
			ResourceName:    "synthetic-noop",
			ActionTaken:     "settled",
			Changed:         false,
			DurationMs:      3,
		})
	}
	switch h.Outcome {
	case "changed":
		out[0].ActionTaken = "updated"
		out[0].Changed = true
		out[0].DurationMs = 42
	case "failed":
		out[0].ActionTaken = "updated"
		out[0].Changed = true
		out[0].ErrorMessage = "synthetic convergence failure (demo fleet)"
		out[0].DurationMs = 55
	}
	return out
}
