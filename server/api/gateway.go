// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package api provides the grpc-gateway REST proxy for the VigoAdmin service
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
	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/bootstrap"
	"git.alexander4.io/vigo/server/compliancedocs"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/tungol"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/publish"
	"git.alexander4.io/vigo/ui"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewGateway creates an HTTP mux that proxies REST requests to the gRPC server
// at the given address using mTLS credentials. It registers the VigoAdmin
// service handlers on /api/ paths and mounts the web UI on /.
func NewGateway(ctx context.Context, grpcAddr, certFile, keyFile, caFile string, queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, serverAddr, agentDir, version, docsDir string, assistant *vigoai.Assistant, hub *tungol.Hub, runStore db.RunStore, fleetIndex *fleet.FleetIndex, authMethod string, enforcer *license.Enforcer, publishCfg config.PublishSection, brandingCfg config.BrandingSection, peerSvc *peer.Service, aw auditRecorder, complianceDocsStore *compliancedocs.Store, gwOpts ...GatewayOption) (*http.ServeMux, *ui.Handler, error) {
	// Build TLS credentials for the gateway's connection to gRPC.
	tlsCreds, err := buildGatewayTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, nil, fmt.Errorf("building gateway TLS: %w", err)
	}

	gwMux := runtime.NewServeMux()

	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(tlsCreds)}

	if err := pb.RegisterVigoAdminHandlerFromEndpoint(ctx, gwMux, grpcAddr, dialOpts); err != nil {
		return nil, nil, fmt.Errorf("registering admin gateway: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	// Mount UI routes.
	// When FleetIndex is available, use it as the data source for O(1) fleet queries.
	var customDS ui.DataSource
	if fleetIndex != nil {
		customDS = fleet.NewDataSource(fleetIndex, queries, runStore, configStore)
	}
	uiHandler, err := ui.NewHandler(queries, configStore, baseInterval, serverAddr, caFile, agentDir, version, assistant, hub, customDS, docsDir, authMethod, enforcer)
	if err != nil {
		return nil, nil, fmt.Errorf("creating UI handler: %w", err)
	}
	if peerSvc != nil {
		uiHandler.SetPeerService(peerSvc)
	}
	uiHandler.RegisterRoutes(mux)

	// Mount traits and inventory endpoints (direct handlers, not proxied through grpc-gateway).
	traitsHandler := NewTraitsHandler(queries)
	traitsHandler.RegisterRoutes(mux)
	inventoryHandler := NewInventoryHandler(queries)
	inventoryHandler.RegisterRoutes(mux)

	// Mount config trace endpoint.
	configTraceHandler := NewConfigTraceHandler(configStore, fleetIndex)
	configTraceHandler.RegisterRoutes(mux)

	// Mount config publish history, versioning, rollback, and reload.
	// Pass hub as configSyncer only if non-nil (nil *Hub wrapped in interface is non-nil).
	var cfgSyncer configSyncer
	if hub != nil {
		cfgSyncer = hub
	}
	configHandler := NewConfigHandler(queries, configStore, fleetIndex, cfgSyncer)
	configHandler.SetAuditWriter(aw)
	if assistant != nil {
		configHandler.SetAI(assistant, config.AISection{Enabled: true})
	}
	if publishCfg.ConvergenceThreshold > 0 && fleetIndex != nil {
		monitor := publish.NewConvergenceMonitor(fleetIndex, func() error {
			return configStore.Reload()
		})
		configHandler.SetConvergenceMonitor(monitor, publishCfg)
	}
	configHandler.RegisterRoutes(mux)

	// Mount envoy management endpoints (tags, maintenance).
	envoysHandler := NewEnvoysHandler(queries, fleetIndex)
	envoysHandler.RegisterRoutes(mux)

	// Mount audit endpoint.
	auditHandler := NewAuditHandler(queries)
	auditHandler.SetAuditWriter(aw)
	auditHandler.RegisterRoutes(mux)

	// Mount preview endpoint.
	previewHandler := NewPreviewHandler(configStore, fleetIndex)
	previewHandler.RegisterRoutes(mux)

	// Mount risk scoring endpoints.
	riskHandler := NewRiskHandler(fleetIndex, queries, baseInterval, version)
	riskHandler.SetBranding(brandingCfg)
	riskHandler.RegisterRoutes(mux)

	// Mount sandgorgon (bare metal lifecycle) endpoints.
	sandgorgonHandler := NewSandgorgonHandler(queries, fleetIndex, "", serverAddr)
	sandgorgonHandler.RegisterRoutes(mux)

	// Mount compliance docs (operator-uploaded artifacts for
	// documented-scope controls). Always mount the routes — the
	// handler 503s when the store isn't configured, so the API
	// surface remains discoverable but inert without `compliance.docs`
	// in server.yaml.
	complianceDocsHandler := NewComplianceDocsHandler(complianceDocsStore)
	complianceDocsHandler.SetAuditWriter(aw)
	complianceDocsHandler.RegisterRoutes(mux)
	if complianceDocsStore != nil {
		uiHandler.SetComplianceDocs(&complianceDocsListing{store: complianceDocsStore})
	}

	// Mount gitback fleet aggregator. Share the handler with the
	// UI so the `/gitback` page reads the cached aggregation
	// directly rather than round-tripping through the REST endpoint.
	gitbackHandler := NewGitbackHandler(fleetIndex, queries)
	gitbackHandler.RegisterRoutes(mux)
	uiHandler.SetGitbackFleet(gitbackHandler)

	// Mount longdrawer + lockbox mesh observability endpoints. Same
	// handler-sharing pattern — the `/mesh` UI page reads the
	// cached aggregators directly. Admin-only RBAC is enforced
	// inside RegisterRoutes; the UI handler adds its own check.
	meshHandler := NewMeshHandler(fleetIndex, queries, authMethod)
	meshHandler.RegisterRoutes(mux)
	uiHandler.SetMesh(meshHandler)

	// Mount bootstrap and agent distribution routes.
	bh := bootstrap.NewHandler(serverAddr, caFile, agentDir, version)
	bh.SetFleetIndex(fleetIndex)
	for _, opt := range gwOpts {
		opt(bh)
	}
	bh.RegisterRoutes(mux)

	// grpc-gateway catches /api/ paths (vigocli uses /api/v1/*).
	mux.Handle("/api/", gwMux)

	return mux, uiHandler, nil
}

// GatewayOption configures optional features on the bootstrap handler.
type GatewayOption func(*bootstrap.Handler)

// NewSpokeGateway creates a lightweight HTTP mux for spoke mode.
// It exposes the admin gRPC-gateway proxy (for vigocli), metrics, and traits
// but omits the web UI and bootstrap routes.
func NewSpokeGateway(ctx context.Context, grpcAddr, certFile, keyFile, caFile string, queries *db.Queries, configStore *config.ConfigStore, baseInterval time.Duration, spokeID string) (*http.ServeMux, error) {
	tlsCreds, err := buildGatewayTLS(certFile, keyFile, caFile)
	if err != nil {
		return nil, fmt.Errorf("building gateway TLS: %w", err)
	}

	gwMux := runtime.NewServeMux()
	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(tlsCreds)}

	if err := pb.RegisterVigoAdminHandlerFromEndpoint(ctx, gwMux, grpcAddr, dialOpts); err != nil {
		return nil, fmt.Errorf("registering admin gateway: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	// Traits and inventory endpoints for admin queries.
	traitsHandler := NewTraitsHandler(queries)
	traitsHandler.RegisterRoutes(mux)
	invHandler := NewInventoryHandler(queries)
	invHandler.RegisterRoutes(mux)

	// Health endpoint for monitoring steorra status.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","steorra_id":%q}`, spokeID)
	})

	// grpc-gateway catches /api/ paths (vigocli uses /api/v1/*).
	mux.Handle("/api/", gwMux)

	return mux, nil
}

const maxRequestBodySize = 1 << 20 // 1 MB

// MaxBodyMiddleware wraps an http.Handler to limit request body sizes to 1 MB.
// This prevents unbounded memory consumption from oversized POST bodies.
func MaxBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip body limiting for WebSocket upgrade requests — MaxBytesReader
		// wraps the ResponseWriter which strips the Hijacker interface
		// needed by the WebSocket upgrade.
		if r.Header.Get("Upgrade") == "websocket" {
			next.ServeHTTP(w, r)
			return
		}
		// Skip body limiting for payload distribute — streams to disk
		// via io.Copy and doesn't buffer in memory.
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/swarm/filecast/distribute" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
		}
		next.ServeHTTP(w, r)
	})
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
