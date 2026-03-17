// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package spanner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ComplianceSource provides compliance counters for the spoke status reporter.
// Implemented by *fleet.FleetIndex (via its ComplianceSummary method wrapper).
type ComplianceSource interface {
	// ComplianceCounts returns the current compliance breakdown:
	// total, compliant, drifted, failing, stale, unknown.
	ComplianceCounts() (total, compliant, drifted, failing, stale, unknown int)
}

// failoverThreshold is the number of consecutive failures on the current hub
// address before the reporter tries the next fallback address.
const failoverThreshold = 3

// Reporter runs on spokes and periodically reports status to the hub via the
// ReportSpokeStatus gRPC RPC. It is a background goroutine and does not
// interfere with the check-in hot path.
//
// When hub_fallback_addrs are configured, the reporter automatically fails over
// to fallback hub addresses after failoverThreshold consecutive failures on the
// current address.
type Reporter struct {
	spokeID          string
	hubAddr          string   // primary hub address
	hubFallbackAddrs []string // fallback hub addresses (tried in order)
	certFile         string
	keyFile          string
	caFile           string
	compliance       ComplianceSource
	interval         time.Duration

	// dialFunc is used for establishing gRPC connections. Replaceable for testing.
	dialFunc func(ctx context.Context, addr string, creds credentials.TransportCredentials) (*grpc.ClientConn, error)

	// Mutable state protected by mu.
	mu        sync.Mutex
	failCount int    // consecutive failures on current address
	activeIdx int    // index into allAddrs (-1 means not yet connected)
	allAddrs  []string // hubAddr followed by hubFallbackAddrs
}

// NewReporter creates a Reporter that will periodically send status to the hub.
// hubFallbackAddrs may be nil for single-hub deployments.
func NewReporter(spokeID, hubAddr string, hubFallbackAddrs []string, certFile, keyFile, caFile string, compliance ComplianceSource, interval time.Duration) *Reporter {
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Build the full address list: primary first, then fallbacks.
	allAddrs := make([]string, 0, 1+len(hubFallbackAddrs))
	allAddrs = append(allAddrs, hubAddr)
	allAddrs = append(allAddrs, hubFallbackAddrs...)

	return &Reporter{
		spokeID:          spokeID,
		hubAddr:          hubAddr,
		hubFallbackAddrs: hubFallbackAddrs,
		certFile:         certFile,
		keyFile:          keyFile,
		caFile:           caFile,
		compliance:       compliance,
		interval:         interval,
		allAddrs:         allAddrs,
		activeIdx:        0,
	}
}

// Run blocks until ctx is cancelled, periodically reporting spoke status to the
// hub. Call as a goroutine: go reporter.Run(ctx).
func (r *Reporter) Run(ctx context.Context) {
	slog.Info("spoke status reporter started",
		"spoke_id", r.spokeID,
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
			slog.Error("spoke reporter: failed to build TLS credentials, aborting",
				"error", err,
			)
			return
		}
	}

	// Establish initial connection.
	conn, client, err := r.dial(ctx, r.currentAddr(), tlsCreds)
	if err != nil {
		slog.Error("spoke reporter: failed to dial hub, will retry on tick",
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
			slog.Info("spoke status reporter stopped", "spoke_id", r.spokeID)
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
func (r *Reporter) recordFailure(ctx context.Context, tlsCreds credentials.TransportCredentials, conn **grpc.ClientConn, client *pb.HobnobAdminClient) (*grpc.ClientConn, pb.HobnobAdminClient) {
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
		"spoke_id", r.spokeID,
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
		slog.Warn("spoke reporter: failed to dial fallback hub",
			"addr", newAddr,
			"error", err,
		)
		return nil, nil
	}

	return newConn, newClient
}

// dial establishes a gRPC connection to the given address.
func (r *Reporter) dial(ctx context.Context, addr string, creds credentials.TransportCredentials) (*grpc.ClientConn, pb.HobnobAdminClient, error) {
	if r.dialFunc != nil {
		conn, err := r.dialFunc(ctx, addr, creds)
		if err != nil {
			return nil, nil, err
		}
		return conn, pb.NewHobnobAdminClient(conn), nil
	}

	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(creds),
	)
	if err != nil {
		return nil, nil, err
	}
	return conn, pb.NewHobnobAdminClient(conn), nil
}

// report sends a single status report to the hub. Returns true on success.
func (r *Reporter) report(ctx context.Context, client pb.HobnobAdminClient) bool {
	total, compliant, drifted, failing, stale, unknown := r.compliance.ComplianceCounts()

	var compliancePct float32
	if total > 0 {
		compliancePct = float32(compliant) / float32(total) * 100
	}

	req := &pb.SpokeStatusReport{
		SpokeId:       r.spokeID,
		ThrallCount:   int32(total),
		Compliant:     int32(compliant),
		Drifted:       int32(drifted),
		Failing:       int32(failing),
		Stale:         int32(stale),
		Unknown:       int32(unknown),
		CompliancePct: compliancePct,
	}

	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := client.ReportSpokeStatus(rpcCtx, req)
	if err != nil {
		// Log at debug level: spoke operates independently of hub connectivity.
		slog.Debug("spoke reporter: failed to report status to hub",
			"spoke_id", r.spokeID,
			"hub_addr", r.currentAddr(),
			"error", err,
		)
		return false
	}

	slog.Debug("spoke status reported to hub",
		"spoke_id", r.spokeID,
		"thrall_count", total,
		"compliant", compliant,
	)
	return true
}

// ActiveAddr returns the hub address the reporter is currently targeting.
// Exported for testing and diagnostics.
func (r *Reporter) ActiveAddr() string {
	return r.currentAddr()
}

// buildReporterTLS creates mTLS credentials for the spoke's connection to the hub.
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
