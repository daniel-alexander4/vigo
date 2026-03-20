// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package grpc provides the mTLS gRPC server for Vigo, implementing
// both VigoAgent and VigoAdmin service handlers.
package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/spanner"
	"github.com/daniel-alexander4/vigo/server/fleet"
	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/daniel-alexander4/vigo/server/registry"
	vigosmtp "github.com/daniel-alexander4/vigo/server/smtp"
	"github.com/daniel-alexander4/vigo/server/push"
	"github.com/daniel-alexander4/vigo/server/traits"
	"github.com/daniel-alexander4/vigo/server/webhook"
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
	CertFile        string
	KeyFile         string
	CAFile          string
	GRPCPort        string
	Registry        *registry.Registry
	ConfigStore     *config.ConfigStore
	Queries         *db.Queries
	RunStore        db.RunStore
	Interval        time.Duration
	JitterPercent   int32
	Dispatcher      *webhook.Dispatcher
	TraitsDir            string
	SignatureWindow      time.Duration // 0 → default 5m
	TraitsScannerTTL     time.Duration // 0 → default 30s
	MaxConcurrentStreams uint32        // 0 → default 5000
	GRPCReadBuffer      int           // 0 → gRPC default 32 KiB
	GRPCWriteBuffer     int           // 0 → gRPC default 32 KiB
	MaxConnectionAge    time.Duration // 0 → no forced reconnect
	MaxConnectionIdle   time.Duration // 0 → default 15m
	KeepaliveTime       time.Duration // 0 → default 30s
	KeepaliveTimeout    time.Duration // 0 → default 10s
	StreamSendBuffer    int           // 0 → default 8
	Hub              *spanner.Hub // non-nil when role=hub
	SMTPNotifier     *vigosmtp.Notifier
	LicenseEnforcer  *license.Enforcer
	PushStore        *push.Store
	FleetIndex       *fleet.FleetIndex  // in-memory index for hot-path operations
	RunBatcher       *fleet.RunBatcher  // async run persistence from delta events
	BundleSigner     *BundleSigner     // signs policy bundles (nil = no signing)
	BundleMaxAge     uint32            // max_age_seconds for signed bundles (0 = forever)
	RateLimit        config.RateLimitSection // rate limiting config
	FallbackServers  []string          // peer server addresses returned to agents on registration
	AuditWriter        auditRecorder     // tamper-evident audit writer (nil-safe)
	ServerVersion      string            // server binary version — agents with different version get force_update
	AgentBinAvailable  func() bool // if set, version-mismatch auto-update only fires when at least one binary exists
}

// NewServer creates a gRPC server configured with mTLS (SEC-01).
// TLS 1.3 minimum, client certificate required and verified against CA.
// Returns the server, a CertCache for TLS hot reload, a TraitsBatcher,
// and a TraitsPruner whose Start methods should be called in goroutines.
func NewServer(cfg ServerConfig) (*grpc.Server, *CertCache, *db.TraitsBatcher, *db.TraitsPruner, *AgentService, error) {
	tlsCfg, certCache, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("building TLS config: %w", err)
	}

	creds := credentials.NewTLS(tlsCfg)

	traitsScannerTTL := cfg.TraitsScannerTTL
	if traitsScannerTTL == 0 {
		traitsScannerTTL = 30 * time.Second
	}

	traitsBatch := db.NewTraitsBatcher(cfg.Queries)
	traitsPruner := db.NewTraitsPruner(cfg.Queries)

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

	sigInterceptor := SignatureVerifyInterceptor(cfg.Queries, cfg.SignatureWindow, cfg.FleetIndex)

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
		registry:      cfg.Registry,
		configStore:   cfg.ConfigStore,
		queries:       cfg.Queries,
		runStore:      cfg.RunStore,
		dispatcher:    cfg.Dispatcher,
		smtpNotifier:  cfg.SMTPNotifier,
		traitsScanner: traits.NewScanner(cfg.TraitsDir, traitsScannerTTL),
		traitsBatch:   traitsBatch,
		traitsPruner:  traitsPruner,
		policyCache:   newPolicyCache(),
		streamMgr:        newStreamManager(cfg.StreamSendBuffer, cfg.FleetIndex, 12*time.Minute),
		pushRouter:       newPushRouter(cfg.Queries),
		queryRouter:      newQueryRouter(),
		pushStore:        cfg.PushStore,
		hub:              cfg.Hub,
		licenseEnforcer:  cfg.LicenseEnforcer,
		fleetIndex:       cfg.FleetIndex,
		runBatcher:       cfg.RunBatcher,
		reassignStore:    reassignStore,
		bundleSigner:     cfg.BundleSigner,
		bundleMaxAge:     cfg.BundleMaxAge,
		baseInterval:     cfg.Interval,
		jitterPercent:    cfg.JitterPercent,
		fallbackServers:  cfg.FallbackServers,
		auditWriter:        cfg.AuditWriter,
		serverVersion:      cfg.ServerVersion,
		agentBinAvailable:  cfg.AgentBinAvailable,
	}
	if cfg.BundleSigner != nil {
		if pem, err := cfg.BundleSigner.PublicKeyPEM(); err == nil {
			agentSvc.bundlePubKeyPEM = pem
		}
	}
	pb.RegisterVigoAgentServer(srv, agentSvc)

	adminSvc := &AdminService{
		registry:      cfg.Registry,
		queries:       cfg.Queries,
		runStore:      cfg.RunStore,
		configStore:   cfg.ConfigStore,
		baseInterval:  cfg.Interval,
		hub:           cfg.Hub,
		fleetIndex:    cfg.FleetIndex,
		reassignStore: reassignStore,
		auditWriter:   cfg.AuditWriter,
	}
	pb.RegisterVigoAdminServer(srv, adminSvc)

	return srv, certCache, traitsBatch, traitsPruner, agentSvc, nil
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
