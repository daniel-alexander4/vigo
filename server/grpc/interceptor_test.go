// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/daniel-alexander4/vigo/generated.code/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/fleet"
	isgrpc "github.com/daniel-alexander4/vigo/server/grpc"
)

// noopHandler is a grpc.UnaryHandler that records whether it was called and
// returns the request back as the response. This makes it easy to verify
// that the handler was (or was not) invoked by the interceptor.
func noopHandler(ctx context.Context, req any) (any, error) {
	return req, nil
}

// ctxCheckHandler returns a handler that captures the context passed to it,
// so tests can inspect context values (e.g. the envoy stored by the interceptor).
func ctxCheckHandler(captured *context.Context) grpc.UnaryHandler {
	return func(ctx context.Context, req any) (any, error) {
		*captured = ctx
		return req, nil
	}
}

// makeInfo creates a *grpc.UnaryServerInfo with the given FullMethod string.
func makeInfo(fullMethod string) *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: fullMethod}
}

// genKeyPair generates an ED25519 keypair and returns the PEM-encoded public
// key along with the raw public and private keys.
func genKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	pemStr, err := publicKeyPEM(pub)
	if err != nil {
		t.Fatalf("publicKeyPEM: %v", err)
	}
	return pub, priv, pemStr
}

// signCheckIn builds a signed CheckInRequest.
func signCheckIn(envoyID, fingerprint string, ts time.Time, priv ed25519.PrivateKey) *pb.CheckInRequest {
	tsStr := fmt.Sprintf("%d", ts.Unix())
	sig := signPayload([]byte(fingerprint), tsStr, priv)
	return &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: fingerprint,
		Signature:        sig,
		Timestamp:        timestamppb.New(ts),
	}
}

// signReportResult builds a signed ReportResultRequest.
func signReportResult(envoyID, runID string, ts time.Time, priv ed25519.PrivateKey) *pb.ReportResultRequest {
	tsStr := fmt.Sprintf("%d", ts.Unix())
	sig := signPayload([]byte(runID), tsStr, priv)
	return &pb.ReportResultRequest{
		EnvoyId:  envoyID,
		RunId:     runID,
		Signature: sig,
		Timestamp: timestamppb.New(ts),
	}
}

// signReportTraits builds a signed ReportTraitsRequest.
func signReportTraits(envoyID string, traitsJSON []byte, ts time.Time, priv ed25519.PrivateKey) *pb.ReportTraitsRequest {
	tsStr := fmt.Sprintf("%d", ts.Unix())
	sig := signPayload(traitsJSON, tsStr, priv)
	return &pb.ReportTraitsRequest{
		EnvoyId:   envoyID,
		TraitsJson: traitsJSON,
		Signature:  sig,
		Timestamp:  timestamppb.New(ts),
	}
}

// --- Tests ---

// TestInterceptorRegisterBypasses verifies that the Register RPC passes
// through the interceptor without any signature verification.
func TestInterceptorRegisterBypasses(t *testing.T) {
	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, nil)

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return req, nil
	}

	req := &pb.RegisterRequest{
		Hostname:  "test.example.com",
		IpAddress: "10.0.0.1",
		PubkeyPem: "fake-pem",
		Token:     "fake-token",
	}

	info := makeInfo(pb.VigoAgent_Register_FullMethodName)
	_, err := interceptor(context.Background(), req, info, handler)
	if err != nil {
		t.Fatalf("Register should pass through interceptor, got error: %v", err)
	}
	if !called {
		t.Fatal("handler was not called for Register RPC")
	}
}

// TestInterceptorCheckInValidSignature verifies that a properly signed
// CheckInRequest passes verification when the envoy is in FleetIndex.
func TestInterceptorCheckInValidSignature(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "web01.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	req := signCheckIn(envoyID, "fp-123", now, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	var captured context.Context
	_, err := interceptor(context.Background(), req, info, ctxCheckHandler(&captured))
	if err != nil {
		t.Fatalf("expected CheckIn to pass with valid signature, got: %v", err)
	}

	// Verify the interceptor stored the envoy in context.
	envoy := db.EnvoyFromContext(captured)
	if envoy == nil {
		t.Fatal("expected envoy to be stored in context")
	}
	if envoy.ID != envoyID {
		t.Errorf("expected envoy ID %s, got %s", envoyID, envoy.ID)
	}
}

// TestInterceptorCheckInUnknownEnvoy verifies that a CheckInRequest with a
// UUID not in FleetIndex or DB returns Unauthenticated.
func TestInterceptorCheckInUnknownEnvoy(t *testing.T) {
	_, priv, _ := genKeyPair(t)
	unknownID := uuid.New().String()

	fi := fleet.NewFleetIndex() // empty — envoy not present

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	req := signCheckIn(unknownID, "fp", now, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for unknown envoy_id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorCheckInExpiredTimestamp verifies that a request with a
// timestamp older than the signature window is rejected.
func TestInterceptorCheckInExpiredTimestamp(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "stale.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	// 10 minutes ago — well outside the 5-minute window.
	staleTime := time.Now().Add(-10 * time.Minute)
	req := signCheckIn(envoyID, "fp", staleTime, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for expired timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorCheckInWrongKey verifies that signing with a different key
// than the one registered causes signature verification failure.
func TestInterceptorCheckInWrongKey(t *testing.T) {
	pub, _, pubPEM := genKeyPair(t)       // key A — registered
	_, wrongPriv, _ := genKeyPair(t) // key B — used for signing
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "wrongkey.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	req := signCheckIn(envoyID, "fp", now, wrongPriv) // signed with wrong key
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for wrong signing key")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorCheckInTamperedPayload verifies that signing one payload and
// sending a different one causes verification failure.
func TestInterceptorCheckInTamperedPayload(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "tampered.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	// Sign "original-fp" but send "tampered-fp" in the request.
	tsStr := fmt.Sprintf("%d", now.Unix())
	sig := signPayload([]byte("original-fp"), tsStr, priv)

	req := &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "tampered-fp",
		Signature:        sig,
		Timestamp:        timestamppb.New(now),
	}
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for tampered payload")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorReportResultValidSignature verifies that a properly signed
// ReportResultRequest passes verification.
func TestInterceptorReportResultValidSignature(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "result.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	runID := uuid.New().String()
	req := signReportResult(envoyID, runID, now, priv)
	info := makeInfo(pb.VigoAgent_ReportResult_FullMethodName)

	var captured context.Context
	_, err := interceptor(context.Background(), req, info, ctxCheckHandler(&captured))
	if err != nil {
		t.Fatalf("expected ReportResult to pass with valid signature, got: %v", err)
	}

	// Verify the interceptor stored the envoy in context.
	envoy := db.EnvoyFromContext(captured)
	if envoy == nil {
		t.Fatal("expected envoy to be stored in context for ReportResult")
	}
	if envoy.ID != envoyID {
		t.Errorf("expected envoy ID %s, got %s", envoyID, envoy.ID)
	}
}

// TestInterceptorReportTraitsValidSignature verifies that a properly signed
// ReportTraitsRequest passes verification.
func TestInterceptorReportTraitsValidSignature(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "traits.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	traitsJSON := []byte(`{"os_family":"debian","hostname":"traits.example.com"}`)
	req := signReportTraits(envoyID, traitsJSON, now, priv)
	info := makeInfo(pb.VigoAgent_ReportTraits_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err != nil {
		t.Fatalf("expected ReportTraits to pass with valid signature, got: %v", err)
	}
}

// TestInterceptorFleetIndexFastPath verifies that when a envoy is in the
// FleetIndex (but NOT in the DB), the interceptor still works because it
// uses the in-memory fast path.
func TestInterceptorFleetIndexFastPath(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "fastpath.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	// Use the shared testDB — the envoy is NOT inserted into the DB.
	// The interceptor should succeed via FleetIndex alone.
	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	req := signCheckIn(envoyID, "fp-fast", now, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err != nil {
		t.Fatalf("expected FleetIndex fast path to work without DB, got: %v", err)
	}
}

// TestInterceptorDBFallback verifies that when a envoy is NOT in the
// FleetIndex but IS in the DB, the interceptor falls back to the DB path.
func TestInterceptorDBFallback(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)

	// Insert envoy directly into the DB via queries.
	queries := db.New(testDB)
	ctx := context.Background()
	envoy, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  fmt.Sprintf("dbfallback-%s.example.com", uuid.New().String()[:8]),
		IpAddress: "10.99.0.1",
		PubkeyPem: pubPEM,
	})
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}

	// Empty FleetIndex — envoy is NOT here.
	fi := fleet.NewFleetIndex()

	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	req := signCheckIn(envoy.ID, "fp-db-fallback", now, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	var captured context.Context
	_, err = interceptor(ctx, req, info, ctxCheckHandler(&captured))
	if err != nil {
		t.Fatalf("expected DB fallback to work, got: %v", err)
	}

	// Verify the interceptor stored the correct envoy in context.
	ctxEnvoy := db.EnvoyFromContext(captured)
	if ctxEnvoy == nil {
		t.Fatal("expected envoy to be stored in context from DB fallback")
	}
	if ctxEnvoy.ID != envoy.ID {
		t.Errorf("expected envoy ID %s, got %s", envoy.ID, ctxEnvoy.ID)
	}

	_ = pub // used for PEM generation
}

// TestInterceptorNilFleetIndex verifies that the interceptor works when
// FleetIndex is nil (pure DB mode), falling back to DB entirely.
func TestInterceptorNilFleetIndex(t *testing.T) {
	_, priv, pubPEM := genKeyPair(t)

	queries := db.New(testDB)
	ctx := context.Background()
	envoy, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  fmt.Sprintf("nilfi-%s.example.com", uuid.New().String()[:8]),
		IpAddress: "10.99.0.2",
		PubkeyPem: pubPEM,
	})
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}

	// Pass nil for FleetIndex.
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 0, nil)

	now := time.Now()
	req := signCheckIn(envoy.ID, "fp-nil-fi", now, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err = interceptor(ctx, req, info, noopHandler)
	if err != nil {
		t.Fatalf("expected nil FleetIndex to fall back to DB, got: %v", err)
	}
}

// TestInterceptorDefaultSigWindow verifies that passing sigWindow=0 uses the
// 5-minute default. A 4m59s-old request should succeed, a 5m1s-old request
// should fail.
func TestInterceptorDefaultSigWindow(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "sigwindow.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 0, fi) // 0 → default 5min

	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	// Within default window — should pass.
	within := time.Now().Add(-4*time.Minute - 59*time.Second)
	req := signCheckIn(envoyID, "fp-ok", within, priv)
	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err != nil {
		t.Fatalf("expected request within default window to pass, got: %v", err)
	}

	// Outside default window — should fail.
	outside := time.Now().Add(-5*time.Minute - 1*time.Second)
	req = signCheckIn(envoyID, "fp-fail", outside, priv)
	_, err = interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected request outside default window to fail")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", st.Code())
	}
}

// TestInterceptorCheckInMissingTimestamp verifies that a CheckInRequest without
// a timestamp is rejected with InvalidArgument.
func TestInterceptorCheckInMissingTimestamp(t *testing.T) {
	pub, _, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "nots.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        []byte("fake"),
		Timestamp:        nil, // deliberately missing
	}
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for missing timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorCheckInMalformedUUID verifies that a malformed UUID in
// envoy_id is rejected with InvalidArgument.
func TestInterceptorCheckInMalformedUUID(t *testing.T) {
	queries := db.New(testDB)
	fi := fleet.NewFleetIndex()
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.CheckInRequest{
		EnvoyId:         "not-a-valid-uuid",
		StateFingerprint: "fp",
		Signature:        []byte("fake"),
		Timestamp:        timestamppb.Now(),
	}
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for malformed UUID")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorReportResultWrongKey verifies that ReportResult also rejects
// a signature from the wrong key.
func TestInterceptorReportResultWrongKey(t *testing.T) {
	pub, _, pubPEM := genKeyPair(t)     // key A — registered
	_, wrongPriv, _ := genKeyPair(t) // key B — used for signing
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "result-wrong.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	req := signReportResult(envoyID, "run-123", now, wrongPriv)
	info := makeInfo(pb.VigoAgent_ReportResult_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for ReportResult with wrong key")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorReportTraitsExpiredTimestamp verifies that ReportTraits also
// rejects expired timestamps.
func TestInterceptorReportTraitsExpiredTimestamp(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "traits-stale.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	staleTime := time.Now().Add(-10 * time.Minute)
	traitsJSON := []byte(`{"os":"linux"}`)
	req := signReportTraits(envoyID, traitsJSON, staleTime, priv)
	info := makeInfo(pb.VigoAgent_ReportTraits_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for ReportTraits with expired timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorAdminRPCPassesThrough verifies that unrecognized RPC methods
// (like admin RPCs) pass through the interceptor without signature verification.
func TestInterceptorAdminRPCPassesThrough(t *testing.T) {
	queries := db.New(testDB)
	fi := fleet.NewFleetIndex()
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return req, nil
	}

	// Use a fake admin method name.
	info := makeInfo("/vigo.v1.VigoAdmin/ListEnvoys")
	_, err := interceptor(context.Background(), "admin-request", info, handler)
	if err != nil {
		t.Fatalf("admin RPC should pass through, got: %v", err)
	}
	if !called {
		t.Fatal("handler was not called for admin RPC")
	}
}

// TestInterceptorFutureTimestampRejected verifies that a timestamp in the
// future beyond the signature window is rejected.
func TestInterceptorFutureTimestampRejected(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "future.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	// 10 minutes in the future.
	futureTime := time.Now().Add(10 * time.Minute)
	req := signCheckIn(envoyID, "fp-future", futureTime, priv)
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for future timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorCustomSigWindow verifies that a custom signature window is
// respected. Uses a 1-second window.
func TestInterceptorCustomSigWindow(t *testing.T) {
	pub, priv, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "custom-window.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 1*time.Second, fi)

	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	// Current time — should pass.
	now := time.Now()
	req := signCheckIn(envoyID, "fp-now", now, priv)
	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err != nil {
		t.Fatalf("expected current-time request to pass with 1s window, got: %v", err)
	}

	// 2 seconds ago — should fail.
	old := time.Now().Add(-2 * time.Second)
	req = signCheckIn(envoyID, "fp-old", old, priv)
	_, err = interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected 2s-old request to fail with 1s window")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v", st.Code())
	}
}

// TestInterceptorReportResultMissingTimestamp verifies that ReportResultRequest
// without a timestamp is rejected.
func TestInterceptorReportResultMissingTimestamp(t *testing.T) {
	pub, _, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "rr-nots.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.ReportResultRequest{
		EnvoyId:  envoyID,
		RunId:     "run-1",
		Signature: []byte("fake"),
		Timestamp: nil,
	}
	info := makeInfo(pb.VigoAgent_ReportResult_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for ReportResult with missing timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorReportTraitsMissingTimestamp verifies that ReportTraitsRequest
// without a timestamp is rejected.
func TestInterceptorReportTraitsMissingTimestamp(t *testing.T) {
	pub, _, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "rt-nots.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.ReportTraitsRequest{
		EnvoyId:   envoyID,
		TraitsJson: []byte(`{}`),
		Signature:  []byte("fake"),
		Timestamp:  nil,
	}
	info := makeInfo(pb.VigoAgent_ReportTraits_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for ReportTraits with missing timestamp")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorReportResultMalformedUUID verifies that ReportResultRequest
// with a malformed UUID is rejected.
func TestInterceptorReportResultMalformedUUID(t *testing.T) {
	queries := db.New(testDB)
	fi := fleet.NewFleetIndex()
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.ReportResultRequest{
		EnvoyId:  "bad-uuid",
		RunId:     "run-1",
		Signature: []byte("fake"),
		Timestamp: timestamppb.Now(),
	}
	info := makeInfo(pb.VigoAgent_ReportResult_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for ReportResult with malformed UUID")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorReportTraitsMalformedUUID verifies that ReportTraitsRequest
// with a malformed UUID is rejected.
func TestInterceptorReportTraitsMalformedUUID(t *testing.T) {
	queries := db.New(testDB)
	fi := fleet.NewFleetIndex()
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.ReportTraitsRequest{
		EnvoyId:   "bad-uuid",
		TraitsJson: []byte(`{}`),
		Signature:  []byte("fake"),
		Timestamp:  timestamppb.Now(),
	}
	info := makeInfo(pb.VigoAgent_ReportTraits_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for ReportTraits with malformed UUID")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v: %s", st.Code(), st.Message())
	}
}

// TestInterceptorDBFallbackReportResult verifies that ReportResult also works
// via the DB fallback path when not in FleetIndex.
func TestInterceptorDBFallbackReportResult(t *testing.T) {
	_, priv, pubPEM := genKeyPair(t)

	queries := db.New(testDB)
	ctx := context.Background()
	envoy, err := queries.CreateEnvoy(ctx, db.CreateEnvoyParams{
		Hostname:  fmt.Sprintf("dbfb-rr-%s.example.com", uuid.New().String()[:8]),
		IpAddress: "10.99.0.3",
		PubkeyPem: pubPEM,
	})
	if err != nil {
		t.Fatalf("CreateEnvoy: %v", err)
	}

	fi := fleet.NewFleetIndex() // empty
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	now := time.Now()
	runID := "run-db-fallback"
	req := signReportResult(envoy.ID, runID, now, priv)
	info := makeInfo(pb.VigoAgent_ReportResult_FullMethodName)

	var captured context.Context
	_, err = interceptor(ctx, req, info, ctxCheckHandler(&captured))
	if err != nil {
		t.Fatalf("expected DB fallback to work for ReportResult, got: %v", err)
	}

	ctxEnvoy := db.EnvoyFromContext(captured)
	if ctxEnvoy == nil {
		t.Fatal("expected envoy in context from DB fallback for ReportResult")
	}
	if ctxEnvoy.ID != envoy.ID {
		t.Errorf("expected envoy ID %s, got %s", envoy.ID, ctxEnvoy.ID)
	}
}

// TestInterceptorEmptySignature verifies that an empty signature is rejected.
func TestInterceptorEmptySignature(t *testing.T) {
	pub, _, pubPEM := genKeyPair(t)
	envoyID := uuid.New().String()

	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:        envoyID,
		Hostname:  "emptysig.example.com",
		PubkeyPEM: pubPEM,
	}, pub)

	queries := db.New(testDB)
	interceptor := isgrpc.SignatureVerifyInterceptor(queries, 5*time.Minute, fi)

	req := &pb.CheckInRequest{
		EnvoyId:         envoyID,
		StateFingerprint: "fp",
		Signature:        nil, // empty signature
		Timestamp:        timestamppb.Now(),
	}
	info := makeInfo(pb.VigoAgent_CheckIn_FullMethodName)

	_, err := interceptor(context.Background(), req, info, noopHandler)
	if err == nil {
		t.Fatal("expected error for empty signature")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated, got %v: %s", st.Code(), st.Message())
	}
}
