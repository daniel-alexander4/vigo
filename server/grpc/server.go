// Package grpc provides the mTLS gRPC server for Hobnob, implementing
// both HobnobAgent and HobnobAdmin service handlers.
package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/traits"
	"github.com/hobnob/hobnob/server/webhook"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	BaseInterval    time.Duration
	Dispatcher      *webhook.Dispatcher
	TraitsDir        string
	SignatureWindow  time.Duration // 0 → default 5m
	NodeCacheTTL     time.Duration // 0 → default 60s
	TraitsScannerTTL time.Duration // 0 → default 30s
	Hub              *federation.Hub // non-nil when role=hub
}

// NewServer creates a gRPC server configured with mTLS (SEC-01).
// TLS 1.3 minimum, client certificate required and verified against CA.
// Returns the server, a CertCache for TLS hot reload, a LastSeenBatcher,
// and a TraitsPruner whose Start methods should be called in goroutines.
func NewServer(cfg ServerConfig) (*grpc.Server, *CertCache, *db.LastSeenBatcher, *db.TraitsPruner, error) {
	tlsCfg, certCache, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("building TLS config: %w", err)
	}

	creds := credentials.NewTLS(tlsCfg)

	nodeCacheTTL := cfg.NodeCacheTTL
	if nodeCacheTTL == 0 {
		nodeCacheTTL = 60 * time.Second
	}
	traitsScannerTTL := cfg.TraitsScannerTTL
	if traitsScannerTTL == 0 {
		traitsScannerTTL = 30 * time.Second
	}

	nodeCache := db.NewNodeCache(cfg.Queries, nodeCacheTTL)
	lastSeenBatch := db.NewLastSeenBatcher(cfg.Queries)
	traitsPruner := db.NewTraitsPruner(cfg.Queries)

	srv := grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(SignatureVerifyInterceptor(cfg.Queries, nodeCache, cfg.SignatureWindow)),
	)

	agentSvc := &AgentService{
		registry:      cfg.Registry,
		configStore:   cfg.ConfigStore,
		queries:       cfg.Queries,
		runStore:      cfg.RunStore,
		dispatcher:    cfg.Dispatcher,
		traitsScanner: traits.NewScanner(cfg.TraitsDir, traitsScannerTTL),
		nodeCache:     nodeCache,
		lastSeenBatch: lastSeenBatch,
		traitsPruner:  traitsPruner,
		policyCache:   newPolicyCache(),
		streamMgr:     newStreamManager(),
		hub:           cfg.Hub,
	}
	pb.RegisterHobnobAgentServer(srv, agentSvc)

	adminSvc := &AdminService{
		registry:     cfg.Registry,
		queries:      cfg.Queries,
		runStore:     cfg.RunStore,
		configStore:  cfg.ConfigStore,
		baseInterval: cfg.BaseInterval,
		hub:          cfg.Hub,
	}
	pb.RegisterHobnobAdminServer(srv, adminSvc)

	return srv, certCache, lastSeenBatch, traitsPruner, nil
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
