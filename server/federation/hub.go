// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

// Package federation provides hub-spoke coordination for federated deployments.
// The Hub connects to spoke servers via gRPC and aggregates admin queries
// (ListThralls, GetComplianceSummary, ListRuns, ForcePush) across all spokes.
package federation

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

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Hub manages gRPC connections to spoke servers and fans out admin queries.
type Hub struct {
	spokes []spokeConn
}

type spokeConn struct {
	id       string
	addr     string
	patterns []string // hostname glob patterns routed to this spoke
	conn     *grpc.ClientConn
	admin    pb.HobnobAdminClient
}

// NewHub creates a Hub that connects to each configured spoke via mTLS.
func NewHub(ctx context.Context, spokes []config.SpokeEntry, certFile, keyFile, caFile string) (*Hub, error) {
	tlsCreds, err := buildClientTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building spoke TLS credentials: %w", err)
	}

	h := &Hub{spokes: make([]spokeConn, 0, len(spokes))}

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
			admin:    pb.NewHobnobAdminClient(conn),
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

// AggregateListThralls fans out ListThralls to all spokes and merges results.
func (h *Hub) AggregateListThralls(ctx context.Context) ([]*pb.Thrall, error) {
	type result struct {
		thralls []*pb.Thrall
		err   error
	}

	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		resp, err := sc.admin.ListThralls(ctx, &pb.ListThrallsRequest{})
		if err != nil {
			return nil, err
		}
		return resp.Thralls, nil
	})

	var allNodes []*pb.Thrall
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spoke ListThralls failed", "spoke_id", r.spokeID, "error", r.err)
			continue
		}
		allNodes = append(allNodes, r.value.([]*pb.Thrall)...)
	}
	return allNodes, nil
}

// AggregateComplianceSummary fans out GetComplianceSummary to all spokes
// and sums the results.
func (h *Hub) AggregateComplianceSummary(ctx context.Context) (*pb.GetComplianceSummaryResponse, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		return sc.admin.GetComplianceSummary(ctx, &pb.GetComplianceSummaryRequest{})
	})

	agg := &pb.GetComplianceSummaryResponse{}
	spokeCount := 0
	for _, r := range results {
		if r.err != nil {
			slog.Warn("spoke GetComplianceSummary failed", "spoke_id", r.spokeID, "error", r.err)
			continue
		}
		resp := r.value.(*pb.GetComplianceSummaryResponse)
		agg.Total += resp.Total
		agg.Compliant += resp.Compliant
		agg.Drifted += resp.Drifted
		agg.Failing += resp.Failing
		agg.Stale += resp.Stale
		agg.Unknown += resp.Unknown
		spokeCount++
	}

	if agg.Total > 0 {
		agg.CompliancePct = float32(agg.Compliant) / float32(agg.Total) * 100
	}

	return agg, nil
}

// AggregateListRuns fans out ListRuns to all spokes and merges results.
// If thrallID is non-empty, only queries spokes (all of them — fan-out is cheap).
func (h *Hub) AggregateListRuns(ctx context.Context, thrallID string, limit int32) ([]*pb.Run, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		resp, err := sc.admin.ListRuns(ctx, &pb.ListRunsRequest{
			ThrallId: thrallID,
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

// ForwardForcePush sends a ForcePush to all spokes (or targeted if thrallID
// is non-empty). Returns total thralls notified across all spokes.
func (h *Hub) ForwardForcePush(ctx context.Context, thrallID string) (int32, error) {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		resp, err := sc.admin.ForcePush(ctx, &pb.ForcePushRequest{ThrallId: thrallID})
		if err != nil {
			return nil, err
		}
		return resp.ThrallsNotified, nil
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

// EnrollOnSpoke creates a thrall record on the target spoke via the EnrollThrall
// admin RPC. Returns the thrall UUID assigned by the spoke.
func (h *Hub) EnrollOnSpoke(ctx context.Context, spokeAddr, hostname, ipAddress, pubkeyPEM string) (string, error) {
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

	resp, err := target.admin.EnrollThrall(ctx, &pb.EnrollThrallRequest{
		Hostname:  hostname,
		IpAddress: ipAddress,
		PubkeyPem: pubkeyPEM,
	})
	if err != nil {
		return "", fmt.Errorf("enroll on spoke %s: %w", target.id, err)
	}

	slog.Info("enrolled thrall on spoke", "spoke_id", target.id, "hostname", hostname, "node_id", resp.ThrallId)
	return resp.ThrallId, nil
}

// SpokeStatus describes the health and summary of a single spoke.
type SpokeStatus struct {
	ID          string   `json:"id"`
	Addr        string   `json:"addr"`
	Patterns    []string `json:"patterns"`
	Healthy     bool     `json:"healthy"`
	ThrallCount int32    `json:"thrall_count"`
	Error       string   `json:"error,omitempty"`
}

// SpokeStatuses probes each spoke's health by calling GetComplianceSummary
// with a short timeout and returns the results.
func (h *Hub) SpokeStatuses(ctx context.Context) []SpokeStatus {
	results := h.fanOut(ctx, func(ctx context.Context, sc spokeConn) (any, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return sc.admin.GetComplianceSummary(probeCtx, &pb.GetComplianceSummaryRequest{})
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
		} else {
			statuses[i].Healthy = true
			statuses[i].ThrallCount = r.value.(*pb.GetComplianceSummaryResponse).Total
		}
	}
	return statuses
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
