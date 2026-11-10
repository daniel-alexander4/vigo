// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DownloadCustomTrait serves the raw bytes of a custom trait executable.
// Agents call this RPC to download new or updated trait executables
// identified by name from the CheckInResponse manifest.
func (s *AgentService) DownloadCustomTrait(ctx context.Context, req *pb.DownloadCustomTraitRequest) (*pb.DownloadCustomTraitResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	data, err := s.traitsScanner.ReadFile(req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "trait %q not found: %v", req.GetName(), err)
	}

	return &pb.DownloadCustomTraitResponse{
		Data: data,
	}, nil
}
