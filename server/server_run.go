// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/backup"
	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/task"
	"git.alexander4.io/vigo/server/watcher"
)

// Run starts every background goroutine the server needs and blocks
// until ctx is cancelled. Returns nil on clean shutdown, an error if a
// listener cannot bind. Construction lives in NewServer; lifecycle
// lives here. The pair was extracted from main.go::run() during the
// Phase B refactor.
func (s *Server) Run(ctx context.Context) error {
	slog.Info("license loaded",
		"license_id", s.enforcer.GetStatus().License.LicenseID,
		"stage", s.enforcer.GetStatus().Stage.String(),
	)

	s.startLicenseEnforcement(ctx)
	s.startAsyncFlushers(ctx)
	s.startRetentionAndBackup(ctx)
	s.startTicketsRotation(ctx)

	if err := s.startGRPCServer(ctx); err != nil {
		return err
	}

	s.startMetricsAndHealthChecks(ctx)
	s.startConvergenceAlerters(ctx)
	s.startSnapshotters(ctx)
	s.startGRC(ctx)
	s.startNVDEnricher(ctx)
	s.startStacksWatcher(ctx)
	s.startAIWarmup()
	s.startSwarm(ctx)
	s.startHTTPServer()
	s.startS3Server()
	s.startSpannerGossip(ctx)

	slog.Info("server ready",
		"mode", s.mode,
		"grpc", s.cfg.Server.GRPCListen,
		"rest", s.cfg.Server.APIListen,
	)

	<-ctx.Done()
	slog.Info("shutting down...")

	s.grpcSrv.GracefulStop()
	if err := s.httpSrv.Shutdown(context.Background()); err != nil {
		slog.Error("REST gateway shutdown error", "error", err)
	}
	if s.s3Srv != nil {
		if err := s.s3Srv.Shutdown(context.Background()); err != nil {
			slog.Error("curator S3 API shutdown error", "error", err)
		}
	}

	slog.Info("server stopped")
	return nil
}

// startLicenseEnforcement runs the license enforcer's periodic check
// loop. The enforcer was wired with a stop-service callback during
// NewServer that calls s.cancel on terminal license failure.
func (s *Server) startLicenseEnforcement(ctx context.Context) {
	daemon.SafeGo("license-enforcer", func() { s.enforcer.Run(ctx) })

	// Peer replication: gRPC registration migrated into NewServer. The
	// primary-side push loop is managed by setPrimaryPushing, which captures
	// the lifecycle ctx here so a runtime Promote/Demote can start or stop it
	// without a restart.
	if s.peerService != nil {
		s.peerPushMu.Lock()
		s.peerPushCtx = ctx
		s.peerPushMu.Unlock()
		s.peerService.SetRoleChangeHook(s.setPrimaryPushing)
		// Use the Service's effective role (already resolved in NewServer from
		// server.yaml plus any persisted runtime transition), not a fresh read
		// of cfg.Peers.Role — otherwise a restart after a runtime promote would
		// start the pusher off the stale config role.
		s.setPrimaryPushing(s.peerService.Role() == "primary")
	}

}

// setPrimaryPushing starts or stops the outbound peer.Primary push loop to
// match the node's current role: on (Promote / primary at boot) constructs and
// starts the loop; off (Demote) stops it. Idempotent and safe under concurrent
// transitions and shutdown. A no-op when no peers are configured or before the
// lifecycle ctx is captured. The Stop drains via the captured ctx, so it
// returns promptly and does not stall the Demote RPC that triggers it.
func (s *Server) setPrimaryPushing(on bool) {
	if s.peerService == nil {
		return
	}
	s.peerPushMu.Lock()
	defer s.peerPushMu.Unlock()

	if on {
		if s.peerPrimary != nil || s.peerPushCtx == nil {
			return // already pushing, or lifecycle not started yet
		}
		ctx := s.peerPushCtx
		primary := peer.NewPrimary(s.serverRoot, s.cfg.Peers.Servers, s.peerTLS, s.peerSigner())
		s.peerPrimary = primary
		daemon.SafeGo("peer-primary", func() {
			if err := primary.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("peer primary error", "error", err)
			}
		})
		return
	}

	if s.peerPrimary == nil {
		return
	}
	primary := s.peerPrimary
	s.peerPrimary = nil
	primary.Stop()
}

// startAsyncFlushers starts the staggered SQLite write batchers
// (last-seen, run-result, traits) plus the traits-snapshot pruner.
// Offsets avoid SQLite write-lock contention on the default 10s
// interval.
func (s *Server) startAsyncFlushers(ctx context.Context) {
	stateFlusher := fleet.NewStateFlusher(s.fleetIndex, s.queries, s.metrics)
	daemon.SafeGo("state-flusher", func() { stateFlusher.Start(ctx, s.lastSeenFlush, 0) })
	daemon.SafeGo("run-batcher", func() { s.runBatcher.Start(ctx, s.lastSeenFlush, s.lastSeenFlush/3) })
	daemon.SafeGo("traits-batcher", func() { s.traitsBatch.Start(ctx, s.lastSeenFlush, 2*s.lastSeenFlush/3) })
}

// startTicketsRotation rotates TLS session-ticket keys daily on the
// gRPC listener. No-op when the store is nil (non-local secrets
// backend). Rotation persists both keys (active + 1 grace) under the
// master key, and the next handshake picks up the new keys via the
// GetConfigForClient callback wired in tickets.Store.Apply.
func (s *Server) startTicketsRotation(ctx context.Context) {
	if s.ticketsStore == nil {
		return
	}
	daemon.SafeGo("tickets-rotation", func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				daemon.Guard("tickets-rotation", func() {
					if err := s.ticketsStore.Rotate(); err != nil {
						slog.Warn("tls ticket-key rotation failed", "error", err)
						return
					}
					slog.Info("tls ticket-key rotated", "keys", len(s.ticketsStore.Keys()))
				})
			}
		}
	})
}

// startRetentionAndBackup starts the task-run timeout reaper, the
// generic retention pruner, and (SQLite only, when configured) the
// Litestream WAL replicator.
func (s *Server) startRetentionAndBackup(ctx context.Context) {
	taskReaper := task.NewReaper(s.queries)
	daemon.SafeGo("task-reaper", func() { taskReaper.Start(ctx) })

	retention := parseRetention(s.cfg.Database.Retention)
	retentionPruner := db.NewRetentionPruner(s.queries, s.db, retention)
	retentionPruner.SetPostgres(s.isPostgres)
	daemon.SafeGo("retention-pruner", func() { retentionPruner.Start(ctx, 24*time.Hour) })
	slog.Info("retention pruner enabled", "retention", retention, "interval", "24h")

	if !s.isPostgres && s.cfg.Backup.URL != "" {
		replicator := backup.NewReplicator(backup.Config{
			URL:             s.cfg.Backup.URL,
			DBPath:          s.cfg.Database.DSN,
			AccessKeyID:     s.cfg.Backup.AccessKeyID,
			SecretAccessKey: s.cfg.Backup.SecretAccessKey,
			Region:          s.cfg.Backup.Region,
			Endpoint:        s.cfg.Backup.Endpoint,
			ForcePathStyle:  s.cfg.Backup.ForcePathStyle,
			Retention:       s.cfg.Backup.Retention,
			SyncInterval:    s.cfg.Backup.SyncInterval,
		})
		daemon.SafeGo("litestream-replicator", func() {
			if err := replicator.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("litestream replication failed", "error", err)
			}
		})
	}
}

// adminSocketPath is the local Unix-domain socket the grpc-gateway dials to
// reach VigoAdmin. It is per-instance (rooted under serverRoot, alongside
// bolt/ and puddle/) so concurrent servers — including the ephemeral ones in
// the test harness — never collide on a shared global path. The operator
// path rides this socket; the network gRPC port requires a signed
// control-plane call for VigoAdmin/VigoPeer (see the SignatureVerifyInterceptor).
func (s *Server) adminSocketPath() string {
	return filepath.Join(s.serverRoot, "run", "admin.sock")
}

// startGRPCServer binds the gRPC listeners (network TCP + local operator UDS)
// and starts a serve goroutine for each. Returns an error if either listener
// cannot bind.
func (s *Server) startGRPCServer(ctx context.Context) error {
	grpcLis, err := net.Listen("tcp", s.cfg.Server.GRPCListen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.cfg.Server.GRPCListen, err)
	}

	go func() {
		slog.Info("gRPC server starting", "addr", s.cfg.Server.GRPCListen)
		if err := s.grpcSrv.Serve(grpcLis); err != nil {
			slog.Error("gRPC server error", "error", err)
		}
	}()

	// Local operator socket. The grpc-gateway dials this to reach VigoAdmin
	// without presenting a signed control-plane call (it is token+RBAC-authed
	// at the REST layer). 0600 + root-owned: only the local server process can
	// reach it. Remove any stale socket left by an unclean shutdown first.
	sockPath := s.adminSocketPath()
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return fmt.Errorf("creating admin socket dir: %w", err)
	}
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale admin socket: %w", err)
	}
	udsLis, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listening on admin socket %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return fmt.Errorf("chmod admin socket: %w", err)
	}

	go func() {
		slog.Info("gRPC operator socket starting", "path", sockPath)
		if err := s.grpcSrv.Serve(udsLis); err != nil {
			slog.Error("gRPC operator socket error", "error", err)
		}
	}()
	return nil
}

// startMetricsAndHealthChecks starts the Prometheus collector, the
// build-info + uptime tickers, the stale-envoy checker, the
// compliance-waiver expirer, and the secret-rotation watcher.
func (s *Server) startMetricsAndHealthChecks(ctx context.Context) {
	// Set build info metric and start uptime tracker. version is set at
	// package level via -ldflags at build time.
	s.metrics.BuildInfo.WithLabelValues(version).Set(1)
	serverStartTime := time.Now()
	daemon.SafeGo("uptime-tracker", func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			s.metrics.UptimeSeconds.Set(time.Since(serverStartTime).Seconds())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})

	// Start metrics collector in background.
	var fleetSource metrics.FleetSource
	var swarmSource metrics.SwarmSource
	if s.fleetIndex != nil {
		fleetSource = &metricsFleetAdapter{fleetIndex: s.fleetIndex}
		swarmSource = newMetricsSwarmAdapter(s)
	}
	var licenseRefresh func()
	if s.enforcer != nil {
		licenseRefresh = s.enforcer.RefreshMetrics
	}
	metricsCollector := metrics.NewCollector(s.queries, s.db, fleetSource, swarmSource, s.metrics, s.baseInterval, s.offlineThreshold, licenseRefresh)
	daemon.SafeGo("metrics-collector", func() { metricsCollector.Run(ctx, s.metricsInterval) })

	// Start stale envoy checker — fires envoy.stale events on transition.
	daemon.SafeGo("stale-checker", func() { s.runStaleChecker(ctx) })

	// Start secret rotation watcher.
	if s.cfg.Watcher.Enabled {
		if vp, ok := s.provider.(watcher.VersionProvider); ok {
			watchPaths := s.configStore.SecretPaths()
			pollInterval := 5 * time.Minute
			if s.cfg.Watcher.PollInterval != "" {
				if parsed, err := time.ParseDuration(s.cfg.Watcher.PollInterval); err == nil {
					pollInterval = parsed
				}
			}
			w := watcher.NewWatcher(vp, watcher.NewDBAdapter(s.queries), s.configStore, s.integrations, s.metrics, pollInterval, watchPaths)
			w.SetConfigReloader(s.configStore)
			w.SetNotifyFunc(func(event string, details map[string]any) {
				s.integrations.Dispatch(integrations.Event{
					Name:      event,
					Timestamp: time.Now().UTC(),
					Summary:   fmt.Sprintf("watcher event: %s", event),
					Details:   details,
				})
			})
			daemon.SafeGo("secret-watcher", func() {
				if err := w.Run(ctx); err != nil && ctx.Err() == nil {
					slog.Error("secret watcher error", "error", err)
				}
			})
		} else {
			slog.Warn("secret watcher enabled but provider does not support versioning")
		}
	}
}

// startConvergenceAlerters starts the framework-aware drift alerter
// and the compliance-threshold checker. The latter is gated by
// cfg.CheckIn.ComplianceThreshold > 0.
//
// NOTE: the security-posture snapshot goroutine is started inside the
// threshold-gated branch below — that matches the pre-refactor
// behavior in main.go::run() exactly. The gating was almost certainly a
// pre-existing indentation bug (security posture is independent of the
// threshold checker), but the refactor preserves it byte-for-byte and
// flags it as a separate follow-up.
func (s *Server) startConvergenceAlerters(ctx context.Context) {
	// Warm + periodically re-anchor the audit-chain verifier (boot full
	// re-verify after any downtime, then daily). Always on — independent of
	// the compliance threshold below.
	daemon.SafeGo("audit-reverify", func() { s.runAuditReverify(ctx) })

	// Start framework-aware drift alerter (background, not hot path).
	// Scans recent runs every 5 minutes, enriches drift events with framework/control context.
	daemon.SafeGo("framework-drift-alerter", func() { s.runFrameworkDriftAlerter(ctx) })

	// Start compliance threshold checker (threshold from checkin config, default 0 = disabled).
	complianceThreshold := s.cfg.CheckIn.ComplianceThreshold
	if complianceThreshold > 0 {
		daemon.SafeGo("compliance-checker", func() { s.runComplianceChecker(ctx, complianceThreshold) })
		daemon.SafeGo("security-posture-snapshot", func() { s.runSecurityPostureSnapshot(ctx) })
	}
}

// startSnapshotters runs the four daily snapshot loops: convergence,
// risk posture, MTTR stats, and standards-compliance scores. Each takes
// an initial snapshot at startup so the sparklines aren't empty on a
// fresh boot.
func (s *Server) startSnapshotters(ctx context.Context) {
	// Start compliance sparkline snapshotting (daily snapshot for the in-memory ring buffer + DB).
	daemon.SafeGo("convergence-snapshot", func() {
		snapshot := func() {
			s.fleetIndex.SnapshotConvergence()
			s.persistConvergenceSnapshot(ctx)
		}
		// Take an initial snapshot immediately so the sparkline isn't empty on startup.
		daemon.Guard("convergence-snapshot", snapshot)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				daemon.Guard("convergence-snapshot", snapshot)
			}
		}
	})

	// Start daily risk posture snapshotting.
	daemon.SafeGo("risk-snapshot", func() {
		daemon.Guard("risk-snapshot", func() { s.persistRiskSnapshot(ctx) })
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				daemon.Guard("risk-snapshot", func() { s.persistRiskSnapshot(ctx) })
			}
		}
	})

	// Start MTTR stats computation (daily, on same cadence as compliance snapshots).
	daemon.SafeGo("mttr-stats", func() {
		daemon.Guard("mttr-stats", func() { s.computeAndStoreMTTR(ctx) })
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				daemon.Guard("mttr-stats", func() { s.computeAndStoreMTTR(ctx) })
			}
		}
	})

	// Start daily standards compliance snapshotting through the unified scorer.
	daemon.SafeGo("compliance-snapshot", func() {
		daemon.Guard("compliance-snapshot", func() { s.reportHandler.PersistComplianceSnapshot(ctx) })
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				daemon.Guard("compliance-snapshot", func() { s.reportHandler.PersistComplianceSnapshot(ctx) })
			}
		}
	})
}

// startGRC starts the GRC evidence pusher's loop. No-op if no GRC
// integrations are configured.
func (s *Server) startGRC(ctx context.Context) {
	if s.grcPusher != nil {
		daemon.SafeGo("grc-pusher", func() { s.grcPusher.Start(ctx) })
	}
}

// startNVDEnricher starts the NVD CVE enricher's reactor. No-op when
// cfg.Risk.NVD.Enabled is false. The enricher feeds the AI assistant's
// CVE on-demand lookup path as well.
func (s *Server) startNVDEnricher(ctx context.Context) {
	if s.nvdEnricher == nil {
		return
	}
	daemon.SafeGo("nvd-enricher", func() {
		s.nvdEnricher.Run(ctx, func() []string {
			return collectFleetCVEIDs(s.fleetIndex)
		})
	})
}

// startStacksWatcher runs the ADR-025 Slice 3 stacks-in-gitback
// watcher. Nil when the stacks project hasn't been founded yet
// (vigocli spanner stacks init) — a future SIGHUP / restart picks it
// up.
func (s *Server) startStacksWatcher(ctx context.Context) {
	if s.stacksWatcher == nil {
		return
	}
	w := s.stacksWatcher
	daemon.SafeGo("stacks-watcher", func() {
		if err := w.Run(ctx); err != nil {
			slog.Warn("stacks watcher exited", "error", err)
		}
	})
}

// startAIWarmup pre-warms the Ollama model so the first user request
// doesn't hit a 60-90 second cold-load delay. No-op for non-Ollama
// providers.
func (s *Server) startAIWarmup() {
	if ollamaP, ok := s.aiProvider.(*vigoai.OllamaProvider); ok {
		daemon.SafeGo("ai-warmup", func() { ollamaP.WarmModel(s.cfg.AI.Model) })
	}
}

// startSwarm runs the swarm content-substrate reactors: the curator
// catalog feed (rebuilds every 5 min) and the swarm retention pruner
// (daily). No-op when the local hostname isn't in the swarm-enabled
// pattern set.
func (s *Server) startSwarm(ctx context.Context) {
	if !s.swarmEnabled {
		return
	}
	daemon.SafeGo("curator-feed", func() { s.runCuratorFeed(ctx, 5*time.Minute) })

	swarmPruner := swarm.NewRetentionPruner(7 * 24 * time.Hour)
	swarmPruner.SetTracker(s.peerTracker)
	swarmPruner.SetManifestStore(s.manifestStore)
	swarmPruner.SetLivenessTTL(parseLivenessTTL(s.cfg.Swarm.ManifestLivenessTTL))
	daemon.SafeGo("swarm-pruner", func() { swarmPruner.Start(ctx, 24*time.Hour) })
}

// startHTTPServer starts the REST + UI gateway. Errors are logged from
// the goroutine; bind failures surface as the goroutine exiting and the
// gateway being unreachable.
func (s *Server) startHTTPServer() {
	go func() {
		slog.Info("REST gateway starting", "addr", s.cfg.Server.APIListen, "tls", true)
		if err := s.httpSrv.ListenAndServeTLS(s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
			slog.Error("REST gateway error", "error", err)
		}
	}()
}

// startS3Server starts the curator S3-compatible API listener (ADR-027) when
// swarm.curator.s3_port is configured; no-op otherwise. Reuses the server's
// TLS material — the same cert/key the REST gateway serves.
func (s *Server) startS3Server() {
	if s.s3Srv == nil {
		return
	}
	go func() {
		slog.Info("curator S3 API starting", "addr", s.s3Srv.Addr, "tls", true)
		if err := s.s3Srv.ListenAndServeTLS(s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
			slog.Error("curator S3 API error", "error", err)
		}
	}()
}

// startSpannerGossip runs the Slice 2-i-a roster-replication
// gossiper. Constructed in NewServer for every non-standalone bolt;
// nil in standalone mode. The gossiper pulls every peer's /snapshot
// on the Tier-2 timer and CRDT-merges it so all bolts converge on one
// admissions roster.
func (s *Server) startSpannerGossip(ctx context.Context) {
	if s.spannerGossip == nil {
		return
	}
	daemon.SafeGo("spanner-gossip", func() { s.spannerGossip.Run(ctx) })
}
