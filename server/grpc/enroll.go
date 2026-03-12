// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"context"
	"log/slog"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnrollThrall handles the HobnobAdmin.EnrollThrall RPC. This is a hub→spoke
// internal RPC: the hub validates the enrollment token centrally, then calls
// this on the target spoke to create the thrall record without re-validation.
func (s *AdminService) EnrollThrall(ctx context.Context, req *pb.EnrollThrallRequest) (*pb.EnrollThrallResponse, error) {
	hostname := req.GetHostname()
	ipAddress := req.GetIpAddress()
	pubkeyPEM := req.GetPubkeyPem()

	if hostname == "" || pubkeyPEM == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname and pubkey_pem are required")
	}

	thrallID, err := s.registry.CreateThrall(ctx, hostname, ipAddress, pubkeyPEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating thrall: %v", err)
	}

	slog.Info("thrall enrolled via hub",
		"hostname", hostname,
		"node_id", thrallID,
	)

	return &pb.EnrollThrallResponse{
		ThrallId: thrallID,
	}, nil
}
