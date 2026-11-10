// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package api provides the grpc-gateway REST proxy for the VigoAdmin service
// and mounts the web UI routes.
package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/bootstrap"
	"git.alexander4.io/vigo/server/compliancedocs"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/publish"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/ui"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// NewGateway creates an HTTP mux that proxies REST requests to the gRPC server
// at grpcAddr. grpcAddr is the local Unix-domain-socket endpoint
// (unix:///…/admin.sock) the server also listens on; the gateway is the
// operator/REST entry point and is already token+RBAC-authed at the REST
// layer, so it reaches VigoAdmin over the local socket rather than the
// network port (which requires a signed control-plane call). The socket is
// 0600 and local-only, so the dial uses insecure transport credentials —
// no TLS on the loopback hop. It registers the VigoAdmin service handlers on
// /api/ paths and mounts the web UI on /.
//
// reg is the per-instance prometheus registry whose collectors the /metrics
// endpoint exposes; m is the same registry's collector set, threaded into
// subhandlers (bootstrap, etc.) that emit metrics.
func NewGateway(ctx context.Context, grpcAddr, caFile string, queries *db.Queries, configStore *config.ConfigStore, baseInterval, offlineThreshold time.Duration, serverAddr, agentDir, version, docsDir string, assistant *vigoai.Assistant, spannerAdmin *spanner.SpannerAdmin, runStore db.RunStore, fleetIndex *fleet.FleetIndex, authMethod string, publishCfg config.PublishSection, brandingCfg config.BrandingSection, sandgorgonEnabled bool, peerSvc *peer.Service, aw auditRecorder, complianceDocsStore *compliancedocs.Store, liveDir, historyDir string, reg *prometheus.Registry, m *metrics.Metrics, gwOpts ...GatewayOption) (*http.ServeMux, *ui.Handler, *ConfigHandler, error) {
	gwMux := runtime.NewServeMux()

	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	if err := pb.RegisterVigoAdminHandlerFromEndpoint(ctx, gwMux, grpcAddr, dialOpts); err != nil {
		return nil, nil, nil, fmt.Errorf("registering admin gateway: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// Mount UI routes. The FleetIndex is the UI data source — O(1)
	// in-memory fleet queries; DB only for historical data.
	customDS := fleet.NewDataSource(fleetIndex, queries, runStore, configStore)
	uiHandler, err := ui.NewHandler(queries, configStore, baseInterval, offlineThreshold, serverAddr, caFile, agentDir, version, assistant, spannerAdmin, customDS, docsDir, authMethod)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("creating UI handler: %w", err)
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
	// Spanner-mode stacks distribution is gitback-driven (ADR-025
	// Slice 4).
	configHandler := NewConfigHandler(queries, configStore, fleetIndex, liveDir, historyDir)
	configHandler.SetAuditWriter(aw)
	configHandler.SetMetrics(m)
	if assistant != nil {
		configHandler.SetAI(assistant, config.AISection{Enabled: true})
	}
	if publishCfg.ConvergenceThreshold > 0 && fleetIndex != nil {
		monitor := publish.NewConvergenceMonitor(fleetIndex, func() error {
			return configStore.Reload()
		}, liveDir, historyDir)
		configHandler.SetConvergenceMonitor(monitor, publishCfg)
	}
	configHandler.RegisterRoutes(mux)

	// Mount envoy management endpoints (tags, maintenance).
	envoysHandler := NewEnvoysHandler(queries, fleetIndex)
	envoysHandler.SetAuditWriter(aw)
	envoysHandler.RegisterRoutes(mux)

	// Mount audit endpoint.
	auditHandler := NewAuditHandler(queries)
	auditHandler.SetAuditWriter(aw)
	auditHandler.RegisterRoutes(mux)

	// Mount preview endpoint.
	previewHandler := NewPreviewHandler(configStore, fleetIndex)
	previewHandler.RegisterRoutes(mux)

	// Mount risk scoring endpoints.
	riskHandler := NewRiskHandler(fleetIndex, queries, baseInterval, offlineThreshold, version)
	riskHandler.SetBranding(brandingCfg)
	riskHandler.RegisterRoutes(mux)

	// Mount sandgorgon (bare metal lifecycle) endpoints — gated behind
	// sandgorgon.enabled (default false). The subsystem is deferred post-v1
	// and exposes destructive BMC power/disk-wipe operations against
	// plaintext-stored BMC credentials, so the routes stay unmounted unless
	// an operator explicitly opts in.
	if sandgorgonEnabled {
		sandgorgonHandler := NewSandgorgonHandler(queries, fleetIndex, "", serverAddr)
		sandgorgonHandler.SetAuditWriter(aw)
		sandgorgonHandler.RegisterRoutes(mux)
	}

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

	// Mount the puddle fleet aggregator (ADR-014). It owns the
	// friendly-name map, so build it first and share its aggregator with
	// the gitback handler below — one instance, one cache, one source of
	// truth for names. The `/puddle` UI page reads the cached
	// aggregation directly.
	puddleHandler := NewPuddleHandler(fleetIndex, queries)
	puddleHandler.RegisterRoutes(mux)
	uiHandler.SetPuddleFleet(puddleHandler)

	// Mount gitback fleet aggregator. Share the handler with the
	// UI so the `/gitback` page reads the cached aggregation
	// directly rather than round-tripping through the REST endpoint.
	// Pass the puddle aggregator so project rows can carry the founder
	// puddle's friendly name (the name map lives in puddlemesh).
	gitbackHandler := NewGitbackHandler(fleetIndex, queries, puddleHandler.Aggregator())
	gitbackHandler.RegisterRoutes(mux)
	uiHandler.SetGitbackFleet(gitbackHandler)

	// Mount longdrawer + lockbox mesh observability endpoints. Same
	// handler-sharing pattern — the `/mesh` UI page reads the
	// cached aggregators directly. Admin-only RBAC is enforced
	// inside RegisterRoutes; the UI handler adds its own check.
	meshHandler := NewMeshHandler(fleetIndex, queries)
	meshHandler.RegisterRoutes(mux)
	uiHandler.SetMesh(meshHandler)

	// Mount bootstrap and agent distribution routes.
	bh := bootstrap.NewHandler(serverAddr, caFile, agentDir, version, m)
	bh.SetFleetIndex(fleetIndex)
	for _, opt := range gwOpts {
		opt(bh)
	}
	bh.RegisterRoutes(mux)

	// Feed the per-(os,arch) expected-agent-version resolver into the UI
	// data source so the Agent Version surface can flag envoys that aren't
	// on the version the server would auto-update them to. Mirrors the
	// auto-update path's per-arch lookup (server/grpc); "" when no binary
	// is published for an envoy's arch.
	customDS.SetExpectedAgentVersionFunc(func(family, arch string) string {
		goos := bootstrap.GoosForFamily(family)
		if goos == "" || arch == "" {
			return ""
		}
		return bh.AgentVersionForArch(goos, arch)
	})

	// grpc-gateway catches /api/ paths (vigocli uses /api/v1/*).
	mux.Handle("/api/", gwMux)

	return mux, uiHandler, configHandler, nil
}

// GatewayOption configures optional features on the bootstrap handler.
type GatewayOption func(*bootstrap.Handler)

const maxRequestBodySize = 1 << 20 // 1 MB

// MaxBodyMiddleware wraps an http.Handler to limit request body sizes to 1 MB.
// This prevents unbounded memory consumption from oversized POST bodies.
func MaxBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The scrier WebSocket relay (GET /ws/scrier/{id}) is the only
		// upgrade route; it is exempted by method + path, never by the
		// client-controlled Upgrade header (which would exempt every route).
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ws/scrier/") {
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
