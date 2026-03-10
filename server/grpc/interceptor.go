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
	"github.com/hobnob/hobnob/agent/auth"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultSignatureWindow is used when no custom window is provided.
const defaultSignatureWindow = 5 * time.Minute

// SignatureVerifyInterceptor returns a unary server interceptor that verifies
// ED25519 signatures on agent requests (SEC-03). The Register RPC is skipped
// because the agent does not have a UUID yet during enrollment.
//
// When a NodeCache is provided, node lookups are served from cache,
// avoiding a DB round-trip on every request. The looked-up node is stored
// in the request context so downstream handlers can reuse it without another
// lookup.
//
// sigWindow configures the maximum age of a signed request before rejection
// for replay prevention. If zero, defaults to 5 minutes.
func SignatureVerifyInterceptor(queries *db.Queries, nodeCache *db.NodeCache, sigWindow time.Duration) grpc.UnaryServerInterceptor {
	if sigWindow == 0 {
		sigWindow = defaultSignatureWindow
	}
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Skip signature verification for Register -- agent has no UUID yet.
		if info.FullMethod == pb.HobnobAgent_Register_FullMethodName {
			return handler(ctx, req)
		}

		// Only verify agent RPCs that carry signatures.
		switch r := req.(type) {
		case *pb.CheckInRequest:
			newCtx, err := verifyCheckIn(ctx, queries, nodeCache, r, sigWindow)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.ReportResultRequest:
			newCtx, err := verifyReportResultCtx(ctx, queries, nodeCache, r, sigWindow)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.ReportTraitsRequest:
			if err := verifyReportTraits(ctx, queries, nodeCache, r, sigWindow); err != nil {
				return nil, err
			}
		default:
			// Admin RPCs and unknown types pass through without agent signature checks.
		}

		return handler(ctx, req)
	}
}

// verifyCheckIn validates the signature on a CheckInRequest. On success, it
// returns a context enriched with the looked-up node for handler reuse.
func verifyCheckIn(ctx context.Context, queries *db.Queries, cache *db.NodeCache, req *pb.CheckInRequest, sigWindow time.Duration) (context.Context, error) {
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}

	node, pubKey, err := lookupNodeAndPubKey(ctx, queries, cache, nodeID)
	if err != nil {
		return ctx, err
	}

	ts := req.GetTimestamp()
	if ts == nil {
		return ctx, status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsTime := ts.AsTime()
	if err := checkTimestamp(tsTime, sigWindow); err != nil {
		return ctx, err
	}

	// The payload for CheckIn is the state fingerprint.
	payload := []byte(req.GetStateFingerprint())
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !auth.VerifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		metrics.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	// Store node in context for handler reuse (eliminates duplicate lookups).
	return db.ContextWithNode(ctx, node), nil
}

// verifyReportResultCtx validates the signature on a ReportResultRequest and
// stores the node in context for handler reuse.
func verifyReportResultCtx(ctx context.Context, queries *db.Queries, cache *db.NodeCache, req *pb.ReportResultRequest, sigWindow time.Duration) (context.Context, error) {
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}

	node, pubKey, err := lookupNodeAndPubKey(ctx, queries, cache, nodeID)
	if err != nil {
		return ctx, err
	}

	ts := req.GetTimestamp()
	if ts == nil {
		return ctx, status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsTime := ts.AsTime()
	if err := checkTimestamp(tsTime, sigWindow); err != nil {
		return ctx, err
	}

	payload := []byte(req.GetRunId())
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !auth.VerifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		metrics.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return db.ContextWithNode(ctx, node), nil
}

// verifyReportTraits validates the signature on a ReportTraitsRequest.
func verifyReportTraits(ctx context.Context, queries *db.Queries, cache *db.NodeCache, req *pb.ReportTraitsRequest, sigWindow time.Duration) error {
	nodeID, err := uuid.Parse(req.GetNodeId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid node_id: %v", err)
	}

	_, pubKey, err := lookupNodeAndPubKey(ctx, queries, cache, nodeID)
	if err != nil {
		return err
	}

	ts := req.GetTimestamp()
	if ts == nil {
		return status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsTime := ts.AsTime()
	if err := checkTimestamp(tsTime, sigWindow); err != nil {
		return err
	}

	payload := req.GetTraitsJson()
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !auth.VerifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		metrics.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// lookupNodeAndPubKey retrieves a node and its parsed ED25519 public key.
// When a NodeCache is available, the parsed pubkey is served from cache,
// eliminating PEM decode + x509 parse on every RPC. Falls back to direct
// DB queries + on-the-fly parsing when no cache is configured.
func lookupNodeAndPubKey(ctx context.Context, queries *db.Queries, cache *db.NodeCache, nodeID uuid.UUID) (db.Node, ed25519.PublicKey, error) {
	// Fast path: cache returns both node and parsed pubkey.
	if cache != nil {
		node, pubKey, err := cache.GetByIDWithPubKey(ctx, nodeID)
		if err != nil {
			metrics.SigVerifyFailed.WithLabelValues("node_not_found").Inc()
			slog.Error("node lookup failed", "node_id", nodeID, "error", err)
			return db.Node{}, nil, status.Error(codes.Unauthenticated, "node not found")
		}
		return node, pubKey, nil
	}

	// Slow path: direct DB query + parse.
	node, err := queries.GetNodeByID(ctx, nodeID)
	if err != nil {
		metrics.SigVerifyFailed.WithLabelValues("node_not_found").Inc()
		slog.Error("node lookup failed", "node_id", nodeID, "error", err)
		return db.Node{}, nil, status.Error(codes.Unauthenticated, "node not found")
	}

	block, _ := pem.Decode([]byte(node.PubkeyPem))
	if block == nil {
		return db.Node{}, nil, status.Error(codes.Internal, "invalid stored public key")
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return db.Node{}, nil, status.Errorf(codes.Internal, "parsing stored public key: %v", err)
	}

	pubKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return db.Node{}, nil, status.Error(codes.Internal, "stored key is not ED25519")
	}

	return node, pubKey, nil
}

// checkTimestamp verifies the timestamp is within the allowed window.
func checkTimestamp(ts time.Time, window time.Duration) error {
	diff := time.Since(ts)
	if math.Abs(float64(diff)) > float64(window) {
		return status.Errorf(codes.Unauthenticated,
			"request timestamp too far from server time (diff: %v, window: %v) — ensure NTP is running on both server and agent (e.g. timedatectl set-ntp true)",
			diff, window)
	}
	return nil
}
