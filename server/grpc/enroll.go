// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"log/slog"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetReassignment handles the VigoAdmin.SetReassignment RPC. This is a
// hub→bolt internal RPC: the hub calls this on the SOURCE bolt to mark
// an envoy for reassignment. On the envoy's next check-in, the bolt returns
// the reassign_addr and reassign_id so the agent can reconnect to the new bolt.
func (s *AdminService) SetReassignment(ctx context.Context, req *pb.SetReassignmentRequest) (*pb.SetReassignmentResponse, error) {
	if req.EnvoyId == "" || req.TargetAddr == "" || req.NewEnvoyId == "" {
		return nil, status.Error(codes.InvalidArgument, "envoy_id, target_addr, and new_envoy_id are required")
	}

	if s.reassignStore != nil {
		s.reassignStore.Set(req.EnvoyId, ReassignInfo{
			TargetAddr: req.TargetAddr,
			NewEnvoyID: req.NewEnvoyId,
		})
		slog.Info("reassignment set",
			"envoy_id", req.EnvoyId,
			"target_addr", req.TargetAddr,
			"new_envoy_id", req.NewEnvoyId,
		)
	}

	return &pb.SetReassignmentResponse{}, nil
}

// EnrollEnvoy handles the VigoAdmin.EnrollEnvoy RPC. This is a hub→bolt
// internal RPC: the hub validates the enrollment token centrally, then calls
// this on the target bolt to create the envoy record without re-validating
// the token.
func (s *AdminService) EnrollEnvoy(ctx context.Context, req *pb.EnrollEnvoyRequest) (*pb.EnrollEnvoyResponse, error) {
	hostname := req.GetHostname()
	ipAddress := req.GetIpAddress()
	pubkeyPEM := req.GetPubkeyPem()

	if hostname == "" || pubkeyPEM == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname and pubkey_pem are required")
	}

	envoyID, err := s.registry.CreateEnvoy(ctx, hostname, ipAddress, pubkeyPEM, req.GetForce())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating envoy: %v", err)
	}

	slog.Info("envoy enrolled via hub",
		"hostname", hostname,
		"envoy_id", envoyID,
	)

	return &pb.EnrollEnvoyResponse{
		EnvoyId: envoyID,
	}, nil
}
