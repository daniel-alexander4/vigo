// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"log/slog"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetReassignment handles the HobnobAdmin.SetReassignment RPC. This is a
// hub→spoke internal RPC: the hub calls this on the SOURCE spoke to mark a
// thrall for reassignment. On the thrall's next check-in, the spoke returns
// the reassign_addr and reassign_id so the agent can reconnect to the new spoke.
func (s *AdminService) SetReassignment(ctx context.Context, req *pb.SetReassignmentRequest) (*pb.SetReassignmentResponse, error) {
	if req.ThrallId == "" || req.TargetAddr == "" || req.NewThrallId == "" {
		return nil, status.Error(codes.InvalidArgument, "thrall_id, target_addr, and new_thrall_id are required")
	}

	if s.reassignStore != nil {
		s.reassignStore.Set(req.ThrallId, ReassignInfo{
			TargetAddr:  req.TargetAddr,
			NewThrallID: req.NewThrallId,
		})
		slog.Info("reassignment set",
			"thrall_id", req.ThrallId,
			"target_addr", req.TargetAddr,
			"new_thrall_id", req.NewThrallId,
		)
	}

	return &pb.SetReassignmentResponse{}, nil
}

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
