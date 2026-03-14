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
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/fleet"
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
// The looked-up thrall is stored in the request context so downstream handlers
// can reuse it without another lookup.
//
// sigWindow configures the maximum age of a signed request before rejection
// for replay prevention. If zero, defaults to 5 minutes.
func SignatureVerifyInterceptor(queries *db.Queries, sigWindow time.Duration, fleetIdx *fleet.FleetIndex) grpc.UnaryServerInterceptor {
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
			newCtx, err := verifyCheckIn(ctx, queries, r, sigWindow, fleetIdx)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.ReportResultRequest:
			newCtx, err := verifyReportResultCtx(ctx, queries, r, sigWindow, fleetIdx)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.ReportTraitsRequest:
			if err := verifyReportTraits(ctx, queries, r, sigWindow, fleetIdx); err != nil {
				return nil, err
			}
		default:
			// Admin RPCs and unknown types pass through without agent signature checks.
		}

		start := time.Now()
		resp, err := handler(ctx, req)
		code := status.Code(err)
		metrics.GRPCRequestsTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
		metrics.GRPCRequestDuration.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
		return resp, err
	}
}

// verifyCheckIn validates the signature on a CheckInRequest. On success, it
// returns a context enriched with the looked-up thrall for handler reuse.
func verifyCheckIn(ctx context.Context, queries *db.Queries, req *pb.CheckInRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex) (context.Context, error) {
	if _, err := uuid.Parse(req.GetThrallId()); err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid thrall_id: %v", err)
	}
	thrallID := req.GetThrallId()

	thrall, pubKey, err := lookupThrallAndPubKey(ctx, queries, thrallID, fleetIdx)
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
		metrics.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	// Store thrall in context for handler reuse (eliminates duplicate lookups).
	return db.ContextWithThrall(ctx, thrall), nil
}

// verifyReportResultCtx validates the signature on a ReportResultRequest and
// stores the thrall in context for handler reuse.
func verifyReportResultCtx(ctx context.Context, queries *db.Queries, req *pb.ReportResultRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex) (context.Context, error) {
	if _, err := uuid.Parse(req.GetThrallId()); err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid thrall_id: %v", err)
	}
	thrallID := req.GetThrallId()

	thrall, pubKey, err := lookupThrallAndPubKey(ctx, queries, thrallID, fleetIdx)
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
		metrics.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return db.ContextWithThrall(ctx, thrall), nil
}

// verifyReportTraits validates the signature on a ReportTraitsRequest.
func verifyReportTraits(ctx context.Context, queries *db.Queries, req *pb.ReportTraitsRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex) error {
	if _, err := uuid.Parse(req.GetThrallId()); err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid thrall_id: %v", err)
	}
	thrallID := req.GetThrallId()

	_, pubKey, err := lookupThrallAndPubKey(ctx, queries, thrallID, fleetIdx)
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
		metrics.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// lookupThrallAndPubKey retrieves a thrall and its parsed ED25519 public key.
// When a FleetIndex is available, the parsed pubkey is served from memory (O(1),
// no PEM parse). Falls back to direct DB query.
func lookupThrallAndPubKey(ctx context.Context, queries *db.Queries, thrallID string, fleetIdx *fleet.FleetIndex) (db.Thrall, ed25519.PublicKey, error) {
	// Fast path: FleetIndex returns parsed pubkey from memory.
	if fleetIdx != nil {
		pubKey := fleetIdx.GetPubKey(thrallID)
		if pubKey != nil {
			ts, ok, _ := fleetIdx.GetThrallLite(thrallID)
			if ok {
				thrall := fleetThrallToDB(ts)
				return thrall, pubKey, nil
			}
		}
		// Fall through to DB if not in FleetIndex.
	}

	// Slow path: direct DB query + parse.
	thrall, err := queries.GetThrallByID(ctx, thrallID)
	if err != nil {
		metrics.SigVerifyFailed.WithLabelValues("thrall_not_found").Inc()
		slog.Error("thrall lookup failed", "thrall_id", thrallID, "error", err)
		return db.Thrall{}, nil, status.Error(codes.Unauthenticated, "thrall not found")
	}

	block, _ := pem.Decode([]byte(thrall.PubkeyPem))
	if block == nil {
		return db.Thrall{}, nil, status.Error(codes.Internal, "invalid stored public key")
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return db.Thrall{}, nil, status.Errorf(codes.Internal, "parsing stored public key: %v", err)
	}

	pubKey, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return db.Thrall{}, nil, status.Error(codes.Internal, "stored key is not ED25519")
	}

	return thrall, pubKey, nil
}

// fleetThrallToDB converts a FleetIndex ThrallState to a db.Thrall for
// compatibility with code that expects the DB model.
func fleetThrallToDB(ts *fleet.ThrallState) db.Thrall {
	t := db.Thrall{
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
	if len(ts.Tags) > 0 {
		t.Tags = strings.Join(ts.Tags, ",")
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
	if math.Abs(float64(diff)) > float64(window) {
		return status.Errorf(codes.Unauthenticated,
			"request timestamp too far from server time (diff: %v, window: %v) — ensure NTP is running on both server and agent (e.g. timedatectl set-ntp true)",
			diff, window)
	}
	return nil
}
