// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
	"github.com/hobnob/hobnob/server/registry"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/webhook"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Register handles the HobnobAgent.Register RPC. It delegates to the enrollment
// registry to validate the one-time token, match approved patterns, and create
// the client record. In hub mode, after token validation, the thrall is enrolled
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

	thrallID, err := s.registry.Register(ctx,
		req.GetHostname(),
		req.GetIpAddress(),
		req.GetPubkeyPem(),
		req.GetToken(),
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
				"hostname belongs to a revoked thrall and cannot re-enroll")
		default:
			return nil, status.Errorf(codes.Internal, "registration failed: %v", err)
		}
	}

	var spokeAddr string

	// Hub mode: route the enrollment to the target spoke.
	if s.hub != nil {
		addr, matched := s.hub.RouteHostname(req.GetHostname())
		if matched {
			spokeThrallID, enrollErr := s.hub.EnrollOnSpoke(ctx, addr, req.GetHostname(), req.GetIpAddress(), req.GetPubkeyPem())
			if enrollErr != nil {
				slog.Error("spoke enrollment failed, thrall registered on hub only",
					"hostname", req.GetHostname(),
					"spoke_addr", addr,
					"error", enrollErr,
				)
			} else {
				// Spoke created the thrall — use the spoke's thrall ID and address.
				thrallID = spokeThrallID
				spokeAddr = addr
				slog.Info("thrall enrolled on spoke",
					"hostname", req.GetHostname(),
					"spoke_addr", addr,
					"thrall_id", thrallID,
				)
			}
		}
	}

	// Insert into FleetIndex for immediate in-memory availability.
	if s.fleetIndex != nil && spokeAddr == "" {
		// Only index locally when not routed to a spoke.
		pubKey, _ := db.ParsePubKey(req.GetPubkeyPem())
		s.fleetIndex.AddThrall(fleet.ThrallState{
			ID:               thrallID,
			Hostname:         req.GetHostname(),
			IP:               req.GetIpAddress(),
			PubkeyPEM:        req.GetPubkeyPem(),
			EnrolledAt:       time.Now(),
			ComplianceStatus: "unknown",
		}, pubKey)
	}

	slog.Info("thrall registered",
		"hostname", req.GetHostname(),
		"node_id", thrallID,
	)

	// Dispatch thrall.enrolled webhook event.
	if s.dispatcher != nil {
		s.dispatcher.Dispatch(ctx, "thrall.enrolled", webhook.EventPayload{
			Thrall: &webhook.ThrallInfo{
				ID:       thrallID,
				Hostname: req.GetHostname(),
				IP:       req.GetIpAddress(),
			},
		})
	}

	s.smtpNotifier.Notify(hobnobsmtp.Event{
		Name: "thrall.enrolled", Timestamp: time.Now().UTC(),
		Thrall: &hobnobsmtp.ThrallInfo{ID: thrallID, Hostname: req.GetHostname(), IP: req.GetIpAddress()},
	})

	resp := &pb.RegisterResponse{
		Success:   true,
		ThrallId:    thrallID,
		Message:   "registered successfully",
		SpokeAddr: spokeAddr,
	}

	// Include server's bundle-signing public key for agent-side verification.
	if s.bundleSigner != nil {
		if pem, err := s.bundleSigner.PublicKeyPEM(); err == nil {
			resp.ServerPubkeyPem = pem
		}
	}

	return resp, nil
}
