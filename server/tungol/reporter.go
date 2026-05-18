// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tungol

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
)

// ConvergenceSource provides compliance counters for the steorra status reporter.
// Implemented by *fleet.FleetIndex (via its ConvergenceSummary method wrapper).
type ConvergenceSource interface {
	// ConvergenceCounts returns the current convergence breakdown:
	// total, converged, relapsed, diverged, degraded, failed, offline, noData.
	ConvergenceCounts() (total, converged, relapsed, diverged, degraded, failed, offline, noData int)
}

// failoverThreshold is the number of consecutive failures on the current hub
// address before the reporter tries the next fallback address.
const failoverThreshold = 3

// Reporter runs on steorras and periodically reports status to the hub via the
// ReportSteorraStatus gRPC RPC. It is a background goroutine and does not
// interfere with the check-in hot path.
//
// When hub_fallback_addrs are configured, the reporter automatically fails over
// to fallback hub addresses after failoverThreshold consecutive failures on the
// current address.
type Reporter struct {
	steorraID        string
	tungolID         string             // federation anchor; embedded in every report
	signingKey       ed25519.PrivateKey // per-steorra identity (Phase 1); nil → unsigned legacy reports
	pubkeyBytes      []byte             // 32-byte raw pubkey derived from signingKey at construction
	hubAddr          string             // primary hub address
	hubFallbackAddrs []string           // fallback hub addresses (tried in order)
	certFile         string
	keyFile          string
	caFile           string
	convergence      ConvergenceSource
	configVersion    func() uint64 // returns current config version; nil-safe
	interval         time.Duration

	// dialFunc is used for establishing gRPC connections. Replaceable for testing.
	dialFunc func(ctx context.Context, addr string, creds credentials.TransportCredentials) (*grpc.ClientConn, error)

	// Mutable state protected by mu.
	mu        sync.Mutex
	failCount int      // consecutive failures on current address
	activeIdx int      // index into allAddrs (-1 means not yet connected)
	allAddrs  []string // hubAddr followed by hubFallbackAddrs
}

// NewReporter creates a Reporter that will periodically send status to the hub.
// hubFallbackAddrs may be nil for single-hub deployments. tungolID + signingKey
// are Phase 1 (ADR-tungol): if signingKey is non-nil, every report is signed
// with it and the corresponding pubkey is embedded so hubs can verify +
// pin-on-first-sight. A nil signingKey produces legacy unsigned reports —
// hubs accept those without pinning (mixed-version fleet during rollout).
func NewReporter(steorraID, tungolID string, signingKey ed25519.PrivateKey, hubAddr string, hubFallbackAddrs []string, certFile, keyFile, caFile string, convergence ConvergenceSource, interval time.Duration) *Reporter {
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Build the full address list: primary first, then fallbacks.
	allAddrs := make([]string, 0, 1+len(hubFallbackAddrs))
	allAddrs = append(allAddrs, hubAddr)
	allAddrs = append(allAddrs, hubFallbackAddrs...)

	var pubkeyBytes []byte
	if signingKey != nil {
		pub, ok := signingKey.Public().(ed25519.PublicKey)
		if ok {
			pubkeyBytes = []byte(pub)
		}
	}

	return &Reporter{
		steorraID:        steorraID,
		tungolID:         tungolID,
		signingKey:       signingKey,
		pubkeyBytes:      pubkeyBytes,
		hubAddr:          hubAddr,
		hubFallbackAddrs: hubFallbackAddrs,
		certFile:         certFile,
		keyFile:          keyFile,
		caFile:           caFile,
		convergence:      convergence,
		interval:         interval,
		allAddrs:         allAddrs,
		activeIdx:        0,
	}
}

// SetConfigVersion sets the function that returns the current config version.
func (r *Reporter) SetConfigVersion(fn func() uint64) {
	r.configVersion = fn
}

// Run blocks until ctx is cancelled, periodically reporting steorra status to the
// hub. Call as a goroutine: go reporter.Run(ctx).
func (r *Reporter) Run(ctx context.Context) {
	slog.Info("steorra status reporter started",
		"steorra_id", r.steorraID,
		"hub_addr", r.hubAddr,
		"hub_fallback_addrs", r.hubFallbackAddrs,
		"interval", r.interval,
	)

	// Build mTLS credentials for connecting to the hub.
	// When dialFunc is set (testing), skip TLS setup — the custom dialer
	// provides its own credentials.
	var tlsCreds credentials.TransportCredentials
	if r.dialFunc == nil {
		var err error
		tlsCreds, err = buildReporterTLS(r.certFile, r.keyFile, r.caFile)
		if err != nil {
			slog.Error("steorra reporter: failed to build TLS credentials, aborting",
				"error", err,
			)
			return
		}
	}

	// Establish initial connection.
	conn, client, err := r.dial(ctx, r.currentAddr(), tlsCreds)
	if err != nil {
		slog.Error("steorra reporter: failed to dial hub, will retry on tick",
			"hub_addr", r.currentAddr(),
			"error", err,
		)
		// Don't abort — we'll try again (possibly on a fallback) on the next tick.
	}
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("steorra status reporter stopped", "steorra_id", r.steorraID)
			return
		case <-ticker.C:
			if client == nil {
				// No connection yet — try to establish one.
				conn, client, err = r.dial(ctx, r.currentAddr(), tlsCreds)
				if err != nil {
					r.recordFailure(ctx, tlsCreds, &conn, &client)
					continue
				}
			}

			ok := r.report(ctx, client)
			if !ok {
				newConn, newClient := r.recordFailure(ctx, tlsCreds, &conn, &client)
				if newConn != nil {
					conn = newConn
					client = newClient
				}
			} else {
				r.resetFailCount()
			}
		}
	}
}

// currentAddr returns the address the reporter is currently targeting.
func (r *Reporter) currentAddr() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allAddrs[r.activeIdx]
}

// resetFailCount resets the consecutive failure counter (called on success).
func (r *Reporter) resetFailCount() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failCount = 0
}

// recordFailure increments the failure counter and, if the threshold is reached,
// rotates to the next hub address. Returns the new connection/client if a
// rotation happened, or nil if no change.
func (r *Reporter) recordFailure(ctx context.Context, tlsCreds credentials.TransportCredentials, conn **grpc.ClientConn, client *pb.VigoAdminClient) (*grpc.ClientConn, pb.VigoAdminClient) {
	r.mu.Lock()
	r.failCount++
	count := r.failCount
	numAddrs := len(r.allAddrs)
	r.mu.Unlock()

	// Only one address configured — nothing to fail over to.
	if numAddrs <= 1 {
		return nil, nil
	}

	if count < failoverThreshold {
		return nil, nil
	}

	// Rotate to the next address.
	r.mu.Lock()
	r.activeIdx = (r.activeIdx + 1) % numAddrs
	newAddr := r.allAddrs[r.activeIdx]
	r.failCount = 0
	r.mu.Unlock()

	slog.Warn("hub unreachable, trying fallback",
		"steorra_id", r.steorraID,
		"addr", newAddr,
	)

	// Close old connection and establish a new one.
	if *conn != nil {
		(*conn).Close()
		*conn = nil
		*client = nil
	}

	newConn, newClient, err := r.dial(ctx, newAddr, tlsCreds)
	if err != nil {
		slog.Warn("steorra reporter: failed to dial fallback hub",
			"addr", newAddr,
			"error", err,
		)
		return nil, nil
	}

	return newConn, newClient
}

// dial establishes a gRPC connection to the given address.
func (r *Reporter) dial(ctx context.Context, addr string, creds credentials.TransportCredentials) (*grpc.ClientConn, pb.VigoAdminClient, error) {
	if r.dialFunc != nil {
		conn, err := r.dialFunc(ctx, addr, creds)
		if err != nil {
			return nil, nil, err
		}
		return conn, pb.NewVigoAdminClient(conn), nil
	}

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(creds),
	)
	if err != nil {
		return nil, nil, err
	}
	return conn, pb.NewVigoAdminClient(conn), nil
}

// report sends a single status report to the hub. Returns true on success.
func (r *Reporter) report(ctx context.Context, client pb.VigoAdminClient) bool {
	total, converged, relapsed, diverged, degraded, failed, offline, noData := r.convergence.ConvergenceCounts()

	// Lost-contact envoys excluded from compliance percentage.
	activeTotal := total - offline
	var convergencePct float32
	if activeTotal > 0 {
		convergencePct = float32(converged) / float32(activeTotal) * 100
	}

	req := &pb.SteorraStatusReport{
		SteorraId:      r.steorraID,
		TungolId:       r.tungolID,
		EnvoyCount:     int32(total),
		Converged:      int32(converged),
		Relapsed:       int32(relapsed),
		Diverged:       int32(diverged),
		Degraded:       int32(degraded),
		Failed:         int32(failed),
		Offline:        int32(offline),
		NoData:         int32(noData),
		ConvergencePct: convergencePct,
	}
	if r.configVersion != nil {
		req.ConfigVersion = r.configVersion()
	}

	// Phase 1 (ADR-tungol): sign the report so hubs can verify the
	// asserted (tungol_id, steorra_id) → pubkey pin. Mirrors the
	// PolicyBundle signing pattern in server/grpc/bundlesign.go —
	// marshal the message with pubkey + signature fields cleared,
	// sign that, then write both fields back. Pre-Phase-1 reporters
	// (no signingKey) send unsigned reports; hubs accept those
	// without pinning during mixed-version rollout.
	if r.signingKey != nil {
		canonical, err := marshalReportContent(req)
		if err != nil {
			slog.Error("steorra reporter: canonical marshal failed", "error", err)
			return false
		}
		req.SteorraPubkey = r.pubkeyBytes
		req.Signature = ed25519.Sign(r.signingKey, canonical)
	}

	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := client.ReportSteorraStatus(rpcCtx, req)
	if err != nil {
		// Log at debug level: steorra operates independently of hub connectivity.
		slog.Debug("steorra reporter: failed to report status to hub",
			"steorra_id", r.steorraID,
			"hub_addr", r.currentAddr(),
			"error", err,
		)
		return false
	}

	slog.Debug("steorra status reported to hub",
		"steorra_id", r.steorraID,
		"envoy_count", total,
		"converged", converged,
	)
	return true
}

// ActiveAddr returns the hub address the reporter is currently targeting.
// Exported for testing and diagnostics.
func (r *Reporter) ActiveAddr() string {
	return r.currentAddr()
}

// buildReporterTLS creates mTLS credentials for the steorra's connection to the hub.
func buildReporterTLS(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

// marshalReportContent serializes a SteorraStatusReport for signing.
// Creates a copy with steorra_pubkey + signature zeroed so the
// signature covers only content fields (mirrors the PolicyBundle
// signing pattern in server/grpc/bundlesign.go). The proto3 default
// for proto.Marshal is deterministic, which is required for
// signature verification to be stable.
func marshalReportContent(r *pb.SteorraStatusReport) ([]byte, error) {
	content := &pb.SteorraStatusReport{
		SteorraId:      r.SteorraId,
		TungolId:       r.TungolId,
		EnvoyCount:     r.EnvoyCount,
		Converged:      r.Converged,
		Relapsed:       r.Relapsed,
		Diverged:       r.Diverged,
		Degraded:       r.Degraded,
		Failed:         r.Failed,
		Offline:        r.Offline,
		NoData:         r.NoData,
		ConvergencePct: r.ConvergencePct,
		ConfigVersion:  r.ConfigVersion,
		// SteorraPubkey + Signature intentionally omitted (zeroed).
	}
	return proto.Marshal(content)
}

// VerifyReportSignature checks that a report's signature is valid
// against the pubkey carried in the report itself. The hub uses
// this for two distinct purposes: (1) prove the sender holds the
// private key for the asserted pubkey; (2) compared to the pinned
// pubkey for (tungol_id, steorra_id), detect identity substitution.
// Returns nil on valid signature, or an error describing the
// rejection cause.
func VerifyReportSignature(r *pb.SteorraStatusReport) error {
	if len(r.SteorraPubkey) != ed25519.PublicKeySize {
		return fmt.Errorf("steorra_pubkey is %d bytes, expected %d", len(r.SteorraPubkey), ed25519.PublicKeySize)
	}
	if len(r.Signature) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, expected %d", len(r.Signature), ed25519.SignatureSize)
	}
	canonical, err := marshalReportContent(r)
	if err != nil {
		return fmt.Errorf("canonical marshal: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(r.SteorraPubkey), canonical, r.Signature) {
		return fmt.Errorf("ed25519 verify failed")
	}
	return nil
}
