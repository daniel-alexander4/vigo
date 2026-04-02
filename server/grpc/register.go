// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/registry"
	vigosmtp "github.com/daniel-alexander4/vigo/server/smtp"
	"github.com/daniel-alexander4/vigo/server/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Register handles the VigoAgent.Register RPC. It delegates to the enrollment
// registry to validate the one-time token, match approved patterns, and create
// the client record. In hub mode, after token validation, the envoy is enrolled
// on the target spoke and the spoke address is returned to the agent.
func (s *AgentService) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	slog.Info("register request",
		"hostname", req.GetHostname(),
		"ip", req.GetIpAddress(),
	)

	// License enforcement: block new registrations at Stage 2+.
	if s.licenseEnforcer != nil {
		if err := s.licenseEnforcer.RegistrationAllowed(); err != nil {
			slog.Warn("registration blocked by license enforcement",
				"hostname", req.GetHostname(),
				"error", err,
			)
			return nil, status.Error(codes.PermissionDenied, err.Error())
		}
	}

	// If force-replacing, revoke the old envoy in FleetIndex first.
	if req.GetForce() && s.fleetIndex != nil {
		if oldID := s.fleetIndex.GetByHostname(req.GetHostname()); oldID != "" {
			s.fleetIndex.SetRevoked(oldID, true)
		}
	}

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

	var spokeAddr string

	// Hub mode: route the enrollment to the target spoke.
	if s.hub != nil {
		addr, matched := s.hub.RouteHostname(req.GetHostname())
		if matched {
			spokeEnvoyID, enrollErr := s.hub.EnrollOnSpoke(ctx, addr, req.GetHostname(), req.GetIpAddress(), req.GetPubkeyPem(), req.GetForce())
			if enrollErr != nil {
				slog.Error("spoke enrollment failed, envoy registered on hub only",
					"hostname", req.GetHostname(),
					"spoke_addr", addr,
					"error", enrollErr,
				)
			} else {
				// Spoke created the envoy — use the spoke's envoy ID and address.
				envoyID = spokeEnvoyID
				spokeAddr = addr
				slog.Info("envoy enrolled on spoke",
					"hostname", req.GetHostname(),
					"spoke_addr", addr,
					"envoy_id", envoyID,
				)
			}
		}
	}

	// Insert into FleetIndex for immediate in-memory availability.
	if s.fleetIndex != nil && spokeAddr == "" {
		// Only index locally when not routed to a spoke.
		pubKey, _ := db.ParsePubKey(req.GetPubkeyPem())
		s.fleetIndex.AddEnvoy(fleet.EnvoyState{
			ID:               envoyID,
			Hostname:         req.GetHostname(),
			IP:               req.GetIpAddress(),
			PubkeyPEM:        req.GetPubkeyPem(),
			EnrolledAt:       time.Now(),
			ConvergenceStatus: "no data",
		}, pubKey)
	}

	slog.Info("envoy registered",
		"hostname", req.GetHostname(),
		"node_id", envoyID,
	)

	// Record tamper-evident audit event.
	recordAudit(ctx, s.auditWriter, "envoy.enroll", "system", "grpc",
		req.GetHostname()+" enrolled as "+envoyID, "")

	// Dispatch envoy.enrolled webhook event.
	if s.dispatcher != nil {
		s.dispatcher.Dispatch(ctx, "envoy.enrolled", webhook.EventPayload{
			Envoy: &webhook.EnvoyInfo{
				ID:       envoyID,
				Hostname: req.GetHostname(),
				IP:       req.GetIpAddress(),
			},
		})
	}

	s.smtpNotifier.Notify(vigosmtp.Event{
		Name: "envoy.enrolled", Timestamp: time.Now().UTC(),
		Envoy: &vigosmtp.EnvoyInfo{ID: envoyID, Hostname: req.GetHostname(), IP: req.GetIpAddress()},
	})

	resp := &pb.RegisterResponse{
		Success:         true,
		EnvoyId:        envoyID,
		Message:         "registered successfully",
		SpokeAddr:       spokeAddr,
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
