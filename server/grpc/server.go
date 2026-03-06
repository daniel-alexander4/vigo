// Package grpc provides the mTLS gRPC server for Isogov, implementing
// both IsgovAgent and IsgovAdmin service handlers.
package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/db"
	"github.com/isogov/isogov/server/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ServerConfig holds everything needed to construct the gRPC server.
type ServerConfig struct {
	CertFile    string
	KeyFile     string
	CAFile      string
	GRPCPort    string
	Registry    *registry.Registry
	ConfigStore *config.ConfigStore
	Queries     *db.Queries
}

// NewServer creates a gRPC server configured with mTLS (SEC-01).
// TLS 1.3 minimum, client certificate required and verified against CA.
func NewServer(cfg ServerConfig) (*grpc.Server, error) {
	tlsCfg, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("building TLS config: %w", err)
	}

	creds := credentials.NewTLS(tlsCfg)

	srv := grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(SignatureVerifyInterceptor(cfg.Queries)),
	)

	agentSvc := &AgentService{
		registry:    cfg.Registry,
		configStore: cfg.ConfigStore,
		queries:     cfg.Queries,
	}
	pb.RegisterIsgovAgentServer(srv, agentSvc)

	adminSvc := &AdminService{
		registry: cfg.Registry,
		queries:  cfg.Queries,
	}
	pb.RegisterIsgovAdminServer(srv, adminSvc)

	return srv, nil
}

// buildTLSConfig creates a tls.Config requiring client certificates verified
// against the CA. Minimum TLS version is 1.3.
func buildTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading server cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
