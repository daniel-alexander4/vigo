package grpc

import (
	"context"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DownloadCustomNail serves the raw bytes of a custom nail executable.
// Agents call this RPC to download new or updated nail executables
// identified by name from the CheckInResponse manifest.
func (s *AgentService) DownloadCustomNail(ctx context.Context, req *pb.DownloadCustomNailRequest) (*pb.DownloadCustomNailResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	data, err := s.nailsScanner.ReadFile(req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "nail %q not found: %v", req.GetName(), err)
	}

	return &pb.DownloadCustomNailResponse{
		Data: data,
	}, nil
}
