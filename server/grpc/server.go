// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package grpc provides the mTLS gRPC server for Vigo, implementing
// both VigoAgent and VigoAdmin service handlers.
package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/task"
	"git.alexander4.io/vigo/server/tickets"
	"git.alexander4.io/vigo/server/traits"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// CertCache holds a TLS certificate atomically, allowing hot reload
// without server restart. The watcher calls Reload() when a TLS cert
// secret rotates; GetCertificate serves the cached cert on each handshake.
type CertCache struct {
	cert *tls.Certificate
	mu   sync.RWMutex
}

// NewCertCache creates a CertCache pre-loaded with an initial certificate.
func NewCertCache(initial *tls.Certificate) *CertCache {
	return &CertCache{cert: initial}
}

// GetCertificate returns the cached certificate for TLS handshakes.
// It satisfies the tls.Config.GetCertificate callback signature.
func (cc *CertCache) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	cc.mu.RLock()
	defer cc.mu.RUnlock()
	return cc.cert, nil
}

// ServerConfig holds everything needed to construct the gRPC server.
type ServerConfig struct {
	CertFile                   string
	KeyFile                    string
	CAFile                     string
	GRPCPort                   string
	Registry                   *registry.Registry
	ConfigStore                *config.ConfigStore
	Queries                    *db.Queries
	RunStore                   db.RunStore
	Interval                   time.Duration
	OfflineThreshold           time.Duration
	JitterPercent              int32
	MaxConcurrentCheckIns      int // 0 → unbounded (no load shedding)
	MaxConcurrentReportResults int // 0 → defaultReportResultConcurrency (always on)
	Integrations               *integrations.Dispatcher
	TraitsDir                  string
	SignatureWindow            time.Duration         // 0 → default 5m
	TraitsScannerTTL           time.Duration         // 0 → default 30s
	MaxConcurrentStreams       uint32                // 0 → default 5000
	GRPCReadBuffer             int                   // 0 → gRPC default 32 KiB
	GRPCWriteBuffer            int                   // 0 → gRPC default 32 KiB
	MaxConnectionAge           time.Duration         // 0 → no forced reconnect
	MaxConnectionIdle          time.Duration         // 0 → default 15m
	KeepaliveTime              time.Duration         // 0 → default 30s
	KeepaliveTimeout           time.Duration         // 0 → default 10s
	StreamSendBuffer           int                   // 0 → default 8
	SpannerAdmin               *spanner.SpannerAdmin // non-nil in a spanner (mode != standalone); drives peer-equal admin reads
	// SMTPNotifier removed — events route through Integrations dispatcher
	LicenseEnforcer *license.Enforcer
	TaskStore       *task.Store
	FleetIndex      *fleet.FleetIndex       // in-memory index for hot-path operations
	RunBatcher      *fleet.RunBatcher       // async run persistence from delta events
	BundleSigner    *BundleSigner           // signs policy bundles (nil = no signing)
	BundleMaxAge    uint32                  // max_age_seconds for signed bundles (0 = forever)
	RateLimit       config.RateLimitSection // rate limiting config
	FallbackServers []string                // peer server addresses returned to agents on registration
	AuditWriter     auditRecorder           // tamper-evident audit writer (nil-safe)
	// ServerVersions is a per-arch map ("<goos>/<goarch>" → version) of the
	// agent binary versions available for distribution. Auto-update is
	// triggered only when an agent reports a version that differs from the
	// version known for THAT agent's arch. Cross-arch comparison (the bug
	// that caused the raspberrypi update loop) is no longer possible — an
	// arm64 agent is now compared against the arm64 binary's version, not
	// the server-arch binary's version.
	ServerVersions    map[string]string
	AgentBinAvailable func() bool      // if set, version-mismatch auto-update only fires when at least one binary exists
	GlobalObserveOnly bool             // global observe-only mode from server.yaml checkin.observe_only
	Metrics           *metrics.Metrics // per-instance prometheus collectors
	// TicketsStore, if non-nil, persists TLS 1.3 session-ticket
	// encryption keys across server restarts so envoys can resume PSK
	// sessions instead of full-handshaking on every deploy. nil → Go's
	// default per-process key (correct but expensive on restart).
	TicketsStore *tickets.Store
}

// NewServer creates a gRPC server configured with mTLS (SEC-01).
// TLS 1.3 minimum, client certificate required and verified against CA.
// Returns the server, a CertCache for TLS hot reload, and a
// TraitsBatcher whose Start method should be called in a goroutine.
func NewServer(cfg ServerConfig) (*grpc.Server, *CertCache, *db.TraitsBatcher, *AgentService, *AdminService, error) {
	tlsCfg, certCache, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("building TLS config: %w", err)
	}
	if cfg.TicketsStore != nil {
		cfg.TicketsStore.Apply(tlsCfg)
	}

	creds := newCountingCreds(credentials.NewTLS(tlsCfg), cfg.Metrics)

	traitsScannerTTL := cfg.TraitsScannerTTL
	if traitsScannerTTL == 0 {
		traitsScannerTTL = 30 * time.Second
	}

	traitsBatch := db.NewTraitsBatcher(cfg.Queries)
	if cfg.FleetIndex != nil {
		fi := cfg.FleetIndex
		traitsBatch.SetFlushHook(func(batch map[string]string) {
			for envoyID, traitsJSON := range batch {
				var parsed map[string]any
				if err := json.Unmarshal([]byte(traitsJSON), &parsed); err == nil {
					flat := make(map[string]string)
					fleet.FlattenJSON("", parsed, flat)
					fi.UpdateTraits(envoyID, flat)
				}
			}
		})
	}
	maxStreams := cfg.MaxConcurrentStreams
	if maxStreams == 0 {
		maxStreams = 5000
	}
	maxConnIdle := cfg.MaxConnectionIdle
	if maxConnIdle == 0 {
		maxConnIdle = 15 * time.Minute
	}
	kaTime := cfg.KeepaliveTime
	if kaTime == 0 {
		kaTime = 30 * time.Second
	}
	kaTimeout := cfg.KeepaliveTimeout
	if kaTimeout == 0 {
		kaTimeout = 10 * time.Second
	}

	sigInterceptor := SignatureVerifyInterceptor(cfg.Queries, cfg.SignatureWindow, cfg.FleetIndex, cfg.Metrics)

	var interceptors []grpc.UnaryServerInterceptor
	if cfg.RateLimit.Enabled {
		interceptors = append(interceptors, RateLimitInterceptor(cfg.RateLimit))
	}
	interceptors = append(interceptors, sigInterceptor)

	opts := []grpc.ServerOption{
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(interceptors...),
		grpc.MaxConcurrentStreams(maxStreams),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: maxConnIdle,
			MaxConnectionAge:  cfg.MaxConnectionAge, // 0 = no forced reconnect
			Time:              kaTime,
			Timeout:           kaTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	// Explicit message size limits (SEC-13). Default Go gRPC is 4 MiB
	// which is too small for large policy bundles. Set 8 MiB as an explicit
	// upper bound to prevent unbounded allocation attacks.
	opts = append(opts,
		grpc.MaxRecvMsgSize(8*1024*1024),
		grpc.MaxSendMsgSize(8*1024*1024),
	)
	if cfg.GRPCReadBuffer > 0 {
		opts = append(opts, grpc.ReadBufferSize(cfg.GRPCReadBuffer))
	}
	if cfg.GRPCWriteBuffer > 0 {
		opts = append(opts, grpc.WriteBufferSize(cfg.GRPCWriteBuffer))
	}

	srv := grpc.NewServer(opts...)

	reassignStore := NewReassignStore()

	agentSvc := &AgentService{
		registry:          cfg.Registry,
		configStore:       cfg.ConfigStore,
		queries:           cfg.Queries,
		runStore:          cfg.RunStore,
		integrations:      cfg.Integrations,
		traitsScanner:     traits.NewScanner(cfg.TraitsDir, traitsScannerTTL),
		traitsBatch:       traitsBatch,
		policyCache:       newPolicyCache(),
		streamMgr:         newStreamManager(cfg.StreamSendBuffer, cfg.FleetIndex, cfg.Metrics, 12*time.Minute),
		taskRouter:        newTaskRouter(cfg.Queries, cfg.Metrics),
		queryRouter:       newQueryRouter(),
		taskStore:         cfg.TaskStore,
		licenseEnforcer:   cfg.LicenseEnforcer,
		fleetIndex:        cfg.FleetIndex,
		runBatcher:        cfg.RunBatcher,
		reassignStore:     reassignStore,
		bundleSigner:      cfg.BundleSigner,
		bundleMaxAge:      cfg.BundleMaxAge,
		baseInterval:      cfg.Interval,
		offlineThreshold:  cfg.OfflineThreshold,
		jitterPercent:     cfg.JitterPercent,
		fallbackServers:   cfg.FallbackServers,
		auditWriter:       cfg.AuditWriter,
		serverVersions:    cfg.ServerVersions,
		agentBinAvailable: cfg.AgentBinAvailable,
		globalObserveOnly: cfg.GlobalObserveOnly,
		metrics:           cfg.Metrics,
	}
	if cfg.MaxConcurrentCheckIns > 0 {
		agentSvc.checkInSem = make(chan struct{}, cfg.MaxConcurrentCheckIns)
	}
	// ReportResult is always admission-capped — a queue-depth bound in front of
	// the single SQLite writer. The cap is tunable (checkin.report_max_concurrent)
	// but never off: 0/unset resolves to defaultReportResultConcurrency.
	reportCap := cfg.MaxConcurrentReportResults
	if reportCap <= 0 {
		reportCap = defaultReportResultConcurrency
	}
	agentSvc.reportResultSem = make(chan struct{}, reportCap)
	if cfg.BundleSigner != nil {
		if pem, err := cfg.BundleSigner.PublicKeyPEM(); err == nil {
			agentSvc.bundlePubKeyPEM = pem
		}
	}
	pb.RegisterVigoAgentServer(srv, agentSvc)

	adminSvc := &AdminService{
		registry:         cfg.Registry,
		queries:          cfg.Queries,
		runStore:         cfg.RunStore,
		configStore:      cfg.ConfigStore,
		metrics:          cfg.Metrics,
		baseInterval:     cfg.Interval,
		offlineThreshold: cfg.OfflineThreshold,
		spannerAdmin:     cfg.SpannerAdmin,
		fleetIndex:       cfg.FleetIndex,
		reassignStore:    reassignStore,
		auditWriter:      cfg.AuditWriter,
		integrations:     cfg.Integrations,
	}
	pb.RegisterVigoAdminServer(srv, adminSvc)

	return srv, certCache, traitsBatch, agentSvc, adminSvc, nil
}

// buildTLSConfig creates a tls.Config requiring client certificates verified
// against the CA. Minimum TLS version is 1.3. Uses GetCertificate callback
// via CertCache for hot-reload support (SECR-10).
func buildTLSConfig(certFile, keyFile, caFile string) (*tls.Config, *CertCache, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("loading server cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, nil, fmt.Errorf("failed to add CA cert to pool")
	}

	cache := NewCertCache(&cert)

	return &tls.Config{
		GetCertificate: cache.GetCertificate,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		ClientCAs:      caPool,
		MinVersion:     tls.VersionTLS13,
	}, cache, nil
}
