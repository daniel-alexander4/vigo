// Package api provides the grpc-gateway REST proxy for the IsgovAdmin service
// and mounts the web UI routes.
package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/db"
	"github.com/isogov/isogov/ui"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewGateway creates an HTTP mux that proxies REST requests to the gRPC server
// at the given address using mTLS credentials. It registers the IsgovAdmin
// service handlers on /api/ paths and mounts the web UI on /.
func NewGateway(ctx context.Context, grpcAddr, certFile, keyFile, caFile string, queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, serverAddr, agentDir, version string) (*http.ServeMux, error) {
	// Build TLS credentials for the gateway's connection to gRPC.
	tlsCreds, err := buildGatewayTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building gateway TLS: %w", err)
	}

	gwMux := runtime.NewServeMux()

	opts := []grpc.DialOption{grpc.WithTransportCredentials(tlsCreds)}

	if err := pb.RegisterIsgovAdminHandlerFromEndpoint(ctx, gwMux, grpcAddr, opts); err != nil {
		return nil, fmt.Errorf("registering admin gateway: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	// Mount UI routes.
	uiHandler, err := ui.NewHandler(queries, configStore, baseInterval, serverAddr, caFile, agentDir, version)
	if err != nil {
		return nil, fmt.Errorf("creating UI handler: %w", err)
	}
	uiHandler.RegisterRoutes(mux)

	// grpc-gateway catches /api/ paths (gvctl CLI uses /api/v1/*).
	mux.Handle("/api/", gwMux)

	return mux, nil
}

// buildGatewayTLS creates TLS credentials for the gateway's connection back
// to the gRPC server. The gateway acts as a client to gRPC, so it needs
// a client certificate and the CA to verify the server.
func buildGatewayTLS(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading gateway cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to add CA cert to pool")
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}

	return credentials.NewTLS(tlsCfg), nil
}
