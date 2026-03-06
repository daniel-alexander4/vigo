// Package api provides the grpc-gateway REST proxy for the IsgovAdmin service.
package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	pb "github.com/isogov/isogov/gen/proto/isogov"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewGateway creates an HTTP mux that proxies REST requests to the gRPC server
// at the given address using mTLS credentials. It registers the IsgovAdmin
// service handlers on /api/v1/ paths.
func NewGateway(ctx context.Context, grpcAddr, certFile, keyFile, caFile string) (*http.ServeMux, error) {
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
	mux.Handle("/", gwMux)

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
