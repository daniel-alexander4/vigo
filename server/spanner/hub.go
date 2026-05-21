// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package spanner provides hub coordination for the spanner federation:
// one Hub aggregates admin queries across a configured set of bolts
// (peer vigosrvs that own their own envoy partition). The current
// implementation is still hub-bolt (not yet the peer-equal redesign);
// Phase 1+ will replace the Hub-centric model with peer-equal gossip.
// The Hub connects to each bolt over mTLS gRPC and fans out admin
// queries (ListEnvoys, GetConvergenceSummary, ListRuns, ForcePush)
// across all of them.
package spanner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Hub manages gRPC connections to bolts and fans out admin queries.
type Hub struct {
	bolts       []boltConn
	boltReports map[string]*pb.BoltStatusReport // latest bolt self-reports, protected by mu
	// boltPins holds first-sighting (spanner_id, bolt_id) →
	// bolt_pubkey records for Phase 1 identity pinning. In-memory
	// only: lost on hub restart by design (Phase 2's CRDT layer
	// adds the proper persisted signed-members set). Protected by mu.
	boltPins    map[string]*BoltPin
	auditWriter PinAuditRecorder // nil-safe; emits mismatch events
	metrics     *metrics.Metrics // per-instance prometheus collectors
	mu          sync.RWMutex
}

// BoltPin records the verified identity of a bolt on first
// sighting. Subsequent reports from the same (spanner_id, bolt_id)
// must carry the same pubkey or are rejected with an audit event.
type BoltPin struct {
	SpannerID string
	BoltID    string
	Pubkey    []byte // 32-byte Ed25519 raw pubkey
	PinnedAt  time.Time
	LastSeen  time.Time
}

// PinAuditRecorder is the nil-safe interface the hub uses to emit
// `spanner_bolt_pubkey_mismatch` audit events. Implemented by
// *audit.Writer; tests pass a no-op or a recording stub.
type PinAuditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// SetAuditWriter attaches the audit writer for pubkey-mismatch
// events. nil-safe — calling without a writer just suppresses
// audit records; pin enforcement still runs.
func (h *Hub) SetAuditWriter(w PinAuditRecorder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.auditWriter = w
}

// pinKey is the map key for boltPins. (spanner_id, bolt_id)
// pairs scoped: two spanners can each have a bolt with the same
// bolt_id and they don't collide.
func pinKey(spannerID, boltID string) string {
	return spannerID + "\x00" + boltID
}

type boltConn struct {
	id       string
	addr     string
	patterns []string // hostname glob patterns routed to this bolt
	conn     *grpc.ClientConn
	admin    pb.VigoAdminClient
}

// NewHub creates a Hub that connects to the configured bootstrap-discovery
// peers via mTLS. Each address gets a long-lived gRPC client connection used
// for admin-query fan-out + the JoinSpanner rendezvous. After Slice 2A.2
// wires the CRDT merge, the peer list is dynamic — gossip extends this
// initial seed. m owns the per-peer latency/up gauges. The legacy
// per-bolt config (id + patterns) lived in server.yaml pre-ADR-026; in
// the peer-equal model that data lives in the gossiped admissions CRDT
// (spanner_bolts table), so admin-query routing-by-pattern is degraded
// in 2A.1b — RouteHostname returns no-route until 2A.2 lands the merge.
func NewHub(ctx context.Context, bootstrapPeers []string, certFile, keyFile, caFile string, m *metrics.Metrics) (*Hub, error) {
	tlsCreds, err := buildClientTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building bolt TLS credentials: %w", err)
	}

	h := &Hub{
		bolts:       make([]boltConn, 0, len(bootstrapPeers)),
		boltReports: make(map[string]*pb.BoltStatusReport),
		boltPins:    make(map[string]*BoltPin),
		metrics:     m,
	}

	for _, addr := range bootstrapPeers {
		// NewClient is lazy: it never blocks and only errors on a
		// malformed target. The connection opens on the first RPC and
		// gRPC reconnects in the background, so an unreachable bootstrap
		// peer no longer fails Hub construction.
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(tlsCreds),
		)
		if err != nil {
			// Close any already-opened connections.
			h.Close()
			return nil, fmt.Errorf("connecting to bootstrap peer at %s: %w", addr, err)
		}

		// id stays empty until the peer announces its own pubkey via the
		// signed BoltStatusReport (Phase 1) and we pin it; patterns
		// stay empty until the CRDT merge populates them.
		h.bolts = append(h.bolts, boltConn{
			id:    addr, // temporary handle until pubkey lands via announcement
			addr:  addr,
			conn:  conn,
			admin: pb.NewVigoAdminClient(conn),
		})
		slog.Info("connected to bootstrap peer", "addr", addr)
	}

	return h, nil
}

// Close shuts down all bolt connections.
func (h *Hub) Close() {
	for _, s := range h.bolts {
		if s.conn != nil {
			s.conn.Close()
		}
	}
}

// BoltCount returns the number of configured bolts.
func (h *Hub) BoltCount() int {
	return len(h.bolts)
}

// AggregateListEnvoys fans out ListEnvoys to all bolts and merges results.
func (h *Hub) AggregateListEnvoys(ctx context.Context) ([]*pb.Envoy, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc boltConn) (any, error) {
		resp, err := sc.admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Envoys, nil
	})

	var allNodes []*pb.Envoy
	for _, r := range results {
		if r.err != nil {
			slog.Warn("bolt ListEnvoys failed", "bolt_id", r.boltID, "error", r.err)
			continue
		}
		allNodes = append(allNodes, r.value.([]*pb.Envoy)...)
	}
	return allNodes, nil
}

// AggregateConvergenceSummary fans out GetConvergenceSummary to all bolts
// and sums the results.
func (h *Hub) AggregateConvergenceSummary(ctx context.Context) (*pb.GetConvergenceSummaryResponse, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc boltConn) (any, error) {
		return sc.admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
	})

	agg := &pb.GetConvergenceSummaryResponse{}
	boltCount := 0
	for _, r := range results {
		if r.err != nil {
			slog.Warn("bolt GetConvergenceSummary failed", "bolt_id", r.boltID, "error", r.err)
			continue
		}
		resp := r.value.(*pb.GetConvergenceSummaryResponse)
		agg.Total += resp.Total
		agg.Converged += resp.Converged
		agg.Relapsed += resp.Relapsed
		agg.Diverged += resp.Diverged
		agg.Failed += resp.Failed
		agg.Offline += resp.Offline
		agg.NoData += resp.NoData
		boltCount++
	}

	// Lost-contact envoys excluded from compliance percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := agg.Total - agg.Offline
	if activeTotal > 0 {
		agg.ConvergencePct = float32(agg.Converged) / float32(activeTotal) * 100
	}

	return agg, nil
}

// AggregateListRuns fans out ListRuns to all bolts and merges results.
// If envoyID is non-empty, only queries bolts (all of them — fan-out is cheap).
func (h *Hub) AggregateListRuns(ctx context.Context, envoyID string, limit int32) ([]*pb.Run, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc boltConn) (any, error) {
		resp, err := sc.admin.ListRuns(ctx, &pb.ListRunsRequest{
			EnvoyId: envoyID,
			Limit:   limit,
		})
		if err != nil {
			return nil, err
		}
		return resp.Runs, nil
	})

	var allRuns []*pb.Run
	for _, r := range results {
		if r.err != nil {
			slog.Warn("bolt ListRuns failed", "bolt_id", r.boltID, "error", r.err)
			continue
		}
		allRuns = append(allRuns, r.value.([]*pb.Run)...)
	}
	return allRuns, nil
}

// ForwardForcePush sends a ForcePush to all bolts (or targeted if envoyID
// is non-empty). Returns total envoys notified across all bolts.
func (h *Hub) ForwardForcePush(ctx context.Context, envoyID string) (int32, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc boltConn) (any, error) {
		resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{EnvoyId: envoyID})
		if err != nil {
			return nil, err
		}
		return resp.EnvoysNotified, nil
	})

	var total int32
	for _, r := range results {
		if r.err != nil {
			slog.Warn("bolt ForcePush failed", "bolt_id", r.boltID, "error", r.err)
			continue
		}
		total += r.value.(int32)
	}
	return total, nil
}

// RouteHostname determines which bolt should handle a given hostname by
// matching against each bolt's configured patterns. Returns the bolt's
// gRPC address and true if a match is found, or empty string and false if
// no bolt pattern matches (the hub handles it locally).
func (h *Hub) RouteHostname(hostname string) (addr string, ok bool) {
	for _, sc := range h.bolts {
		for _, pattern := range sc.patterns {
			matched, err := filepath.Match(pattern, hostname)
			if err != nil {
				slog.Warn("invalid bolt pattern", "bolt_id", sc.id, "pattern", pattern, "error", err)
				continue
			}
			if matched {
				return sc.addr, true
			}
		}
	}
	return "", false
}

// EnrollOnBolt creates a envoy record on the target bolt via the EnrollEnvoy
// admin RPC. Returns the envoy UUID assigned by the bolt.
func (h *Hub) EnrollOnBolt(ctx context.Context, boltAddr, hostname, ipAddress, pubkeyPEM string, force bool) (string, error) {
	// Find the bolt connection by address.
	var target *boltConn
	for i := range h.bolts {
		if h.bolts[i].addr == boltAddr {
			target = &h.bolts[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("no bolt connection for address %s", boltAddr)
	}

	resp, err := target.admin.EnrollEnvoy(ctx, &pb.EnrollEnvoyRequest{
		Hostname:  hostname,
		IpAddress: ipAddress,
		PubkeyPem: pubkeyPEM,
		Force:     force,
	})
	if err != nil {
		return "", fmt.Errorf("enroll on bolt %s: %w", target.id, err)
	}

	slog.Info("enrolled envoy on bolt", "bolt_id", target.id, "hostname", hostname, "envoy_id", resp.EnvoyId)
	return resp.EnvoyId, nil
}

// BoltByID returns the bolt connection for the given ID, or nil if not found.
func (h *Hub) BoltByID(id string) *boltConn {
	for i := range h.bolts {
		if h.bolts[i].id == id {
			return &h.bolts[i]
		}
	}
	return nil
}

// BoltListEnvoys queries a single bolt for its envoy list.
func (h *Hub) BoltListEnvoys(ctx context.Context, boltID string) ([]*pb.Envoy, error) {
	sc := h.BoltByID(boltID)
	if sc == nil {
		return nil, fmt.Errorf("bolt %q not found", boltID)
	}
	resp, err := sc.admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEnvoys on bolt %s: %w", boltID, err)
	}
	return resp.Envoys, nil
}

// BoltListRuns queries a single bolt for its run list.
func (h *Hub) BoltListRuns(ctx context.Context, boltID string, limit int32) ([]*pb.Run, error) {
	sc := h.BoltByID(boltID)
	if sc == nil {
		return nil, fmt.Errorf("bolt %q not found", boltID)
	}
	resp, err := sc.admin.ListRuns(ctx, &pb.ListRunsRequest{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("ListRuns on bolt %s: %w", boltID, err)
	}
	return resp.Runs, nil
}

// BoltConvergenceSummary queries a single bolt for its compliance summary.
func (h *Hub) BoltConvergenceSummary(ctx context.Context, boltID string) (*pb.GetConvergenceSummaryResponse, error) {
	sc := h.BoltByID(boltID)
	if sc == nil {
		return nil, fmt.Errorf("bolt %q not found", boltID)
	}
	return sc.admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
}

// BoltForcePush sends a ForcePush to a single bolt.
func (h *Hub) BoltForcePush(ctx context.Context, boltID, envoyID string) (int32, error) {
	sc := h.BoltByID(boltID)
	if sc == nil {
		return 0, fmt.Errorf("bolt %q not found", boltID)
	}
	resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{EnvoyId: envoyID})
	if err != nil {
		return 0, fmt.Errorf("ForcePush on bolt %s: %w", boltID, err)
	}
	return resp.EnvoysNotified, nil
}

// BoltStatus describes the health and summary of a single bolt.
type BoltStatus struct {
	ID             string   `json:"id"`
	Addr           string   `json:"addr"`
	Patterns       []string `json:"patterns"`
	Healthy        bool     `json:"healthy"`
	EnvoyCount     int32    `json:"envoy_count"`
	Converged      int32    `json:"converged"`
	Relapsed       int32    `json:"relapsed"`
	Diverged       int32    `json:"diverged"`
	Failed         int32    `json:"failed"`
	Offline        int32    `json:"offline"`
	NoData         int32    `json:"no_data"`
	ConvergencePct float32  `json:"compliance_pct"`
	LatencyMs      int64    `json:"latency_ms"`
	ConfigVersion  uint64   `json:"config_version,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// probeResult carries both the compliance response and the latency from a bolt probe.
type probeResult struct {
	resp    *pb.GetConvergenceSummaryResponse
	latency time.Duration
}

// BoltStatuses probes each bolt's health by calling GetConvergenceSummary
// with a short timeout and returns the results with compliance breakdown and latency.
// If a bolt is unreachable but has a recent self-report, the report data is
// used but the bolt is still marked unhealthy.
func (h *Hub) BoltStatuses(ctx context.Context) []BoltStatus {
	results := h.fanOut(ctx, func(ctx context.Context, sc boltConn) (any, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		start := time.Now()
		resp, err := sc.admin.GetConvergenceSummary(probeCtx, &pb.GetConvergenceSummaryRequest{})
		latency := time.Since(start)
		if err != nil {
			return &probeResult{latency: latency}, err
		}
		return &probeResult{resp: resp, latency: latency}, nil
	})

	statuses := make([]BoltStatus, len(h.bolts))
	for i, sc := range h.bolts {
		statuses[i] = BoltStatus{
			ID:       sc.id,
			Addr:     sc.addr,
			Patterns: sc.patterns,
		}
		r := results[i]
		if r.err != nil {
			statuses[i].Error = r.err.Error()
			if pr, ok := r.value.(*probeResult); ok {
				statuses[i].LatencyMs = pr.latency.Milliseconds()
				if h.metrics != nil {
					h.metrics.SpannerBoltLatency.WithLabelValues(sc.id).Observe(pr.latency.Seconds())
				}
			}
			if h.metrics != nil {
				h.metrics.SpannerBoltUp.WithLabelValues(sc.id).Set(0)
			}

			// If probe failed but we have a self-report from the bolt,
			// populate compliance data so dashboards are not blank.
			if report := h.getBoltReport(sc.id); report != nil {
				statuses[i].EnvoyCount = report.EnvoyCount
				statuses[i].Converged = report.Converged
				statuses[i].Relapsed = report.Relapsed
				statuses[i].Diverged = report.Diverged
				statuses[i].Failed = report.Failed
				statuses[i].Offline = report.Offline
				statuses[i].NoData = report.NoData
				statuses[i].ConvergencePct = report.ConvergencePct
				statuses[i].ConfigVersion = report.ConfigVersion
			}
		} else {
			pr := r.value.(*probeResult)
			statuses[i].Healthy = true
			statuses[i].EnvoyCount = pr.resp.Total
			statuses[i].Converged = pr.resp.Converged
			statuses[i].Relapsed = pr.resp.Relapsed
			statuses[i].Diverged = pr.resp.Diverged
			statuses[i].Failed = pr.resp.Failed
			statuses[i].Offline = pr.resp.Offline
			statuses[i].NoData = pr.resp.NoData
			statuses[i].ConvergencePct = pr.resp.ConvergencePct
			statuses[i].LatencyMs = pr.latency.Milliseconds()
			if h.metrics != nil {
				h.metrics.SpannerBoltLatency.WithLabelValues(sc.id).Observe(pr.latency.Seconds())
				h.metrics.SpannerBoltUp.WithLabelValues(sc.id).Set(1)
			}
		}
	}
	return statuses
}

// RecordBoltReport stores a bolt's self-reported status. Called
// by the ReportBoltStatus gRPC handler on the hub.
//
// Phase 1 (ADR-spanner): when the report carries bolt_pubkey +
// signature + spanner_id, the hub (1) verifies the signature against
// the asserted pubkey, (2) on first sighting pins
// (spanner_id, bolt_id) → pubkey, (3) on subsequent reports
// requires the asserted pubkey to match the pinned one. Mismatch
// emits a `spanner_bolt_pubkey_mismatch` audit event and the
// report is REJECTED — neither stored nor folded into aggregates.
//
// Pre-Phase-1 reporters (no pubkey/signature fields) are accepted
// without pinning, logged at info. Mixed-version fleet rollout is
// safe; the pin starts on the first compliant report.
//
// Returns nil on success, error on rejection. The gRPC handler
// surfaces the error to the caller as a non-zero status code.
func (h *Hub) RecordBoltReport(ctx context.Context, report *pb.BoltStatusReport) error {
	// Legacy (pre-Phase-1) report: no pubkey/signature, no pin.
	if len(report.BoltPubkey) == 0 && len(report.Signature) == 0 {
		slog.Info("legacy bolt report (no signature) — accepted without pinning",
			"bolt_id", report.BoltId,
			"spanner_id", report.SpannerId,
		)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.boltReports[report.BoltId] = report
		return nil
	}

	// Phase 1 report: must verify and pin.
	if report.SpannerId == "" {
		return fmt.Errorf("signed report missing spanner_id (required for pinning)")
	}
	if err := VerifyReportSignature(report); err != nil {
		slog.Warn("bolt report signature verify failed; rejecting",
			"bolt_id", report.BoltId,
			"spanner_id", report.SpannerId,
			"error", err,
		)
		h.emitMismatchAudit(ctx, report, nil, "signature_verify_failed: "+err.Error())
		return fmt.Errorf("signature verify: %w", err)
	}

	key := pinKey(report.SpannerId, report.BoltId)
	now := time.Now().UTC()

	h.mu.Lock()
	defer h.mu.Unlock()
	pinned, ok := h.boltPins[key]
	if !ok {
		// First sighting — pin.
		pinCopy := make([]byte, len(report.BoltPubkey))
		copy(pinCopy, report.BoltPubkey)
		h.boltPins[key] = &BoltPin{
			SpannerID: report.SpannerId,
			BoltID:    report.BoltId,
			Pubkey:    pinCopy,
			PinnedAt:  now,
			LastSeen:  now,
		}
		slog.Info("bolt pubkey pinned on first sighting",
			"spanner_id", report.SpannerId,
			"bolt_id", report.BoltId,
			"pubkey_short", shortPubkey(report.BoltPubkey),
		)
		h.boltReports[report.BoltId] = report
		return nil
	}

	// Subsequent sighting — pubkey must match pin.
	if !bytesEqual(pinned.Pubkey, report.BoltPubkey) {
		slog.Warn("bolt pubkey mismatch — rejecting report",
			"spanner_id", report.SpannerId,
			"bolt_id", report.BoltId,
			"asserted_pubkey_short", shortPubkey(report.BoltPubkey),
			"pinned_pubkey_short", shortPubkey(pinned.Pubkey),
			"pinned_at", pinned.PinnedAt,
		)
		// Drop the lock before emitting audit (audit writer may
		// block on DB I/O; no point holding the report mutex).
		h.mu.Unlock()
		h.emitMismatchAudit(ctx, report, pinned, "pubkey_mismatch")
		h.mu.Lock() // restore for defer
		return fmt.Errorf("bolt pubkey for (spanner_id=%s, bolt_id=%s) does not match pinned identity", report.SpannerId, report.BoltId)
	}

	// Verified + matches pin — record + update last-seen.
	pinned.LastSeen = now
	h.boltReports[report.BoltId] = report
	return nil
}

// emitMismatchAudit records a `spanner_bolt_pubkey_mismatch`
// audit event with the asserted vs pinned pubkeys. nil-safe.
func (h *Hub) emitMismatchAudit(ctx context.Context, report *pb.BoltStatusReport, pinned *BoltPin, reason string) {
	if h.auditWriter == nil {
		return
	}
	pinnedHex := ""
	if pinned != nil {
		pinnedHex = fmt.Sprintf("%x", pinned.Pubkey)
	}
	payload := fmt.Sprintf(
		`{"spanner_id":%q,"bolt_id":%q,"asserted_pubkey":"%x","pinned_pubkey":%q,"reason":%q,"decision":"rejected"}`,
		report.SpannerId, report.BoltId, report.BoltPubkey, pinnedHex, reason,
	)
	summary := fmt.Sprintf("bolt pubkey mismatch on (spanner=%s, bolt=%s); report rejected", report.SpannerId, report.BoltId)
	_ = h.auditWriter.Record(ctx, "spanner_bolt_pubkey_mismatch", "spanner", "grpc", summary, payload)
}

// shortPubkey is the first-8-bytes hex form used in log fields.
// Operators cross-reference this against the local pubkey shown
// by `vigocli spanner status`.
func shortPubkey(pk []byte) string {
	if len(pk) < 8 {
		return fmt.Sprintf("%x", pk)
	}
	return fmt.Sprintf("%x", pk[:8])
}

// bytesEqual is a constant-time-not-required equality check used
// for pubkey pin comparison. The pubkeys aren't secret — the
// signature on the report already proves possession of the
// private key — so constant-time isn't needed.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LookupBoltPin returns the pinned identity for a (spanner_id,
// bolt_id) pair, or nil if none is pinned. Used by the
// /api/v1/spanner/status surface to show operators the verified
// pubkey for each bolt.
func (h *Hub) LookupBoltPin(spannerID, boltID string) *BoltPin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pin, ok := h.boltPins[pinKey(spannerID, boltID)]
	if !ok {
		return nil
	}
	// Return a copy so callers can't mutate the pinned bytes under
	// the hub's nose.
	pkCopy := make([]byte, len(pin.Pubkey))
	copy(pkCopy, pin.Pubkey)
	return &BoltPin{
		SpannerID: pin.SpannerID,
		BoltID:    pin.BoltID,
		Pubkey:    pkCopy,
		PinnedAt:  pin.PinnedAt,
		LastSeen:  pin.LastSeen,
	}
}

// getBoltReport returns the latest self-report for a bolt, or nil.
func (h *Hub) getBoltReport(boltID string) *pb.BoltStatusReport {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.boltReports[boltID]
}

// fanOutResult holds the response from one bolt.
type fanOutResult struct {
	boltID string
	value  any
	err    error
}

// fanOut executes fn on all bolts concurrently and collects results.
func (h *Hub) fanOut(ctx context.Context, fn func(ctx context.Context, sc boltConn) (any, error)) []fanOutResult {
	var wg sync.WaitGroup
	results := make([]fanOutResult, len(h.bolts))

	for i, sc := range h.bolts {
		wg.Add(1)
		go func(idx int, bolt boltConn) {
			defer wg.Done()
			val, err := fn(ctx, bolt)
			results[idx] = fanOutResult{
				boltID: bolt.id,
				value:  val,
				err:    err,
			}
		}(i, sc)
	}

	wg.Wait()
	return results
}

// ReassignResult holds the outcome of a single envoy reassignment.
type ReassignResult struct {
	NewEnvoyID string
	TargetAddr string
}

// ReassignEnvoy moves a envoy from one bolt to another.
// 1. Gets envoy info from source bolt.
// 2. Enrolls envoy on target bolt.
// 3. Sets reassignment on source bolt so agent reconnects.
func (h *Hub) ReassignEnvoy(ctx context.Context, envoyID, fromBoltID, toBoltID string) (*ReassignResult, error) {
	from := h.BoltByID(fromBoltID)
	if from == nil {
		return nil, fmt.Errorf("source bolt %q not found", fromBoltID)
	}
	to := h.BoltByID(toBoltID)
	if to == nil {
		return nil, fmt.Errorf("target bolt %q not found", toBoltID)
	}

	// Get envoy info from source.
	resp, err := from.admin.GetEnvoy(ctx, &pb.GetEnvoyRequest{Id: envoyID})
	if err != nil {
		return nil, fmt.Errorf("getting envoy from source bolt: %w", err)
	}
	envoy := resp.Envoy

	// Enroll on target bolt.
	newID, err := h.EnrollOnBolt(ctx, to.addr, envoy.Hostname, envoy.IpAddress, "", true)
	if err != nil {
		return nil, fmt.Errorf("enrolling on target bolt: %w", err)
	}

	// Set reassignment on source bolt so agent reconnects on next check-in.
	_, err = from.admin.SetReassignment(ctx, &pb.SetReassignmentRequest{
		EnvoyId:    envoyID,
		TargetAddr: to.addr,
		NewEnvoyId: newID,
	})
	if err != nil {
		return nil, fmt.Errorf("setting reassignment on source bolt: %w", err)
	}

	slog.Info("envoy reassigned",
		"envoy_id", envoyID,
		"from_bolt", fromBoltID,
		"to_bolt", toBoltID,
		"new_envoy_id", newID,
	)

	return &ReassignResult{
		NewEnvoyID: newID,
		TargetAddr: to.addr,
	}, nil
}

// DrainResult holds the outcome of draining a bolt.
type DrainResult struct {
	Moved  int32
	Failed int32
	Errors []string
}

// DrainBolt moves all envoys from a bolt to other bolts.
// If targetBoltID is empty, routes each envoy by hostname pattern.
// If targetBoltID is set, all envoys go to that specific bolt.
func (h *Hub) DrainBolt(ctx context.Context, boltID, targetBoltID string) (*DrainResult, error) {
	src := h.BoltByID(boltID)
	if src == nil {
		return nil, fmt.Errorf("bolt %q not found", boltID)
	}

	// Get all envoys on source bolt.
	envoys, err := h.BoltListEnvoys(ctx, boltID)
	if err != nil {
		return nil, fmt.Errorf("listing envoys on bolt %s: %w", boltID, err)
	}

	result := &DrainResult{}
	for _, t := range envoys {
		if t.Revoked {
			continue
		}

		target := targetBoltID
		if target == "" {
			// Auto-route by hostname against remaining bolts' patterns.
			for _, sc := range h.bolts {
				if sc.id == boltID {
					continue
				}
				for _, pattern := range sc.patterns {
					matched, matchErr := filepath.Match(pattern, t.Hostname)
					if matchErr != nil {
						continue
					}
					if matched {
						target = sc.id
						break
					}
				}
				if target != "" {
					break
				}
			}
		}

		if target == "" {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("no target bolt for %s", t.Hostname))
			continue
		}

		_, err := h.ReassignEnvoy(ctx, t.Id, boltID, target)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("reassign %s: %v", t.Hostname, err))
			continue
		}
		result.Moved++
	}

	slog.Info("bolt drained", "bolt_id", boltID, "moved", result.Moved, "failed", result.Failed)
	return result, nil
}

// buildClientTLS creates TLS credentials for the hub's connections to bolts.
func buildClientTLS(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}), nil
}
