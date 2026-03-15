// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package federation

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
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

// Reporter runs on spokes and periodically reports status to the hub via the
// ReportSpokeStatus gRPC RPC. It is a background goroutine and does not
// interfere with the check-in hot path.
type Reporter struct {
	spokeID    string
	hubAddr    string
	certFile   string
	keyFile    string
	caFile     string
	compliance ComplianceSource
	interval   time.Duration
}

// NewReporter creates a Reporter that will periodically send status to the hub.
func NewReporter(spokeID, hubAddr, certFile, keyFile, caFile string, compliance ComplianceSource, interval time.Duration) *Reporter {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Reporter{
		spokeID:    spokeID,
		hubAddr:    hubAddr,
		certFile:   certFile,
		keyFile:    keyFile,
		caFile:     caFile,
		compliance: compliance,
		interval:   interval,
	}
}

// Run blocks until ctx is cancelled, periodically reporting spoke status to the
// hub. Call as a goroutine: go reporter.Run(ctx).
func (r *Reporter) Run(ctx context.Context) {
	slog.Info("spoke status reporter started",
		"spoke_id", r.spokeID,
		"hub_addr", r.hubAddr,
		"interval", r.interval,
	)

	// Build mTLS credentials for connecting to the hub.
	tlsCreds, err := buildReporterTLS(r.certFile, r.keyFile, r.caFile)
	if err != nil {
		slog.Error("spoke reporter: failed to build TLS credentials, aborting",
			"error", err,
		)
		return
	}

	// Create the gRPC connection once and reuse it.
	conn, err := grpc.DialContext(ctx, r.hubAddr,
		grpc.WithTransportCredentials(tlsCreds),
	)
	if err != nil {
		slog.Error("spoke reporter: failed to dial hub, aborting",
			"hub_addr", r.hubAddr,
			"error", err,
		)
		return
	}
	defer conn.Close()

	client := pb.NewHobnobAdminClient(conn)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("spoke status reporter stopped", "spoke_id", r.spokeID)
			return
		case <-ticker.C:
			r.report(ctx, client)
		}
	}
}

// report sends a single status report to the hub.
func (r *Reporter) report(ctx context.Context, client pb.HobnobAdminClient) {
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
			"hub_addr", r.hubAddr,
			"error", err,
		)
		return
	}

	slog.Debug("spoke status reported to hub",
		"spoke_id", r.spokeID,
		"thrall_count", total,
		"compliant", compliant,
	)
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
