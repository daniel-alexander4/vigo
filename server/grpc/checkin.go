// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckIn handles the HobnobAgent.CheckIn RPC. It validates the client,
// stores inline traits for admin queries, and returns a policy bundle for
// agent-side evaluation. The server never renders templates or evaluates
// conditionals — the agent does that locally with its own traits.
func (s *AgentService) CheckIn(ctx context.Context, req *pb.CheckInRequest) (*pb.CheckInResponse, error) {
	if _, err := uuid.Parse(req.GetThrallId()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid thrall_id: %v", err)
	}
	thrallID := req.GetThrallId()

	// Validate thrall is not revoked + look up state in one lock acquisition.
	var thrall db.Thrall
	if s.fleetIndex != nil {
		ts, exists, revoked := s.fleetIndex.GetThrallLite(thrallID)
		if !exists {
			metrics.CheckinsTotal.WithLabelValues("error").Inc()
			return nil, status.Error(codes.NotFound, "thrall not found")
		}
		if revoked {
			metrics.CheckinsTotal.WithLabelValues("error").Inc()
			return nil, status.Error(codes.PermissionDenied, "thrall is revoked")
		}
		thrall = fleetThrallToDB(ts)
	} else if err := s.registry.ValidateThrall(ctx, thrallID); err != nil {
		metrics.CheckinsTotal.WithLabelValues("error").Inc()
		if errors.Is(err, registry.ErrThrallRevoked) {
			return nil, status.Error(codes.PermissionDenied, "thrall is revoked")
		}
		if errors.Is(err, registry.ErrThrallNotFound) {
			return nil, status.Error(codes.NotFound, "thrall not found")
		}
		return nil, status.Errorf(codes.Internal, "validating thrall: %v", err)
	} else {
		var err error
		thrall, err = s.lookupThrall(ctx, thrallID)
		if err != nil {
			return nil, err
		}
	}

	// Log warning on every check-in if license is in grace or enrollment-block.
	if s.licenseEnforcer != nil {
		if warn := s.licenseEnforcer.CheckInWarning(); warn != "" {
			slog.Warn(warn, "thrall_id", thrallID)
		}
	}

	// Maintenance mode: return empty policy so thrall skips convergence
	// but still reports traits.
	if s.fleetIndex != nil && s.fleetIndex.IsInMaintenance(thrallID) {
		slog.Info("thrall in maintenance mode, returning empty policy", "thrall_id", thrallID)
		return &pb.CheckInResponse{}, nil
	}

	return s.buildCheckInResponse(ctx, thrallID, thrall, req, false)
}

// lookupThrall resolves a thrall from FleetIndex, context, cache, or DB.
func (s *AgentService) lookupThrall(ctx context.Context, thrallID string) (db.Thrall, error) {
	// Fastest: FleetIndex in-memory lookup (no Traits copy needed).
	if s.fleetIndex != nil {
		ts, ok, _ := s.fleetIndex.GetThrallLite(thrallID)
		if ok {
			return fleetThrallToDB(ts), nil
		}
	}
	if n := db.ThrallFromContext(ctx); n != nil {
		return *n, nil
	}
	n, err := s.queries.GetThrallByID(ctx, thrallID)
	if err != nil {
		return db.Thrall{}, status.Errorf(codes.Internal, "looking up thrall: %v", err)
	}
	return n, nil
}
