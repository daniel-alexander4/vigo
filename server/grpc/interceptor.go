package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/isogov/isogov/agent/auth"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/db"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// signatureTimestampWindow is the maximum age of a signed request before
// it is rejected for replay prevention.
const signatureTimestampWindow = 5 * time.Minute

// SignatureVerifyInterceptor returns a unary server interceptor that verifies
// ED25519 signatures on agent requests (SEC-03). The Register RPC is skipped
// because the agent does not have a UUID yet during enrollment.
func SignatureVerifyInterceptor(queries *db.Queries) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Skip signature verification for Register -- agent has no UUID yet.
		if info.FullMethod == pb.IsgovAgent_Register_FullMethodName {
			return handler(ctx, req)
		}

		// Only verify agent RPCs that carry signatures.
		switch r := req.(type) {
		case *pb.CheckInRequest:
			if err := verifyCheckIn(ctx, queries, r); err != nil {
				return nil, err
			}
		case *pb.ReportResultRequest:
			if err := verifyReportResult(ctx, queries, r); err != nil {
				return nil, err
			}
		case *pb.ReportFactsRequest:
			if err := verifyReportFacts(ctx, queries, r); err != nil {
				return nil, err
			}
		default:
			// Admin RPCs and unknown types pass through without agent signature checks.
		}

		return handler(ctx, req)
	}
}

// verifyCheckIn validates the signature on a CheckInRequest.
func verifyCheckIn(ctx context.Context, queries *db.Queries, req *pb.CheckInRequest) error {
	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
	}

	pubKey, err := lookupPubKey(ctx, queries, clientID)
	if err != nil {
		return err
	}

	ts := req.GetTimestamp()
	if ts == nil {
		return status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsTime := ts.AsTime()
	if err := checkTimestamp(tsTime); err != nil {
		return err
	}

	// The payload for CheckIn is the state fingerprint.
	payload := []byte(req.GetStateFingerprint())
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !auth.VerifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// verifyReportResult validates the signature on a ReportResultRequest.
func verifyReportResult(ctx context.Context, queries *db.Queries, req *pb.ReportResultRequest) error {
	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
	}

	pubKey, err := lookupPubKey(ctx, queries, clientID)
	if err != nil {
		return err
	}

	ts := req.GetTimestamp()
	if ts == nil {
		return status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsTime := ts.AsTime()
	if err := checkTimestamp(tsTime); err != nil {
		return err
	}

	payload := []byte(req.GetRunId())
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !auth.VerifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// verifyReportFacts validates the signature on a ReportFactsRequest.
func verifyReportFacts(ctx context.Context, queries *db.Queries, req *pb.ReportFactsRequest) error {
	clientID, err := uuid.Parse(req.GetClientId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid client_id: %v", err)
	}

	pubKey, err := lookupPubKey(ctx, queries, clientID)
	if err != nil {
		return err
	}

	ts := req.GetTimestamp()
	if ts == nil {
		return status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsTime := ts.AsTime()
	if err := checkTimestamp(tsTime); err != nil {
		return err
	}

	payload := req.GetFactsJson()
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !auth.VerifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// lookupPubKey retrieves the ED25519 public key for a client from the database.
func lookupPubKey(ctx context.Context, queries *db.Queries, clientID uuid.UUID) (ed25519.PublicKey, error) {
	client, err := queries.GetClientByID(ctx, clientID)
	if err != nil {
		slog.Error("client lookup failed", "client_id", clientID, "error", err)
		return nil, status.Error(codes.Unauthenticated, "client not found")
	}

	block, _ := pem.Decode([]byte(client.PubkeyPem))
	if block == nil {
		return nil, status.Error(codes.Internal, "invalid stored public key")
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "parsing stored public key: %v", err)
	}

	pubKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, status.Error(codes.Internal, "stored key is not ED25519")
	}

	return pubKey, nil
}

// checkTimestamp verifies the timestamp is within the allowed window.
func checkTimestamp(ts time.Time) error {
	diff := time.Since(ts)
	if math.Abs(float64(diff)) > float64(signatureTimestampWindow) {
		return status.Errorf(codes.Unauthenticated,
			"request timestamp too far from server time (diff: %v, window: %v)",
			diff, signatureTimestampWindow)
	}
	return nil
}
