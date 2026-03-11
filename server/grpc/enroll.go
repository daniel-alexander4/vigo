package grpc

import (
	"context"
	"log/slog"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnrollNode handles the HobnobAdmin.EnrollNode RPC. This is a hub→spoke
// internal RPC: the hub validates the enrollment token centrally, then calls
// this on the target spoke to create the node record without re-validation.
func (s *AdminService) EnrollNode(ctx context.Context, req *pb.EnrollNodeRequest) (*pb.EnrollNodeResponse, error) {
	hostname := req.GetHostname()
	ipAddress := req.GetIpAddress()
	pubkeyPEM := req.GetPubkeyPem()

	if hostname == "" || pubkeyPEM == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname and pubkey_pem are required")
	}

	nodeID, err := s.registry.CreateNode(ctx, hostname, ipAddress, pubkeyPEM)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating node: %v", err)
	}

	slog.Info("node enrolled via hub",
		"hostname", hostname,
		"node_id", nodeID,
	)

	return &pb.EnrollNodeResponse{
		NodeId: nodeID,
	}, nil
}
