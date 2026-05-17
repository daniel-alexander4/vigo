// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package tungol provides hub coordination for the tungol federation:
// one Hub aggregates admin queries across a configured set of steorras
// (peer vigosrvs that own their own envoy partition). The current
// implementation is still hub-steorra (not yet the peer-equal redesign);
// Phase 1+ will replace the Hub-centric model with peer-equal gossip.
// The Hub connects to each steorra over mTLS gRPC and fans out admin
// queries (ListEnvoys, GetConvergenceSummary, ListRuns, ForcePush)
// across all of them.
package tungol

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

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Hub manages gRPC connections to steorras and fans out admin queries.
type Hub struct {
	steorras       []steorraConn
	steorraReports map[string]*pb.SteorraStatusReport // latest steorra self-reports, protected by mu
	mu             sync.RWMutex
}

type steorraConn struct {
	id       string
	addr     string
	patterns []string // hostname glob patterns routed to this steorra
	conn     *grpc.ClientConn
	admin    pb.VigoAdminClient
}

// NewHub creates a Hub that connects to each configured steorra via mTLS.
func NewHub(ctx context.Context, steorras []config.SteorraEntry, certFile, keyFile, caFile string) (*Hub, error) {
	tlsCreds, err := buildClientTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building steorra TLS credentials: %w", err)
	}

	h := &Hub{
		steorras:       make([]steorraConn, 0, len(steorras)),
		steorraReports: make(map[string]*pb.SteorraStatusReport),
	}

	for _, s := range steorras {
		conn, err := grpc.DialContext(ctx, s.Addr,
			grpc.WithTransportCredentials(tlsCreds),
			grpc.WithBlock(),
			grpc.WithTimeout(5*time.Second),
		)
		if err != nil {
			// Close any already-opened connections.
			h.Close()
			return nil, fmt.Errorf("connecting to steorra %s at %s: %w", s.ID, s.Addr, err)
		}

		h.steorras = append(h.steorras, steorraConn{
			id:       s.ID,
			addr:     s.Addr,
			patterns: s.Patterns,
			conn:     conn,
			admin:    pb.NewVigoAdminClient(conn),
		})
		slog.Info("connected to steorra", "steorra_id", s.ID, "addr", s.Addr)
	}

	return h, nil
}

// Close shuts down all steorra connections.
func (h *Hub) Close() {
	for _, s := range h.steorras {
		if s.conn != nil {
			s.conn.Close()
		}
	}
}

// SteorraCount returns the number of configured steorras.
func (h *Hub) SteorraCount() int {
	return len(h.steorras)
}

// AggregateListEnvoys fans out ListEnvoys to all steorras and merges results.
func (h *Hub) AggregateListEnvoys(ctx context.Context) ([]*pb.Envoy, error) {
	type result struct {
		envoys []*pb.Envoy
		err   error
	}

	results := h.fanOut(ctx, func(ctx context.Context, sc steorraConn) (any, error) {
		resp, err := sc.admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Envoys, nil
	})

	var allNodes []*pb.Envoy
	for _, r := range results {
		if r.err != nil {
			slog.Warn("steorra ListEnvoys failed", "steorra_id", r.steorraID, "error", r.err)
			continue
		}
		allNodes = append(allNodes, r.value.([]*pb.Envoy)...)
	}
	return allNodes, nil
}

// AggregateConvergenceSummary fans out GetConvergenceSummary to all steorras
// and sums the results.
func (h *Hub) AggregateConvergenceSummary(ctx context.Context) (*pb.GetConvergenceSummaryResponse, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc steorraConn) (any, error) {
		return sc.admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
	})

	agg := &pb.GetConvergenceSummaryResponse{}
	steorraCount := 0
	for _, r := range results {
		if r.err != nil {
			slog.Warn("steorra GetConvergenceSummary failed", "steorra_id", r.steorraID, "error", r.err)
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
		steorraCount++
	}

	// Lost-contact envoys excluded from compliance percentage — they represent
	// powered-off or decommissioned nodes, not health problems.
	activeTotal := agg.Total - agg.Offline
	if activeTotal > 0 {
		agg.ConvergencePct = float32(agg.Converged) / float32(activeTotal) * 100
	}

	return agg, nil
}

// AggregateListRuns fans out ListRuns to all steorras and merges results.
// If envoyID is non-empty, only queries steorras (all of them — fan-out is cheap).
func (h *Hub) AggregateListRuns(ctx context.Context, envoyID string, limit int32) ([]*pb.Run, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc steorraConn) (any, error) {
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
			slog.Warn("steorra ListRuns failed", "steorra_id", r.steorraID, "error", r.err)
			continue
		}
		allRuns = append(allRuns, r.value.([]*pb.Run)...)
	}
	return allRuns, nil
}

// ForwardForcePush sends a ForcePush to all steorras (or targeted if envoyID
// is non-empty). Returns total envoys notified across all steorras.
func (h *Hub) ForwardForcePush(ctx context.Context, envoyID string) (int32, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc steorraConn) (any, error) {
		resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{EnvoyId: envoyID})
		if err != nil {
			return nil, err
		}
		return resp.EnvoysNotified, nil
	})

	var total int32
	for _, r := range results {
		if r.err != nil {
			slog.Warn("steorra ForcePush failed", "steorra_id", r.steorraID, "error", r.err)
			continue
		}
		total += r.value.(int32)
	}
	return total, nil
}

// RouteHostname determines which steorra should handle a given hostname by
// matching against each steorra's configured patterns. Returns the steorra's
// gRPC address and true if a match is found, or empty string and false if
// no steorra pattern matches (the hub handles it locally).
func (h *Hub) RouteHostname(hostname string) (addr string, ok bool) {
	for _, sc := range h.steorras {
		for _, pattern := range sc.patterns {
			matched, err := filepath.Match(pattern, hostname)
			if err != nil {
				slog.Warn("invalid steorra pattern", "steorra_id", sc.id, "pattern", pattern, "error", err)
				continue
			}
			if matched {
				return sc.addr, true
			}
		}
	}
	return "", false
}

// EnrollOnSteorra creates a envoy record on the target steorra via the EnrollEnvoy
// admin RPC. Returns the envoy UUID assigned by the steorra.
func (h *Hub) EnrollOnSteorra(ctx context.Context, steorraAddr, hostname, ipAddress, pubkeyPEM string, force bool) (string, error) {
	// Find the steorra connection by address.
	var target *steorraConn
	for i := range h.steorras {
		if h.steorras[i].addr == steorraAddr {
			target = &h.steorras[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("no steorra connection for address %s", steorraAddr)
	}

	resp, err := target.admin.EnrollEnvoy(ctx, &pb.EnrollEnvoyRequest{
		Hostname:  hostname,
		IpAddress: ipAddress,
		PubkeyPem: pubkeyPEM,
		Force:     force,
	})
	if err != nil {
		return "", fmt.Errorf("enroll on steorra %s: %w", target.id, err)
	}

	slog.Info("enrolled envoy on steorra", "steorra_id", target.id, "hostname", hostname, "envoy_id", resp.EnvoyId)
	return resp.EnvoyId, nil
}

// SteorraByID returns the steorra connection for the given ID, or nil if not found.
func (h *Hub) SteorraByID(id string) *steorraConn {
	for i := range h.steorras {
		if h.steorras[i].id == id {
			return &h.steorras[i]
		}
	}
	return nil
}

// SteorraListEnvoys queries a single steorra for its envoy list.
func (h *Hub) SteorraListEnvoys(ctx context.Context, steorraID string) ([]*pb.Envoy, error) {
	sc := h.SteorraByID(steorraID)
	if sc == nil {
		return nil, fmt.Errorf("steorra %q not found", steorraID)
	}
	resp, err := sc.admin.ListEnvoys(ctx, &pb.ListEnvoysRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEnvoys on steorra %s: %w", steorraID, err)
	}
	return resp.Envoys, nil
}

// SteorraListRuns queries a single steorra for its run list.
func (h *Hub) SteorraListRuns(ctx context.Context, steorraID string, limit int32) ([]*pb.Run, error) {
	sc := h.SteorraByID(steorraID)
	if sc == nil {
		return nil, fmt.Errorf("steorra %q not found", steorraID)
	}
	resp, err := sc.admin.ListRuns(ctx, &pb.ListRunsRequest{Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("ListRuns on steorra %s: %w", steorraID, err)
	}
	return resp.Runs, nil
}

// SteorraConvergenceSummary queries a single steorra for its compliance summary.
func (h *Hub) SteorraConvergenceSummary(ctx context.Context, steorraID string) (*pb.GetConvergenceSummaryResponse, error) {
	sc := h.SteorraByID(steorraID)
	if sc == nil {
		return nil, fmt.Errorf("steorra %q not found", steorraID)
	}
	return sc.admin.GetConvergenceSummary(ctx, &pb.GetConvergenceSummaryRequest{})
}

// SteorraForcePush sends a ForcePush to a single steorra.
func (h *Hub) SteorraForcePush(ctx context.Context, steorraID, envoyID string) (int32, error) {
	sc := h.SteorraByID(steorraID)
	if sc == nil {
		return 0, fmt.Errorf("steorra %q not found", steorraID)
	}
	resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{EnvoyId: envoyID})
	if err != nil {
		return 0, fmt.Errorf("ForcePush on steorra %s: %w", steorraID, err)
	}
	return resp.EnvoysNotified, nil
}

// SteorraStatus describes the health and summary of a single steorra.
type SteorraStatus struct {
	ID             string   `json:"id"`
	Addr           string   `json:"addr"`
	Patterns       []string `json:"patterns"`
	Healthy        bool     `json:"healthy"`
	EnvoyCount    int32    `json:"envoy_count"`
	Converged      int32    `json:"converged"`
	Relapsed       int32    `json:"relapsed"`
	Diverged       int32    `json:"diverged"`
	Failed         int32    `json:"failed"`
	Offline    int32    `json:"offline"`
	NoData         int32    `json:"no_data"`
	ConvergencePct float32  `json:"compliance_pct"`
	LatencyMs      int64    `json:"latency_ms"`
	ConfigVersion  uint64   `json:"config_version,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// probeResult carries both the compliance response and the latency from a steorra probe.
type probeResult struct {
	resp    *pb.GetConvergenceSummaryResponse
	latency time.Duration
}

// SteorraStatuses probes each steorra's health by calling GetConvergenceSummary
// with a short timeout and returns the results with compliance breakdown and latency.
// If a steorra is unreachable but has a recent self-report, the report data is
// used but the steorra is still marked unhealthy.
func (h *Hub) SteorraStatuses(ctx context.Context) []SteorraStatus {
	results := h.fanOut(ctx, func(ctx context.Context, sc steorraConn) (any, error) {
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

	statuses := make([]SteorraStatus, len(h.steorras))
	for i, sc := range h.steorras {
		statuses[i] = SteorraStatus{
			ID:       sc.id,
			Addr:     sc.addr,
			Patterns: sc.patterns,
		}
		r := results[i]
		if r.err != nil {
			statuses[i].Error = r.err.Error()
			if pr, ok := r.value.(*probeResult); ok {
				statuses[i].LatencyMs = pr.latency.Milliseconds()
				metrics.TungolSteorraLatency.WithLabelValues(sc.id).Observe(pr.latency.Seconds())
			}
			metrics.TungolSteorraUp.WithLabelValues(sc.id).Set(0)

			// If probe failed but we have a self-report from the steorra,
			// populate compliance data so dashboards are not blank.
			if report := h.getSteorraReport(sc.id); report != nil {
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
			metrics.TungolSteorraLatency.WithLabelValues(sc.id).Observe(pr.latency.Seconds())
			metrics.TungolSteorraUp.WithLabelValues(sc.id).Set(1)
		}
	}
	return statuses
}

// RecordSteorraReport stores a steorra's self-reported status. Called by the
// ReportSteorraStatus gRPC handler on the hub.
func (h *Hub) RecordSteorraReport(report *pb.SteorraStatusReport) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.steorraReports[report.SteorraId] = report
}

// getSteorraReport returns the latest self-report for a steorra, or nil.
func (h *Hub) getSteorraReport(steorraID string) *pb.SteorraStatusReport {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.steorraReports[steorraID]
}

// fanOutResult holds the response from one steorra.
type fanOutResult struct {
	steorraID string
	value     any
	err       error
}

// fanOut executes fn on all steorras concurrently and collects results.
func (h *Hub) fanOut(ctx context.Context, fn func(ctx context.Context, sc steorraConn) (any, error)) []fanOutResult {
	var wg sync.WaitGroup
	results := make([]fanOutResult, len(h.steorras))

	for i, sc := range h.steorras {
		wg.Add(1)
		go func(idx int, steorra steorraConn) {
			defer wg.Done()
			val, err := fn(ctx, steorra)
			results[idx] = fanOutResult{
				steorraID: steorra.id,
				value:     val,
				err:       err,
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

// ReassignEnvoy moves a envoy from one steorra to another.
// 1. Gets envoy info from source steorra.
// 2. Enrolls envoy on target steorra.
// 3. Sets reassignment on source steorra so agent reconnects.
func (h *Hub) ReassignEnvoy(ctx context.Context, envoyID, fromSteorraID, toSteorraID string) (*ReassignResult, error) {
	from := h.SteorraByID(fromSteorraID)
	if from == nil {
		return nil, fmt.Errorf("source steorra %q not found", fromSteorraID)
	}
	to := h.SteorraByID(toSteorraID)
	if to == nil {
		return nil, fmt.Errorf("target steorra %q not found", toSteorraID)
	}

	// Get envoy info from source.
	resp, err := from.admin.GetEnvoy(ctx, &pb.GetEnvoyRequest{Id: envoyID})
	if err != nil {
		return nil, fmt.Errorf("getting envoy from source steorra: %w", err)
	}
	envoy := resp.Envoy

	// Enroll on target steorra.
	newID, err := h.EnrollOnSteorra(ctx, to.addr, envoy.Hostname, envoy.IpAddress, "", true)
	if err != nil {
		return nil, fmt.Errorf("enrolling on target steorra: %w", err)
	}

	// Set reassignment on source steorra so agent reconnects on next check-in.
	_, err = from.admin.SetReassignment(ctx, &pb.SetReassignmentRequest{
		EnvoyId:    envoyID,
		TargetAddr:  to.addr,
		NewEnvoyId: newID,
	})
	if err != nil {
		return nil, fmt.Errorf("setting reassignment on source steorra: %w", err)
	}

	slog.Info("envoy reassigned",
		"envoy_id", envoyID,
		"from_steorra", fromSteorraID,
		"to_steorra", toSteorraID,
		"new_envoy_id", newID,
	)

	return &ReassignResult{
		NewEnvoyID: newID,
		TargetAddr:  to.addr,
	}, nil
}

// DrainResult holds the outcome of draining a steorra.
type DrainResult struct {
	Moved  int32
	Failed int32
	Errors []string
}

// DrainSteorra moves all envoys from a steorra to other steorras.
// If targetSteorraID is empty, routes each envoy by hostname pattern.
// If targetSteorraID is set, all envoys go to that specific steorra.
func (h *Hub) DrainSteorra(ctx context.Context, steorraID, targetSteorraID string) (*DrainResult, error) {
	src := h.SteorraByID(steorraID)
	if src == nil {
		return nil, fmt.Errorf("steorra %q not found", steorraID)
	}

	// Get all envoys on source steorra.
	envoys, err := h.SteorraListEnvoys(ctx, steorraID)
	if err != nil {
		return nil, fmt.Errorf("listing envoys on steorra %s: %w", steorraID, err)
	}

	result := &DrainResult{}
	for _, t := range envoys {
		if t.Revoked {
			continue
		}

		target := targetSteorraID
		if target == "" {
			// Auto-route by hostname against remaining steorras' patterns.
			for _, sc := range h.steorras {
				if sc.id == steorraID {
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
			result.Errors = append(result.Errors, fmt.Sprintf("no target steorra for %s", t.Hostname))
			continue
		}

		_, err := h.ReassignEnvoy(ctx, t.Id, steorraID, target)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, fmt.Sprintf("reassign %s: %v", t.Hostname, err))
			continue
		}
		result.Moved++
	}

	slog.Info("steorra drained", "steorra_id", steorraID, "moved", result.Moved, "failed", result.Failed)
	return result, nil
}

// SyncConfigToSteorras creates a tar.gz archive of the config directory and
// pushes it to all steorras via the SyncConfig RPC. Best-effort: logs failures
// but returns nil on partial success. Only returns an error if the archive
// cannot be created at all.
func (h *Hub) SyncConfigToSteorras(ctx context.Context, configDir string) error {
	archive, err := createConfigArchive(configDir)
	if err != nil {
		return fmt.Errorf("creating config archive: %w", err)
	}

	slog.Info("syncing config to steorras", "archive_bytes", len(archive), "steorras", len(h.steorras))

	results := h.fanOut(ctx, func(ctx context.Context, sc steorraConn) (any, error) {
		steorraCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return sc.admin.SyncConfig(steorraCtx, &pb.SyncConfigRequest{
			ConfigArchive: archive,
		})
	})

	for _, r := range results {
		if r.err != nil {
			slog.Warn("config sync to steorra failed", "steorra_id", r.steorraID, "error", r.err)
		} else {
			slog.Info("config synced to steorra", "steorra_id", r.steorraID)
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

// buildClientTLS creates TLS credentials for the hub's connections to steorras.
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
