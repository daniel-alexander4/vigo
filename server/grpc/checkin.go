// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckIn handles the VigoAgent.CheckIn RPC. It validates the client,
// stores inline traits for admin queries, and returns a policy bundle for
// agent-side evaluation. The server never renders templates or evaluates
// conditionals — the agent does that locally with its own traits.
func (s *AgentService) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()

	// Validate envoy is not revoked + look up state in one lock acquisition.
	var envoy db.Envoy
	if s.fleetIndex != nil {
		ts, exists, revoked := s.fleetIndex.GetEnvoyLite(envoyID)
		if !exists {
			metrics.CheckinsTotal.WithLabelValues("error").Inc()
			return nil, status.Error(codes.NotFound, "envoy not found")
		}
		if revoked {
			metrics.CheckinsTotal.WithLabelValues("error").Inc()
			return nil, status.Error(codes.PermissionDenied, "envoy is revoked")
		}
		envoy = fleetEnvoyToDB(ts)
	} else if err := s.registry.ValidateEnvoy(ctx, envoyID); err != nil {
		metrics.CheckinsTotal.WithLabelValues("error").Inc()
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

	// Log warning on every check-in if license is in grace or enrollment-block.
	if s.licenseEnforcer != nil {
		if warn := s.licenseEnforcer.CheckInWarning(); warn != "" {
			slog.Warn(warn, "envoy_id", envoyID)
		}
	}

	// Record liveness — the envoy just contacted us.
	if s.fleetIndex != nil {
		s.fleetIndex.MarkSeen(envoyID, time.Now())
	}

	// Record agent version if reported.
	if agentVersion := req.GetAgentVersion(); agentVersion != "" {
		if s.fleetIndex != nil {
			s.fleetIndex.SetAgentVersion(envoyID, agentVersion)
		}
		_ = s.queries.UpdateAgentVersion(ctx, db.UpdateAgentVersionParams{
			AgentVersion:   agentVersion,
			ID:             envoyID,
			AgentVersion_2: agentVersion,
		})
	}

	// Refresh envoy IP (agent reports its outbound IP each check-in).
	if ipAddr := req.GetIpAddress(); ipAddr != "" {
		if s.fleetIndex != nil {
			s.fleetIndex.SetIP(envoyID, ipAddr)
		}
		_ = s.queries.UpdateEnvoyIP(ctx, db.UpdateEnvoyIPParams{
			IpAddress:   ipAddr,
			ID:          envoyID,
			IpAddress_2: ipAddr,
		})
	}

	// Maintenance mode: return empty policy so envoy skips convergence
	// but still reports traits.
	if s.fleetIndex != nil && s.fleetIndex.IsInMaintenance(envoyID) {
		slog.Info("envoy in maintenance mode, returning empty policy", "envoy_id", envoyID)
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
