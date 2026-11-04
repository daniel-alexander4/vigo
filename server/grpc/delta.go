// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
)

// handleDelta processes a DeltaEvent from the agent stream and returns an ack.
func (s *AgentService) handleDelta(envoyID, hostname string, delta *pb.DeltaEvent) *pb.DeltaAck {
	switch ev := delta.Event.(type) {
	case *pb.DeltaEvent_Run:
		s.handleRunDelta(envoyID, hostname, ev.Run)
	case *pb.DeltaEvent_Heartbeat:
		// Heartbeats only occur within streams — update in-memory only to avoid
		// redundant last_seen DB writes. The stream disconnect handler persists
		// the final timestamp.
		if s.fleetIndex != nil {
			s.fleetIndex.MarkSeenInMemory(envoyID, time.Now())
		}
	}
	return &pb.DeltaAck{AckedSequence: delta.Sequence}
}

// handleRunDelta processes a RunCompleted delta through the ingest path it
// shares with the unary ReportResult (ingestRun); the write is queued on the
// async RunBatcher so the stream acks without waiting on the DB.
func (s *AgentService) handleRunDelta(envoyID, hostname string, run *pb.RunCompleted) {
	_, traceID := ParseOrGenerate(run.GetTraceparent())
	// The async path never returns an error: there is no write to fail here.
	_ = s.ingestRun(context.Background(), runReport{
		envoyID:       envoyID,
		hostname:      hostname,
		results:       run.Results,
		totalCount:    run.TotalCount,
		traceID:       traceID,
		bundleVersion: run.BundleVersion,
		label:         " (delta)",
	}, false)
}
