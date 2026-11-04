// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReportResult handles the VigoAgent.ReportResult RPC. It records the run
// (row + per-resource results + file snapshots, atomically) before acking, via
// the ingest path it shares with the stream delta handler (ingestRun).
func (s *AgentService) ReportResult(ctx context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	// Admission control: shed before doing any work so a result storm
	// (e.g. fleet-wide reporting after a publish) can't pile unbounded
	// writes onto the single SQLite writer. ResourceExhausted tells the
	// agent to back off — it already does.
	if !s.tryAcquireReportSlot() {
		return nil, status.Errorf(codes.ResourceExhausted, "server overloaded (report admission cap %d reached); retry with backoff", cap(s.reportResultSem))
	}
	defer s.releaseReportSlot()

	envoyID := req.GetEnvoyId()
	var traceID string
	ctx, traceID = EnsureTraceID(ctx, req.GetTraceparent())

	// Reuse envoy from interceptor context (avoids redundant DB lookup).
	hostname := envoyID // fallback
	if n := db.EnvoyFromContext(ctx); n != nil {
		hostname = n.Hostname
	}

	if err := s.ingestRun(ctx, runReport{
		envoyID:    envoyID,
		hostname:   hostname,
		results:    req.GetResults(),
		totalCount: req.GetTotalCount(),
		traceID:    traceID,
	}, true); err != nil {
		return nil, status.Errorf(codes.Internal, "recording run: %v", err)
	}
	return &pb.ReportResultResponse{Acknowledged: true}, nil
}
