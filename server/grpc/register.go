// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Register handles the VigoAgent.Register RPC. It delegates to the enrollment
// registry, which validates the one-time token against
// approved patterns, inserts the envoy in the DB and FleetIndex, and returns
// the envoy ID. In a spanner, when another roster bolt owns the hostname's
// pattern partition the envoy is re-enrolled on that bolt and its address is
// returned to the agent (the peer-equal redirect, Slice 2-ii-b).
func (s *AgentService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	slog.Info("register request",
		"hostname", req.GetHostname(),
		"ip", req.GetIpAddress(),
	)

	envoyID, err := s.registry.Register(ctx,
		req.GetHostname(),
		req.GetIpAddress(),
		req.GetPubkeyPem(),
		req.GetToken(),
		req.GetForce(),
	)
	if err != nil {
		slog.Warn("registration failed",
			"hostname", req.GetHostname(),
			"error", err,
		)

		switch {
		case errors.Is(err, registry.ErrNoMatchingPattern):
			return &pb.RegisterResponse{
				Success: false,
				Message: "no matching approved pattern found",
			}, nil
		case errors.Is(err, registry.ErrHostnameRevoked):
			return nil, status.Error(codes.PermissionDenied,
				"hostname belongs to a revoked envoy and cannot re-enroll")
		case errors.Is(err, registry.ErrHostnameInUse):
			return &pb.RegisterResponse{
				Success: false,
				Message: "an active envoy with this hostname already exists; use --force to replace",
			}, nil
		default:
			return nil, status.Errorf(codes.Internal, "registration failed: %v", err)
		}
	}

	// Keep a handle to the locally-registered envoy ID so we can clean
	// up this server's FleetIndex entry if the enrollment is routed to
	// another bolt. envoyID may be reassigned to the owning bolt's ID
	// below.
	localEnvoyID := envoyID

	var boltAddr string

	// Peer-equal enrollment routing (Slice 2-ii-b): resolve which
	// roster bolt owns this hostname via OwningBolt (the ADR-002
	// pattern matcher over the gossiped admissions roster). When a
	// DIFFERENT bolt owns it, create the envoy record on that bolt via
	// a direct EnrollEnvoy admin RPC and return its address — the agent
	// persists bolt_addr and checks in there with the bolt-assigned
	// envoy ID (the redirect mechanism; no agent re-registration). When
	// this bolt owns the hostname (or no roster bolt claims it), the
	// local registration already done above stands.
	if s.spannerAdmin != nil {
		ownerPubkey, ownerHostname, owned := s.spannerAdmin.OwningBolt(ctx, req.GetHostname())
		if owned && !s.spannerAdmin.OwnsLocally(ownerPubkey) && ownerHostname != "" {
			boltEnvoyID, enrollErr := s.spannerAdmin.EnrollOnPeerBolt(ctx, ownerHostname, req.GetHostname(), req.GetIpAddress(), req.GetPubkeyPem(), req.GetForce())
			if enrollErr != nil {
				slog.Error("spanner bolt enrollment failed, envoy registered locally only",
					"hostname", req.GetHostname(),
					"owning_bolt", ownerHostname,
					"error", enrollErr,
				)
			} else {
				// The owning bolt created the envoy — redirect the agent
				// there with the bolt's envoy ID and gRPC address.
				envoyID = boltEnvoyID
				boltAddr = ownerHostname + ":" + s.spannerAdmin.GRPCPort()
				slog.Info("envoy routed to owning bolt (peer-equal)",
					"hostname", req.GetHostname(),
					"owning_bolt", ownerHostname,
					"bolt_addr", boltAddr,
					"envoy_id", envoyID,
				)
			}
		}
	}

	// registry.Register inserted the envoy locally (DB row + FleetIndex,
	// under the enrollment mutex). When the enrollment was routed to another
	// roster bolt, the owning bolt holds the envoy now, so drop both local
	// copies — removing only the FleetIndex entry left the DB row behind,
	// and rehydrate brought it back on restart as a phantom offline envoy.
	if boltAddr != "" {
		s.dropRoutedLocalEnvoy(ctx, localEnvoyID)
	}

	slog.Info("envoy registered",
		"hostname", req.GetHostname(),
		"envoy_id", envoyID,
	)

	// Record tamper-evident audit event.
	recordAudit(ctx, s.auditWriter, "envoy.enroll", "system", "grpc",
		req.GetHostname()+" enrolled as "+envoyID, "")

	// Dispatch envoy.enrolled event to integrations.
	if s.integrations != nil {
		s.integrations.Dispatch(integrations.Event{
			Name:      "envoy.enrolled",
			Timestamp: time.Now(),
			Hostname:  req.GetHostname(),
			EnvoyID:   envoyID,
			Summary:   fmt.Sprintf("%s enrolled as %s", req.GetHostname(), envoyID),
			Details:   map[string]any{"ip": req.GetIpAddress()},
		})
	}

	resp := &pb.RegisterResponse{
		Success:         true,
		EnvoyId:         envoyID,
		Message:         "registered successfully",
		BoltAddr:        boltAddr,
		FallbackServers: s.fallbackServers,
	}

	// Include server's bundle-signing public key for agent-side verification.
	if s.bundleSigner != nil {
		if pem, err := s.bundleSigner.PublicKeyPEM(); err == nil {
			resp.ServerPubkeyPem = pem
		}
	}

	return resp, nil
}

// dropRoutedLocalEnvoy removes this bolt's local record of an envoy whose
// enrollment was routed to the owning bolt: the DB row first (so a restart's
// rehydrate can't resurrect it), then the FleetIndex entry. A delete failure is
// logged, not returned — the agent has already been redirected and the
// enrollment itself succeeded on the owning bolt.
func (s *AgentService) dropRoutedLocalEnvoy(ctx context.Context, envoyID string) {
	if s.queries != nil {
		if err := s.queries.DeleteEnvoy(ctx, envoyID); err != nil {
			slog.Error("routed enrollment: failed to delete local envoy row; it will reappear as an offline envoy until deleted",
				"envoy_id", envoyID, "error", err)
		}
	}
	if s.fleetIndex != nil {
		s.fleetIndex.RemoveEnvoy(envoyID)
	}
}
