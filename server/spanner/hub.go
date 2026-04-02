// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package spanner provides hub-spoke coordination for spanner deployments.
// The Hub connects to spoke servers via gRPC and aggregates admin queries
// (ListEnvoys, GetConvergenceSummary, ListRuns, ForcePush) across all spokes.
package spanner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Hub manages gRPC connections to spoke servers and fans out admin queries.
type Hub struct {
	spokes       []spokeConn
	spokeReports map[string]*pb.SpokeStatusReport // latest spoke self-reports, protected by mu
	mu           sync.RWMutex
}

type spokeConn struct {
	id       string
	addr     string
	patterns []string // hostname glob patterns routed to this spoke
	conn     *grpc.ClientConn
	admin    pb.VigoAdminClient
}

// NewHub creates a Hub that connects to each configured spoke via mTLS.
func NewHub(ctx context.Context, spokes []config.SpokeEntry, certFile, keyFile, caFile string) (*Hub, error) {
	tlsCreds, err := buildClientTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building spoke TLS credentials: %w", err)
	}

	h := &Hub{
		spokes:       make([]spokeConn, 0, len(spokes)),
		spokeReports: make(map[string]*pb.SpokeStatusReport),
	}

	for _, s := range spokes {
		conn, err := grpc.DialContext(ctx, s.Addr,
			grpc.WithTransportCredentials(tlsCreds),
			grpc.WithBlock(),
			grpc.WithTimeout(5*time.Second),
		)
		if err != nil {
			// Close any already-opened connections.
			h.Close()
			return nil, fmt.Errorf("connecting to spoke %s at %s: %w", s.ID, s.Addr, err)
		}

		h.spokes = append(h.spokes, spokeConn{
			id:       s.ID,
			addr:     s.Addr,
			patterns: s.Patterns,
			conn:     conn,
			admin:    pb.NewVigoAdminClient(conn),
		})
		slog.Info("connected to spoke", "spoke_id", s.ID, "addr", s.Addr)
	}

	return h, nil
}

// Close shuts down all spoke connections.
func (h *Hub) Close() {
	for _, s := range h.spokes {
		if s.conn != nil {
			s.conn.Close()
		}
	}
}

// SpokeCount returns the number of configured spokes.
func (h *Hub) SpokeCount() int {
	return len(h.spokes)
}

// AggregateListEnvoys fans out ListEnvoys to all spokes and merges results.
func (h *Hub) AggregateListEnvoys(ctx context.Context) ([]*pb.Envoy, error) {
	type result struct {
		envoys []*pb.Envoy
		err   error
	}

	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		resp, err := sc.admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Envoys, nil
	})

	var allNodes []*pb.Envoy
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spoke ListEnvoys failed", "spoke_id", r.spokeID, "error", r.err)
			continue
		}
		allNodes = append(allNodes, r.value.([]*pb.Envoy)...)
	}
	return allNodes, nil
}

// AggregateConvergenceSummary fans out GetConvergenceSummary to all spokes
// and sums the results.
func (h *Hub) AggregateConvergenceSummary(ctx context.Context) (*pb.GetConvergenceSummaryResponse, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		return sc.admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
	})

	agg := &pb.GetConvergenceSummaryResponse{}
	spokeCount := 0
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spoke GetConvergenceSummary failed", "spoke_id", r.spokeID, "error", r.err)
			continue
		}
		resp := r.value.(*pb.GetConvergenceSummaryResponse)
		agg.Total += resp.Total
		agg.Converged += resp.Converged
		agg.Relapsed += resp.Relapsed
		agg.Diverged += resp.Diverged
		agg.Failed += resp.Failed
		agg.LostContact += resp.LostContact
		agg.NoData += resp.NoData
		spokeCount++
	}

	// Lost-contact envoys excluded from compliance percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := agg.Total - agg.LostContact
	if activeTotal > 0 {
		agg.ConvergencePct = float32(agg.Converged) / float32(activeTotal) * 100
	}

	return agg, nil
}

// AggregateListRuns fans out ListRuns to all spokes and merges results.
// If envoyID is non-empty, only queries spokes (all of them — fan-out is cheap).
func (h *Hub) AggregateListRuns(ctx context.Context, envoyID string, limit int32) ([]*pb.Run, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		resp, err := sc.admin.ListRuns(ctx, &pb.ListRunsRequest{
			EnvoyId: envoyID,
			Limit:  limit,
		})
		if err != nil {
			return nil, err
		}
		return resp.Runs, nil
	})

	var allRuns []*pb.Run
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spoke ListRuns failed", "spoke_id", r.spokeID, "error", r.err)
			continue
		}
		allRuns = append(allRuns, r.value.([]*pb.Run)...)
	}
	return allRuns, nil
}

// ForwardForcePush sends a ForcePush to all spokes (or targeted if envoyID
// is non-empty). Returns total envoys notified across all spokes.
func (h *Hub) ForwardForcePush(ctx context.Context, envoyID string) (int32, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{EnvoyId: envoyID})
		if err != nil {
			return nil, err
		}
		return resp.EnvoysNotified, nil
	})

	var total int32
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spoke ForcePush failed", "spoke_id", r.spokeID, "error", r.err)
			continue
		}
		total += r.value.(int32)
	}
	return total, nil
}

// RouteHostname determines which spoke should handle a given hostname by
// matching against each spoke's configured patterns. Returns the spoke's
// gRPC address and true if a match is found, or empty string and false if
// no spoke pattern matches (the hub handles it locally).
func (h *Hub) RouteHostname(hostname string) (addr string, ok bool) {
	for _, sc := range h.spokes {
		for _, pattern := range sc.patterns {
			matched, err := filepath.Match(pattern, hostname)
			if err != nil {
				slog.Warn("invalid spoke pattern", "spoke_id", sc.id, "pattern", pattern, "error", err)
				continue
			}
			if matched {
				return sc.addr, true
			}
		}
	}
	return "", false
}

// EnrollOnSpoke creates a envoy record on the target spoke via the EnrollEnvoy
// admin RPC. Returns the envoy UUID assigned by the spoke.
func (h *Hub) EnrollOnSpoke(ctx context.Context, spokeAddr, hostname, ipAddress, pubkeyPEM string, force bool) (string, error) {
	// Find the spoke connection by address.
	var target *spokeConn
	for i := range h.spokes {
		if h.spokes[i].addr == spokeAddr {
			target = &h.spokes[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("no spoke connection for address %s", spokeAddr)
	}

	resp, err := target.admin.EnrollEnvoy(ctx, &pb.EnrollEnvoyRequest{
		Hostname:  hostname,
		IpAddress: ipAddress,
		PubkeyPem: pubkeyPEM,
		Force:     force,
	})
	if err != nil {
		return "", fmt.Errorf("enroll on spoke %s: %w", target.id, err)
	}

	slog.Info("enrolled envoy on spoke", "spoke_id", target.id, "hostname", hostname, "node_id", resp.EnvoyId)
	return resp.EnvoyId, nil
}

// SpokeByID returns the spoke connection for the given ID, or nil if not found.
func (h *Hub) SpokeByID(id string) *spokeConn {
	for i := range h.spokes {
		if h.spokes[i].id == id {
			return &h.spokes[i]
		}
	}
	return nil
}

// SpokeListEnvoys queries a single spoke for its envoy list.
func (h *Hub) SpokeListEnvoys(ctx context.Context, spokeID string) ([]*pb.Envoy, error) {
	sc := h.SpokeByID(spokeID)
	if sc == nil {
		return nil, fmt.Errorf("spoke %q not found", spokeID)
	}
	resp, err := sc.admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEnvoys on spoke %s: %w", spokeID, err)
	}
	return resp.Envoys, nil
}

// SpokeListRuns queries a single spoke for its run list.
func (h *Hub) SpokeListRuns(ctx context.Context, spokeID string, limit int32) ([]*pb.Run, error) {
	sc := h.SpokeByID(spokeID)
	if sc == nil {
		return nil, fmt.Errorf("spoke %q not found", spokeID)
	}
	resp, err := sc.admin.ListRuns(ctx, &pb.ListRunsRequest{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("ListRuns on spoke %s: %w", spokeID, err)
	}
	return resp.Runs, nil
}

// SpokeConvergenceSummary queries a single spoke for its compliance summary.
func (h *Hub) SpokeConvergenceSummary(ctx context.Context, spokeID string) (*pb.GetConvergenceSummaryResponse, error) {
	sc := h.SpokeByID(spokeID)
	if sc == nil {
		return nil, fmt.Errorf("spoke %q not found", spokeID)
	}
	return sc.admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
}

// SpokeForcePush sends a ForcePush to a single spoke.
func (h *Hub) SpokeForcePush(ctx context.Context, spokeID, envoyID string) (int32, error) {
	sc := h.SpokeByID(spokeID)
	if sc == nil {
		return 0, fmt.Errorf("spoke %q not found", spokeID)
	}
	resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{EnvoyId: envoyID})
	if err != nil {
		return 0, fmt.Errorf("ForcePush on spoke %s: %w", spokeID, err)
	}
	return resp.EnvoysNotified, nil
}

// SpokeStatus describes the health and summary of a single spoke.
type SpokeStatus struct {
	ID             string   `json:"id"`
	Addr           string   `json:"addr"`
	Patterns       []string `json:"patterns"`
	Healthy        bool     `json:"healthy"`
	EnvoyCount    int32    `json:"envoy_count"`
	Converged      int32    `json:"converged"`
	Relapsed       int32    `json:"relapsed"`
	Diverged       int32    `json:"diverged"`
	Failed         int32    `json:"failed"`
	LostContact    int32    `json:"lost_contact"`
	NoData         int32    `json:"no_data"`
	ConvergencePct float32  `json:"compliance_pct"`
	LatencyMs      int64    `json:"latency_ms"`
	ConfigVersion  uint64   `json:"config_version,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// probeResult carries both the compliance response and the latency from a spoke probe.
type probeResult struct {
	resp    *pb.GetConvergenceSummaryResponse
	latency time.Duration
}

// SpokeStatuses probes each spoke's health by calling GetConvergenceSummary
// with a short timeout and returns the results with compliance breakdown and latency.
// If a spoke is unreachable but has a recent self-report, the report data is
// used but the spoke is still marked unhealthy.
func (h *Hub) SpokeStatuses(ctx context.Context) []SpokeStatus {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
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

	statuses := make([]SpokeStatus, len(h.spokes))
	for i, sc := range h.spokes {
		statuses[i] = SpokeStatus{
			ID:       sc.id,
			Addr:     sc.addr,
			Patterns: sc.patterns,
		}
		r := results[i]
		if r.err != nil {
			statuses[i].Error = r.err.Error()
			if pr, ok := r.value.(*probeResult); ok {
				statuses[i].LatencyMs = pr.latency.Milliseconds()
				metrics.SpannerSpokeLatency.WithLabelValues(sc.id).Observe(pr.latency.Seconds())
			}
			metrics.SpannerSpokeUp.WithLabelValues(sc.id).Set(0)

			// If probe failed but we have a self-report from the spoke,
			// populate compliance data so dashboards are not blank.
			if report := h.getSpokeReport(sc.id); report != nil {
				statuses[i].EnvoyCount = report.EnvoyCount
				statuses[i].Converged = report.Converged
				statuses[i].Relapsed = report.Relapsed
				statuses[i].Diverged = report.Diverged
				statuses[i].Failed = report.Failed
				statuses[i].LostContact = report.LostContact
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
			statuses[i].LostContact = pr.resp.LostContact
			statuses[i].NoData = pr.resp.NoData
			statuses[i].ConvergencePct = pr.resp.ConvergencePct
			statuses[i].LatencyMs = pr.latency.Milliseconds()
			metrics.SpannerSpokeLatency.WithLabelValues(sc.id).Observe(pr.latency.Seconds())
			metrics.SpannerSpokeUp.WithLabelValues(sc.id).Set(1)
		}
	}
	return statuses
}

// RecordSpokeReport stores a spoke's self-reported status. Called by the
// ReportSpokeStatus gRPC handler on the hub.
func (h *Hub) RecordSpokeReport(report *pb.SpokeStatusReport) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spokeReports[report.SpokeId] = report
}

// getSpokeReport returns the latest self-report for a spoke, or nil.
func (h *Hub) getSpokeReport(spokeID string) *pb.SpokeStatusReport {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.spokeReports[spokeID]
}

// fanOutResult holds the response from one spoke.
type fanOutResult struct {
	spokeID string
	value   any
	err     error
}

// fanOut executes fn on all spokes concurrently and collects results.
func (h *Hub) fanOut(ctx context.Context, fn func(ctx context.Context, sc spokeConn) (any, error)) []fanOutResult {
	var wg sync.WaitGroup
	results := make([]fanOutResult, len(h.spokes))

	for i, sc := range h.spokes {
		wg.Add(1)
		go func(idx int, spoke spokeConn) {
			defer wg.Done()
			val, err := fn(ctx, spoke)
			results[idx] = fanOutResult{
				spokeID: spoke.id,
				value:   val,
				err:     err,
			}
		}(i, sc)
	}

	wg.Wait()
	return results
}

// ReassignResult holds the outcome of a single envoy reassignment.
type ReassignResult struct {
	NewEnvoyID string
	TargetAddr  string
}

// ReassignEnvoy moves a envoy from one spoke to another.
// 1. Gets envoy info from source spoke.
// 2. Enrolls envoy on target spoke.
// 3. Sets reassignment on source spoke so agent reconnects.
func (h *Hub) ReassignEnvoy(ctx context.Context, envoyID, fromSpokeID, toSpokeID string) (*ReassignResult, error) {
	from := h.SpokeByID(fromSpokeID)
	if from == nil {
		return nil, fmt.Errorf("source spoke %q not found", fromSpokeID)
	}
	to := h.SpokeByID(toSpokeID)
	if to == nil {
		return nil, fmt.Errorf("target spoke %q not found", toSpokeID)
	}

	// Get envoy info from source.
	resp, err := from.admin.GetEnvoy(ctx, &pb.GetEnvoyRequest{Id: envoyID})
	if err != nil {
		return nil, fmt.Errorf("getting envoy from source spoke: %w", err)
	}
	envoy := resp.Envoy

	// Enroll on target spoke.
	newID, err := h.EnrollOnSpoke(ctx, to.addr, envoy.Hostname, envoy.IpAddress, "", true)
	if err != nil {
		return nil, fmt.Errorf("enrolling on target spoke: %w", err)
	}

	// Set reassignment on source spoke so agent reconnects on next check-in.
	_, err = from.admin.SetReassignment(ctx, &pb.SetReassignmentRequest{
		EnvoyId:    envoyID,
		TargetAddr:  to.addr,
		NewEnvoyId: newID,
	})
	if err != nil {
		return nil, fmt.Errorf("setting reassignment on source spoke: %w", err)
	}

	slog.Info("envoy reassigned",
		"envoy_id", envoyID,
		"from_spoke", fromSpokeID,
		"to_spoke", toSpokeID,
		"new_envoy_id", newID,
	)

	return &ReassignResult{
		NewEnvoyID: newID,
		TargetAddr:  to.addr,
	}, nil
}

// DrainResult holds the outcome of draining a spoke.
type DrainResult struct {
	Moved  int32
	Failed int32
	Errors []string
}

// DrainSpoke moves all envoys from a spoke to other spokes.
// If targetSpokeID is empty, routes each envoy by hostname pattern.
// If targetSpokeID is set, all envoys go to that specific spoke.
func (h *Hub) DrainSpoke(ctx context.Context, spokeID, targetSpokeID string) (*DrainResult, error) {
	src := h.SpokeByID(spokeID)
	if src == nil {
		return nil, fmt.Errorf("spoke %q not found", spokeID)
	}

	// Get all envoys on source spoke.
	envoys, err := h.SpokeListEnvoys(ctx, spokeID)
	if err != nil {
		return nil, fmt.Errorf("listing envoys on spoke %s: %w", spokeID, err)
	}

	result := &DrainResult{}
	for _, t := range envoys {
		if t.Revoked {
			continue
		}

		target := targetSpokeID
		if target == "" {
			// Auto-route by hostname against remaining spokes' patterns.
			for _, sc := range h.spokes {
				if sc.id == spokeID {
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
			result.Errors = append(result.Errors, fmt.Sprintf("no target spoke for %s", t.Hostname))
			continue
		}

		_, err := h.ReassignEnvoy(ctx, t.Id, spokeID, target)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("reassign %s: %v", t.Hostname, err))
			continue
		}
		result.Moved++
	}

	slog.Info("spoke drained", "spoke_id", spokeID, "moved", result.Moved, "failed", result.Failed)
	return result, nil
}

// SyncConfigToSpokes creates a tar.gz archive of the config directory and
// pushes it to all spokes via the SyncConfig RPC. Best-effort: logs failures
// but returns nil on partial success. Only returns an error if the archive
// cannot be created at all.
func (h *Hub) SyncConfigToSpokes(ctx context.Context, configDir string) error {
	archive, err := createConfigArchive(configDir)
	if err != nil {
		return fmt.Errorf("creating config archive: %w", err)
	}

	slog.Info("syncing config to spokes", "archive_bytes", len(archive), "spokes", len(h.spokes))

	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		spokeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return sc.admin.SyncConfig(spokeCtx, &pb.SyncConfigRequest{
			ConfigArchive: archive,
		})
	})

	for _, r := range results {
		if r.err != nil {
			slog.Warn("config sync to spoke failed", "spoke_id", r.spokeID, "error", r.err)
		} else {
			slog.Info("config synced to spoke", "spoke_id", r.spokeID)
		}
	}

	return nil
}

// createConfigArchive creates a tar.gz archive of the given directory in memory.
// Files are archived with paths relative to the directory root.
func createConfigArchive(dir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Skip the root directory itself.
		if rel == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		// Use forward-slash relative paths in the archive.
		header.Name = filepath.ToSlash(rel)

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if d.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// buildClientTLS creates TLS credentials for the hub's connections to spokes.
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
