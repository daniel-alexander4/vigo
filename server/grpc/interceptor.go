// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/cpauth"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"github.com/google/uuid"
	voied25519 "github.com/oasisprotocol/curve25519-voi/primitives/ed25519"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// defaultSignatureWindow is used when no custom window is provided.
const defaultSignatureWindow = 5 * time.Minute

// vigoAdminMethodPrefix is the gRPC method-name prefix for every VigoAdmin
// RPC, derived from the generated service descriptor so it can never drift
// from the wire format. gRPC FullMethod is "/<package>.<Service>/<Method>";
// the proto package is vigo.v1, so this is "/vigo.v1.VigoAdmin/". Deriving it
// (rather than hardcoding) is deliberate: a prior hardcoded "/vigo.VigoAdmin/"
// silently stopped matching after the package gained its .v1 version segment,
// which disabled the roster-bolt control-plane gate entirely.
var vigoAdminMethodPrefix = "/" + pb.VigoAdmin_ServiceDesc.ServiceName + "/"

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
//
// lim (nil = rate limiting disabled) supplies the two check-in limits that
// depend on the verify outcome: a source IP over its failed-verification
// budget is refused before any verify work, a failed verification charges
// that budget, and the per-envoy bucket is charged only once the signature
// has verified — so an unsigned request naming a victim's id cannot spend the
// victim's budget.
func SignatureVerifyInterceptor(queries *db.Queries, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics, lim *CheckInLimiter) grpc.UnaryServerInterceptor {
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

		// Privileged control-plane RPCs (spanner VigoAdmin between roster
		// bolts) must carry a signed call from an admitted roster bolt when
		// they arrive over the network. The operator path reaches VigoAdmin
		// over the local UDS (already token+RBAC-authed at the REST layer) and
		// is exempt — a network caller cannot present a Unix-socket peer addr,
		// so this transport check is the unforgeable boundary, not an
		// exemption a forged request can satisfy.
		if strings.HasPrefix(info.FullMethod, vigoAdminMethodPrefix) && !isLocalSocket(ctx) {
			if err := verifyRosterBoltCall(ctx, queries, sigWindow); err != nil {
				slog.Warn("rejected control-plane RPC", "method", info.FullMethod, "error", err)
				return nil, err
			}
		}

		// Only verify agent RPCs that carry signatures.
		switch r := req.(type) {
		case *pb.CheckInRequest:
			var ip string
			if lim != nil {
				ip = peerIP(ctx)
				if lim.ipThrottled(ip) {
					return nil, lim.throttledIP(ip)
				}
			}
			newCtx, err := verifyCheckIn(ctx, queries, r, sigWindow, fleetIdx, m)
			if err != nil {
				if lim != nil && isVerifyFailure(err) {
					lim.recordFailure(ip)
				}
				return nil, err
			}
			if err := lim.allowEnvoy(r.GetEnvoyId()); err != nil {
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
		case *pb.ResolveSecretsRequest:
			newCtx, err := verifyResolveSecretsCtx(ctx, queries, r, sigWindow, fleetIdx, m)
			if err != nil {
				return nil, err
			}
			ctx = newCtx
		case *pb.WakeRequest:
			if err := verifyWake(ctx, queries, r, sigWindow, fleetIdx, m); err != nil {
				return nil, err
			}
		default:
			// Admin RPCs and unknown types pass through without agent signature checks.
		}

		start := time.Now()
		resp, err := handler(ctx, req)
		dur := time.Since(start)
		code := status.Code(err)
		m.GRPCRequestsTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
		m.GRPCRequestDuration.WithLabelValues(info.FullMethod).Observe(dur.Seconds())
		// Slow-RPC warn: anything above 100ms is 30× the clean-baseline
		// p99 — load-test data showed the system collapses by going from
		// sub-ms to seconds, so a single threshold is enough to catch
		// the cliff without spamming during steady state.
		if dur >= 100*time.Millisecond {
			slog.Warn("slow rpc",
				"method", info.FullMethod,
				"duration", dur,
				"code", code.String(),
			)
		}
		return resp, err
	}
}

// isVerifyFailure reports whether a check-in verify error is a failed
// identity proof — unknown envoy, revoked envoy, bad signature or cert-pin
// refusal — and so charges the source IP's failure budget. Clock skew is
// excluded (an honest host with a drifting clock would otherwise throttle
// every envoy behind its NAT address), as are malformed-request and internal
// errors.
func isVerifyFailure(err error) bool {
	st := status.Convert(err)
	switch st.Code() {
	case codes.NotFound, codes.PermissionDenied:
		return true
	case codes.Unauthenticated:
		return !strings.HasPrefix(st.Message(), clockSkewMsg)
	default:
		return false
	}
}

// isLocalSocket reports whether the RPC arrived over the local operator
// Unix-domain socket (the grpc-gateway path) rather than the network TCP
// port. A network caller's peer addr is a *net.TCPAddr and can never be a
// *net.UnixAddr, so this is an unforgeable transport boundary.
func isLocalSocket(ctx context.Context) bool {
	if p, ok := peer.FromContext(ctx); ok {
		_, isUnix := p.Addr.(*net.UnixAddr)
		return isUnix
	}
	return false
}

// verifyRosterBoltCall authorizes a privileged VigoAdmin RPC arriving over
// the network: the caller must present a valid control-plane signature
// (cpauth) whose public key is an admitted, non-revoked roster bolt. mTLS is
// the transport floor; this is the identity gate on top of it.
func verifyRosterBoltCall(ctx context.Context, queries *db.Queries, window time.Duration) error {
	pubHex, err := cpauth.Verify(ctx, window)
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "control-plane auth: %v", err)
	}
	bolts, err := queries.ListLiveBolts(ctx)
	if err != nil {
		return status.Errorf(codes.Internal, "roster lookup: %v", err)
	}
	for _, b := range bolts {
		if b.Pubkey == pubHex {
			return nil
		}
	}
	return status.Error(codes.PermissionDenied, "caller is not an admitted roster bolt")
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
		m.SigVerifyFailed.WithLabelValues("timestamp_skew").Inc()
		return ctx, err
	}

	// The payload for CheckIn is the state fingerprint.
	payload := []byte(req.GetStateFingerprint())
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey, envoyID, fleetIdx) {
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
		m.SigVerifyFailed.WithLabelValues("timestamp_skew").Inc()
		return ctx, err
	}

	payload := []byte(req.GetRunId())
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey, envoyID, fleetIdx) {
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
		m.SigVerifyFailed.WithLabelValues("timestamp_skew").Inc()
		return err
	}

	payload := req.GetTraitsJson()
	tsStr := fmt.Sprintf("%d", tsTime.Unix())

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey, envoyID, fleetIdx) {
		m.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return nil
}

// verifyResolveSecretsCtx validates the signature on a ResolveSecretsRequest
// and stores the envoy in context for handler reuse. The signed payload is
// the sorted refs joined by NUL bytes — keeps the signature stable across
// any caller-side reordering while binding to the exact set of refs being
// asked for (no reuse of a signature for a different ref set).
func verifyResolveSecretsCtx(ctx context.Context, queries *db.Queries, req *pb.ResolveSecretsRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (context.Context, error) {
	if _, err := uuid.Parse(req.GetEnvoyId()); err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}
	envoyID := req.GetEnvoyId()

	envoy, pubKey, err := lookupEnvoyAndPubKey(ctx, queries, envoyID, fleetIdx, m)
	if err != nil {
		return ctx, err
	}

	tsStr := req.GetTimestamp()
	if tsStr == "" {
		return ctx, status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsUnix, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return ctx, status.Errorf(codes.InvalidArgument, "invalid timestamp: %v", err)
	}
	if err := checkTimestamp(time.Unix(tsUnix, 0), sigWindow); err != nil {
		m.SigVerifyFailed.WithLabelValues("timestamp_skew").Inc()
		return ctx, err
	}

	sortedRefs := append([]string(nil), req.GetRefs()...)
	sort.Strings(sortedRefs)
	payload := []byte(strings.Join(sortedRefs, "\x00"))

	if !verifyPayload(payload, tsStr, req.GetSignature(), pubKey, envoyID, fleetIdx) {
		m.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return ctx, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return db.ContextWithEnvoy(ctx, envoy), nil
}

// verifyWake validates the signature on a WakeRequest. WakeRequest carries a
// string timestamp (like ResolveSecrets, not a protobuf Timestamp), and the
// agent signs over the envoy_id bytes (agent/src/checkin/client.rs). No
// envoy-context reuse downstream, so it returns only error. Revocation is
// enforced in lookupEnvoyAndPubKey.
func verifyWake(ctx context.Context, queries *db.Queries, req *pb.WakeRequest, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) error {
	_, err := verifyEnvoyIDSignature(ctx, queries, req.GetEnvoyId(), req.GetTimestamp(), req.GetSignature(), sigWindow, fleetIdx, m)
	return err
}

// verifyEnvoyIDSignature verifies a request whose signed payload is the
// envoy_id bytes and whose timestamp is a decimal-unix string — the Wake RPC
// and the TunnelStream handshake. It runs the same gates as every other agent
// verify path: UUID shape, lookupEnvoyAndPubKey (revocation + cert pin), the
// signature-window timestamp check, then the ed25519 signature. Returns the
// resolved envoy for callers that need it.
func verifyEnvoyIDSignature(ctx context.Context, queries *db.Queries, envoyID, tsStr string, sig []byte, sigWindow time.Duration, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (db.Envoy, error) {
	if _, err := uuid.Parse(envoyID); err != nil {
		return db.Envoy{}, status.Errorf(codes.InvalidArgument, "invalid envoy_id: %v", err)
	}

	envoy, pubKey, err := lookupEnvoyAndPubKey(ctx, queries, envoyID, fleetIdx, m)
	if err != nil {
		return db.Envoy{}, err
	}

	if tsStr == "" {
		return db.Envoy{}, status.Error(codes.InvalidArgument, "timestamp is required")
	}
	tsUnix, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return db.Envoy{}, status.Errorf(codes.InvalidArgument, "invalid timestamp: %v", err)
	}
	if err := checkTimestamp(time.Unix(tsUnix, 0), sigWindow); err != nil {
		m.SigVerifyFailed.WithLabelValues("timestamp_skew").Inc()
		return db.Envoy{}, err
	}

	if !verifyPayload([]byte(envoyID), tsStr, sig, pubKey, envoyID, fleetIdx) {
		m.SigVerifyFailed.WithLabelValues("bad_signature").Inc()
		return db.Envoy{}, status.Error(codes.Unauthenticated, "signature verification failed")
	}

	return envoy, nil
}

// errRevoked is the refusal for a revoked envoy — every verify path returns
// exactly this, whether the revoked bit came from the index or the DB.
var errRevoked = status.Error(codes.PermissionDenied, "envoy is revoked")

// unknownEnvoy is the refusal for an id with no enrolled envoy (never
// enrolled, deleted, or a server DB reset left this agent orphaned). NotFound
// (not Unauthenticated) so the agent can recognize "re-bootstrap required"
// specifically and back off — distinct from bad-signature/clock-skew. Also a
// client-auth event, so Warn not Error, throttled per envoy.
func unknownEnvoy(envoyID string, cause error, m *metrics.Metrics) error {
	m.SigVerifyFailed.WithLabelValues("envoy_not_found").Inc()
	if defaultUnknownEnvoyLog.shouldLog(envoyID, time.Now()) {
		args := []any{"envoy_id", envoyID}
		if cause != nil {
			args = append(args, "error", cause)
		}
		slog.Warn("check-in from unknown envoy — not enrolled; agent must re-bootstrap", args...)
	}
	return status.Error(codes.NotFound, "unknown envoy: not enrolled — re-bootstrap this agent")
}

// resolveEnvoyAndPubKey retrieves an envoy and its parsed ED25519 public key.
//
// With a FleetIndex (every running server) this never reads the DB: every
// enrollment path inserts into the index synchronously with the DB row
// (registry.insertEnvoyLocked), Rehydrate loads every non-revoked envoy before
// the gRPC listener starts, and the index's revoked-id set covers revoked
// envoys, which are not rehydrated. So an id the index doesn't hold is
// answered from memory — revoked → PermissionDenied, otherwise NotFound — and
// a flood of made-up ids costs map lookups, not queries. The DB path remains
// only for callers with no index (tests, tools).
func resolveEnvoyAndPubKey(ctx context.Context, queries *db.Queries, envoyID string, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (db.Envoy, ed25519.PublicKey, error) {
	if fleetIdx != nil {
		ts, ok, revoked := fleetIdx.GetEnvoyLite(envoyID)
		if !ok {
			if fleetIdx.IsRevokedID(envoyID) {
				m.SigVerifyFailed.WithLabelValues("revoked").Inc()
				return db.Envoy{}, nil, errRevoked
			}
			return db.Envoy{}, nil, unknownEnvoy(envoyID, nil, m)
		}
		envoy := fleetEnvoyToDB(ts)
		if revoked {
			// lookupEnvoyAndPubKey refuses it; no key needed.
			return envoy, nil, nil
		}
		if pubKey := fleetIdx.GetPubKey(envoyID); pubKey != nil {
			return envoy, pubKey, nil
		}
		// Held without a parsed key (the stored PEM failed to parse at
		// enrollment/rehydrate): parse the same PEM from memory so the error
		// is the one the DB row would give.
		pubKey, err := parseStoredPubKey(envoy.PubkeyPem)
		if err != nil {
			return db.Envoy{}, nil, err
		}
		return envoy, pubKey, nil
	}

	envoy, err := queries.GetEnvoyByID(ctx, envoyID)
	if err != nil {
		return db.Envoy{}, nil, unknownEnvoy(envoyID, err, m)
	}
	pubKey, err := parseStoredPubKey(envoy.PubkeyPem)
	if err != nil {
		return db.Envoy{}, nil, err
	}
	return envoy, pubKey, nil
}

// parseStoredPubKey parses an envoy's stored PEM public key, returning the
// verify path's Internal errors for a malformed or non-ED25519 key.
func parseStoredPubKey(pubkeyPEM string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pubkeyPEM))
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

// lookupEnvoyAndPubKey resolves the envoy + its ed25519 pubkey and enforces
// revocation and the mTLS client-cert pin in one place — every signature-verify
// path bottoms out here, so binding both checks to it covers unary CheckIn/
// ReportResult/ReportTraits/ResolveSecrets/Wake, the TunnelStream handshake and
// the held-stream handshake at once. A revoked envoy still holds a valid key and
// a CA-signed cert, so the revoked bit is the only thing that stops it. With a
// fleet index it comes from memory (fleetEnvoyToDB carries it for held envoys;
// the revoked-id set covers the rest); without one, from the DB row. Held-stream messages that skip per-message
// verification (checkin.stream_signature: handshake-only) never reach here and
// keep their own per-message revoked re-check in AgentStream.
func lookupEnvoyAndPubKey(ctx context.Context, queries *db.Queries, envoyID string, fleetIdx *fleet.FleetIndex, m *metrics.Metrics) (db.Envoy, ed25519.PublicKey, error) {
	envoy, pubKey, err := resolveEnvoyAndPubKey(ctx, queries, envoyID, fleetIdx, m)
	if err != nil {
		return db.Envoy{}, nil, err
	}
	if envoy.Revoked != 0 {
		m.SigVerifyFailed.WithLabelValues("revoked").Inc()
		return db.Envoy{}, nil, errRevoked
	}
	if err := enforceCertPin(ctx, envoy, m); err != nil {
		return db.Envoy{}, nil, err
	}
	return envoy, pubKey, nil
}

// certPinMismatchLog and unpinnedLog throttle the two cert-pin warnings to
// once per envoy per window (same pattern as defaultUnknownEnvoyLog).
var (
	certPinMismatchLog = newUnknownEnvoyLog(10 * time.Minute)
	unpinnedLog        = newUnknownEnvoyLog(10 * time.Minute)
)

// errUnpinned is the refusal for an envoy with no enrolled cert pin.
var errUnpinned = status.Error(codes.Unauthenticated,
	"envoy has no enrolled client certificate pin — re-enroll it: curl -sSfk https://<server>:8443/bootstrap | sudo sh")

// enforceCertPin rejects a connection whose TLS client-cert SubjectPublicKeyInfo
// doesn't match the envoy's enrolled pin — a TLS-terminating proxy or MITM
// presenting a different CA-signed cert (mTLS is CA-chain-validated, not pinned
// to the enrolled key, so directness must be enforced here). An empty stored pin
// (an envoy enrolled before pinning, or whose pin failed to store) fails closed
// too: without a pin there is nothing to prove the connection is direct, so the
// envoy is refused until re-enrollment captures one. The startup WARN and
// `vigocli doctor` list such envoys. A nil/non-TLS peer or a missing client
// cert fails closed.
func enforceCertPin(ctx context.Context, envoy db.Envoy, m *metrics.Metrics) error {
	pin := envoy.CertSpkiSha256
	if pin == "" {
		m.SigVerifyFailed.WithLabelValues("cert_pin_missing").Inc()
		if unpinnedLog.shouldLog(envoy.ID, time.Now()) {
			slog.Warn("cert-pin: envoy has no enrolled client-cert pin; rejecting — re-enroll it: curl -sSfk https://<server>:8443/bootstrap | sudo sh",
				"envoy_id", envoy.ID, "hostname", envoy.Hostname)
		}
		return errUnpinned
	}
	got, err := peerCertSPKI(ctx)
	if err != nil {
		return err
	}
	if got != pin {
		m.SigVerifyFailed.WithLabelValues("cert_pin_mismatch").Inc()
		if certPinMismatchLog.shouldLog(envoy.ID, time.Now()) {
			slog.Warn("cert-pin: client certificate does not match enrolled identity — TLS-terminating proxy/MITM on the gRPC path or wrong cert; rejecting",
				"envoy_id", envoy.ID, "hostname", envoy.Hostname)
		}
		return status.Error(codes.Unauthenticated, "client certificate does not match enrolled identity")
	}
	return nil
}

// peerCertSPKI returns the hex SHA-256 of the mTLS client certificate's
// SubjectPublicKeyInfo — the cert pin form enforceCertPin compares and every
// enrollment path stores. A nil/non-TLS peer or a missing client cert is an
// Unauthenticated error.
func peerCertSPKI(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "cert pin: no peer connection")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return "", status.Error(codes.Unauthenticated, "cert pin: no client certificate")
	}
	sum := sha256.Sum256(tlsInfo.State.PeerCertificates[0].RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:]), nil
}

// fleetEnvoyToDB converts a FleetIndex EnvoyState to a db.Envoy for
// compatibility with code that expects the DB model.
func fleetEnvoyToDB(ts *fleet.EnvoyState) db.Envoy {
	t := db.Envoy{
		ID:             ts.ID,
		Hostname:       ts.Hostname,
		IpAddress:      ts.IP,
		PubkeyPem:      ts.PubkeyPEM,
		CertSpkiSha256: ts.CertSPKI,
		EnrolledAt:     ts.EnrolledAt,
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
// The fast path uses FleetIndex's pre-expanded public key cache (skips
// the curve-point decompression stdlib ed25519.Verify does on every
// call). The slow path — envoy not in FleetIndex, e.g. enrolled during
// a rehydration race — falls back to curve25519-voi's plain Verify on
// the raw bytes (still AVX2-accelerated, no expansion benefit).
func verifyPayload(payload []byte, timestamp string, sig []byte, pub ed25519.PublicKey, envoyID string, fleetIdx *fleet.FleetIndex) bool {
	msg := make([]byte, len(payload)+len(timestamp))
	copy(msg, payload)
	copy(msg[len(payload):], timestamp)
	if fleetIdx != nil {
		if ok, found := fleetIdx.Verify(envoyID, msg, sig); found {
			return ok
		}
	}
	return voied25519.Verify(voied25519.PublicKey(pub), msg, sig)
}

// clockSkewMsg prefixes checkTimestamp's refusal; isVerifyFailure keys on it
// to keep clock skew out of the per-IP failure budget.
const clockSkewMsg = "request timestamp too far from server time"

// checkTimestamp verifies the timestamp is within the allowed window.
func checkTimestamp(ts time.Time, window time.Duration) error {
	diff := time.Since(ts)
	if diff > window || diff < -window {
		return status.Errorf(codes.Unauthenticated,
			clockSkewMsg+" (diff: %v, window: %v) — ensure NTP is running on both server and agent (e.g. timedatectl set-ntp true)",
			diff, window)
	}
	return nil
}
