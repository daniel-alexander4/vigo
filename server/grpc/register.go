// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Register handles the VigoAgent.Register RPC. It delegates to the enrollment
// registry, which runs the license gate, validates the one-time token against
// approved patterns, inserts the envoy in the DB and FleetIndex, and returns
// the envoy ID. In hub mode, after the hub's local registration succeeds, the
// envoy is re-enrolled on the target bolt and the bolt address is returned
// to the agent.
//
// All license enforcement lives inside registry.Register — this handler does
// not call licenseEnforcer.RegistrationAllowedAt directly. That way the gate
// check, DB insert, and FleetIndex mutation happen under the registry's
// serialization mutex, eliminating the TOCTOU race where two concurrent
// registrations at the node limit could both pass the old separate gate call.
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
		case errors.Is(err, license.ErrNodeLimitReached):
			return nil, status.Error(codes.PermissionDenied, err.Error())
		case errors.Is(err, license.ErrEnrollmentBlocked), errors.Is(err, license.ErrServiceStopping):
			return nil, status.Error(codes.PermissionDenied, err.Error())
		case errors.Is(err, license.ErrCountUnavailable):
			return nil, status.Error(codes.PermissionDenied, err.Error())
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

	// Keep a handle to the hub-local envoy ID so we can clean up the hub's
	// FleetIndex entry if the enrollment is routed to a bolt. envoyID may
	// be reassigned to the bolt's ID below.
	hubLocalEnvoyID := envoyID

	var boltAddr string

	// Enrollment routing. Two additive branches; the peer-equal path
	// (Slice 2-ii-b) is the effective one — the hub's RouteHostname is
	// degraded (returns no-route) under ADR-026, so the hub branch
	// no-ops on every hostname until Slice 2-ii-c retires the hub.
	//
	// Hub mode (legacy; retired by Slice 2-ii-c): route to the target
	// bolt the hub's pattern table picks.
	if s.hub != nil {
		addr, matched := s.hub.RouteHostname(req.GetHostname())
		if matched {
			boltEnvoyID, enrollErr := s.hub.EnrollOnBolt(ctx, addr, req.GetHostname(), req.GetIpAddress(), req.GetPubkeyPem(), req.GetForce())
			if enrollErr != nil {
				slog.Error("bolt enrollment failed, envoy registered on hub only",
					"hostname", req.GetHostname(),
					"bolt_addr", addr,
					"error", enrollErr,
				)
			} else {
				// Bolt created the envoy — use the bolt's envoy ID and address.
				envoyID = boltEnvoyID
				boltAddr = addr
				slog.Info("envoy enrolled on bolt",
					"hostname", req.GetHostname(),
					"bolt_addr", addr,
					"envoy_id", envoyID,
				)
			}
		}
	}

	// Peer-equal mode (Slice 2-ii-b): resolve which roster bolt owns
	// this hostname via OwningBolt (the ADR-002 pattern matcher over
	// the gossiped admissions roster). When a DIFFERENT bolt owns it,
	// create the envoy record on that bolt via a direct EnrollEnvoy
	// admin RPC and return its address — the agent persists bolt_addr
	// and checks in there with the bolt-assigned envoy ID (the redirect
	// mechanism; no agent re-registration). When this bolt owns the
	// hostname (or no roster bolt claims it), the local registration
	// already done above stands.
	if boltAddr == "" && s.spannerAdmin != nil {
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

	// FleetIndex insertion is now handled inside registry.Register under the
	// enrollment mutex, so we don't call AddEnvoy here. In hub mode where the
	// enrollment was routed to a bolt, the hub's local FleetIndex entry is
	// redundant — the bolt now owns this envoy — so remove it to keep the
	// hub-local active count accurate.
	if s.fleetIndex != nil && boltAddr != "" {
		s.fleetIndex.RemoveEnvoy(hubLocalEnvoyID)
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
