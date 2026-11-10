// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"log/slog"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetReassignment handles the VigoAdmin.SetReassignment RPC, a bolt-to-bolt
// spanner RPC: the peer bolt driving a reassignment (SpannerAdmin.Reassign)
// calls it on the SOURCE bolt to mark an envoy for reassignment. On the
// envoy's next check-in, the source bolt returns the reassign_addr and
// reassign_id so the agent reconnects to the target bolt.
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

// EnrollEnvoy handles the VigoAdmin.EnrollEnvoy RPC, a bolt-to-bolt spanner
// RPC: the bolt that received an enrollment (gRPC Register or REST bootstrap)
// has already validated the token, and calls this on the roster bolt that owns
// the hostname's pattern partition to create the envoy record there without
// re-validating the token. cert_spki_sha256, when set, is stored as the
// envoy's cert pin so a routed envoy is pinned like a local one.
func (s *AdminService) EnrollEnvoy(ctx context.Context, req *pb.EnrollEnvoyRequest) (*pb.EnrollEnvoyResponse, error) {
	hostname := req.GetHostname()
	ipAddress := req.GetIpAddress()
	pubkeyPEM := req.GetPubkeyPem()
	spki := req.GetCertSpkiSha256()

	if hostname == "" || pubkeyPEM == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname and pubkey_pem are required")
	}
	if spki != "" && !isSPKIPin(spki) {
		return nil, status.Error(codes.InvalidArgument, "cert_spki_sha256 must be 64 lowercase hex characters")
	}

	envoyID, err := s.registry.CreateEnvoy(ctx, hostname, ipAddress, pubkeyPEM, req.GetForce())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "creating envoy: %v", err)
	}
	if spki != "" {
		if err := s.registry.SetEnvoyCertPin(ctx, envoyID, spki); err != nil {
			slog.Error("enroll envoy: could not store cert pin; envoy will be refused at check-in until re-enrolled",
				"hostname", hostname, "envoy_id", envoyID, "error", err)
		}
	}

	slog.Info("envoy enrolled via peer bolt",
		"hostname", hostname,
		"envoy_id", envoyID,
		"pinned", spki != "",
	)

	return &pb.EnrollEnvoyResponse{
		EnvoyId: envoyID,
	}, nil
}

// isSPKIPin reports whether s has the cert-pin shape peerCertSPKI produces:
// 64 lowercase hex characters (a hex-encoded SHA-256).
func isSPKIPin(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
