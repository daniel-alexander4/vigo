// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/freshness"
	"git.alexander4.io/vigo/server/registry"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clampReportedCycle bounds the agent-reported inter-CheckIn delta to
// freshness.MaxReportedCycle. Defends against a compromised or buggy
// agent inflating the value to hide itself from offline detection.
// Operators with genuinely longer cycles should raise checkin.interval
// to match.
func clampReportedCycle(deltaMs uint32) uint32 {
	maxMs := uint32(freshness.MaxReportedCycle / time.Millisecond)
	if deltaMs > maxMs {
		return maxMs
	}
	return deltaMs
}

// CheckIn handles the VigoAgent.CheckIn RPC. It validates the client,
// stores inline traits for admin queries, and returns a policy bundle for
// agent-side evaluation. The server never renders templates or evaluates
// conditionals — the agent does that locally with its own traits.
func (s *AgentService) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	s.metrics.GRPCCheckInInFlight.Inc()
	defer s.metrics.GRPCCheckInInFlight.Dec()

	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()
	var traceID string
	ctx, traceID = EnsureTraceID(ctx, req.GetTraceparent())

	// Validate envoy is not revoked + look up state in one lock acquisition.
	var envoy db.Envoy
	if s.fleetIndex != nil {
		ts, exists, revoked := s.fleetIndex.GetEnvoyLite(envoyID)
		if !exists {
			s.metrics.CheckinsTotal.WithLabelValues("error").Inc()
			return nil, status.Error(codes.NotFound, "envoy not found")
		}
		if revoked {
			s.metrics.CheckinsTotal.WithLabelValues("error").Inc()
			return nil, status.Error(codes.PermissionDenied, "envoy is revoked")
		}
		envoy = fleetEnvoyToDB(ts)
	} else if err := s.registry.ValidateEnvoy(ctx, envoyID); err != nil {
		s.metrics.CheckinsTotal.WithLabelValues("error").Inc()
		if errors.Is(err, registry.ErrEnvoyRevoked) {
			return nil, status.Error(codes.PermissionDenied, "envoy is revoked")
		}
		if errors.Is(err, registry.ErrEnvoyNotFound) {
			return nil, status.Error(codes.NotFound, "envoy not found")
		}
		return nil, status.Errorf(codes.Internal, "validating envoy: %v", err)
	} else {
		var err error
		envoy, err = s.lookupEnvoy(ctx, envoyID)
		if err != nil {
			return nil, err
		}
	}

	// Record liveness — the envoy just contacted us.
	if s.fleetIndex != nil {
		s.fleetIndex.MarkSeen(envoyID, time.Now())
		s.fleetIndex.MarkCycleDuration(envoyID, clampReportedCycle(req.GetLastCheckinDeltaMs()))
	}

	// Record agent version if reported. In-memory only; the flusher's
	// value-grouped BatchUpdateAgentVersion path persists it. The inline
	// DB UPDATE was retired at 0.67.28 — at 10K-envoy stagger-up it was
	// 10000 single-row UPDATEs serialized through the WAL writer mutex,
	// dominating batch_last_seen wall time. Value-grouped batched
	// persistence collapses that to ~chunkCount statements per distinct
	// version (typically 1-3 across a real fleet).
	if agentVersion := req.GetAgentVersion(); agentVersion != "" && s.fleetIndex != nil {
		s.fleetIndex.SetAgentVersion(envoyID, agentVersion)
	}

	// Refresh envoy IP (agent reports its outbound IP each check-in).
	// Same change-gated path as SetAgentVersion above.
	if ipAddr := req.GetIpAddress(); ipAddr != "" {
		changed := true
		if s.fleetIndex != nil {
			changed = s.fleetIndex.SetIP(envoyID, ipAddr)
		}
		if changed {
			if err := s.queries.UpdateEnvoyIP(ctx, db.UpdateEnvoyIPParams{
				IpAddress:   ipAddr,
				ID:          envoyID,
				IpAddress_2: ipAddr,
			}); err != nil {
				slog.Warn("checkin db write failed",
					"envoy_id", envoyID,
					"op", "update_envoy_ip",
					"error", err,
				)
				s.metrics.AgentStreamDBErrors.WithLabelValues("update_envoy_ip").Inc()
			}
		}
	}

	// Maintenance mode: return empty policy so envoy skips convergence
	// but still reports traits.
	if s.fleetIndex != nil && s.fleetIndex.IsInMaintenance(envoyID) {
		slog.Info("envoy in maintenance mode, returning empty policy", "envoy_id", envoyID, "trace_id", traceID)
		return &pb.CheckInResponse{}, nil
	}

	return s.buildCheckInResponse(ctx, envoyID, envoy, req, false)
}

// lookupEnvoy resolves a envoy from FleetIndex, context, cache, or DB.
func (s *AgentService) lookupEnvoy(ctx context.Context, envoyID string) (db.Envoy, error) {
	// Fastest: FleetIndex in-memory lookup (no Traits copy needed).
	if s.fleetIndex != nil {
		ts, ok, _ := s.fleetIndex.GetEnvoyLite(envoyID)
		if ok {
			return fleetEnvoyToDB(ts), nil
		}
	}
	if n := db.EnvoyFromContext(ctx); n != nil {
		return *n, nil
	}
	n, err := s.queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		return db.Envoy{}, status.Errorf(codes.Internal, "looking up envoy: %v", err)
	}
	return n, nil
}
