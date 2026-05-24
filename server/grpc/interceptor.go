// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
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
// The looked-up envoy is stored in the request context so downstream handlers
// can reuse it without another lookup.
//
// sigWindow configures the maximum age of a signed request before rejection
// for replay prevention. If zero, defaults to 5 minutes. m owns the gRPC
// request/sig-verify counters this interceptor mutates.
func SignatureVerifyInterceptor(queries *db.Queries, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) grpc.UnaryServerInterceptor {
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
		if info.FullMethod == pb.VigoAgent_Register_FullMethodName {
			return handler(ctx, req)
		}

		// Only verify agent RPCs that carry signatures.
		switch r := req.(type) {
		case *pb.CheckInRequest:
			newCtx, err := verifyCheckIn(ctx, queries, r, sigWindow, fleetIdx, m)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.ReportResultRequest:
			newCtx, err := verifyReportResultCtx(ctx, queries, r, sigWindow, fleetIdx, m)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.ReportTraitsRequest:
			if err := verifyReportTraits(ctx, queries, r, sigWindow, fleetIdx, m); err != nil {
				return nil, err
			}
		default:
			// Admin RPCs and unknown types pass through without agent signature checks.
		}

		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		m.GRPCRequestsTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
		m.GRPCRequestDuration.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

// verifyCheckIn validates the signature on a CheckInRequest. On success, it
// returns a context enriched with the looked-up envoy for handler reuse.
func verifyCheckIn(ctx context.Context, queries *db.Queries, req *pb.CheckInRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (context.Context, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()

	envoy, pubKey, err := lookupEnvoyAndPubKey(ctx, queries, envoyID, fleetIdx, m)
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

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		m.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	// Store envoy in context for handler reuse (eliminates duplicate lookups).
	return db.ContextWithEnvoy(ctx, envoy), nil
}

// verifyReportResultCtx validates the signature on a ReportResultRequest and
// stores the envoy in context for handler reuse.
func verifyReportResultCtx(ctx context.Context, queries *db.Queries, req *pb.ReportResultRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (context.Context, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()

	envoy, pubKey, err := lookupEnvoyAndPubKey(ctx, queries, envoyID, fleetIdx, m)
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

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		m.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return db.ContextWithEnvoy(ctx, envoy), nil
}

// verifyReportTraits validates the signature on a ReportTraitsRequest.
func verifyReportTraits(ctx context.Context, queries *db.Queries, req *pb.ReportTraitsRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) error {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()

	_, pubKey, err := lookupEnvoyAndPubKey(ctx, queries, envoyID, fleetIdx, m)
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

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey) {
		m.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// lookupEnvoyAndPubKey retrieves a envoy and its parsed ED25519 public key.
// When a FleetIndex is available, the parsed pubkey is served from memory (O(1),
// no PEM parse). Falls back to direct DB query.
func lookupEnvoyAndPubKey(ctx context.Context, queries *db.Queries, envoyID string, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (db.Envoy, ed25519.PublicKey, error) {
	// Fast path: FleetIndex returns parsed pubkey from memory.
	if fleetIdx != nil {
		pubKey := fleetIdx.GetPubKey(envoyID)
		if pubKey != nil {
			ts, ok, _ := fleetIdx.GetEnvoyLite(envoyID)
			if ok {
				envoy := fleetEnvoyToDB(ts)
				return envoy, pubKey, nil
			}
		}
		// Fall through to DB if not in FleetIndex.
	}

	// Slow path: direct DB query + parse.
	envoy, err := queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		m.SigVerifyFailed.WithLabelValues("envoy_not_found").Inc()
		slog.Error("envoy lookup failed", "envoy_id", envoyID, "error", err)
		return db.Envoy{}, nil, status.Error(codes.Unauthenticated, "envoy not found")
	}

	block, _ := pem.Decode([]byte(envoy.PubkeyPem))
	if block == nil {
		return db.Envoy{}, nil, status.Error(codes.Internal, "invalid stored public key")
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return db.Envoy{}, nil, status.Errorf(codes.Internal, "parsing stored public key: %v", err)
	}

	pubKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return db.Envoy{}, nil, status.Error(codes.Internal, "stored key is not ED25519")
	}

	return envoy, pubKey, nil
}

// fleetEnvoyToDB converts a FleetIndex EnvoyState to a db.Envoy for
// compatibility with code that expects the DB model.
func fleetEnvoyToDB(ts *fleet.EnvoyState) db.Envoy {
	t := db.Envoy{
		ID:        ts.ID,
		Hostname:  ts.Hostname,
		IpAddress: ts.IP,
		PubkeyPem: ts.PubkeyPEM,
		EnrolledAt: ts.EnrolledAt,
	}
	if !ts.LastSeen.IsZero() {
		t.LastSeen.Time = ts.LastSeen
		t.LastSeen.Valid = true
	}
	if ts.Revoked {
		t.Revoked = 1
	}
	if ts.ForcePush {
		t.ForcePush = 1
	}
	if len(ts.PendingRotatedSecrets) > 0 {
		b, _ := json.Marshal(ts.PendingRotatedSecrets)
		t.PendingRotatedSecrets = string(b)
	}
	return t
}

// verifyPayload verifies an ED25519 signature over payload+timestamp.
func verifyPayload(payload []byte, timestamp string, sig []byte, pub ed25519.PublicKey) bool {
	msg := make([]byte, len(payload)+len(timestamp))
	copy(msg, payload)
	copy(msg[len(payload):], timestamp)
	return ed25519.Verify(pub, msg, sig)
}

// checkTimestamp verifies the timestamp is within the allowed window.
func checkTimestamp(ts time.Time, window time.Duration) error {
	diff := time.Since(ts)
	if diff > window || diff < -window {
		return status.Errorf(codes.Unauthenticated,
			"request timestamp too far from server time (diff: %v, window: %v) — ensure NTP is running on both server and agent (e.g. timedatectl set-ntp true)",
			diff, window)
	}
	return nil
}
