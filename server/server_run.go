// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/backup"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/spanner"
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

	if err := s.startGRPCServer(ctx); err != nil {
		return err
	}

	s.startMetricsAndHealthChecks(ctx)
	s.startConvergenceAlerters(ctx)
	s.startSnapshotters(ctx)
	s.startGRC(ctx)
	s.startNVDEnricher(ctx)
	s.startStockpileWatcher(ctx)
	s.startAIWarmup()
	s.startSwarm(ctx)
	s.startHTTPServer()
	s.startReporter(ctx)
	s.startSpannerGossip(ctx)

	if s.mode == config.ModeJoiner {
		slog.Info("bolt ready",
			"bolt_pubkey", formatLocalBoltPubkey(s.boltID),
			"grpc", s.cfg.Server.GRPCListen,
			"api", s.cfg.Server.APIListen,
		)
	} else {
		slog.Info("server ready",
			"mode", s.mode,
			"grpc", s.cfg.Server.GRPCListen,
			"rest", s.cfg.Server.APIListen,
		)
	}

	<-ctx.Done()
	slog.Info("shutting down...")

	s.grpcSrv.GracefulStop()
	if err := s.httpSrv.Shutdown(context.Background()); err != nil {
		slog.Error("REST gateway shutdown error", "error", err)
	}

	slog.Info("server stopped")
	return nil
}

// startLicenseEnforcement runs the license enforcer's periodic check
// loop. The enforcer was wired with a stop-service callback during
// NewServer that calls s.cancel on terminal license failure.
func (s *Server) startLicenseEnforcement(ctx context.Context) {
	go s.enforcer.Run(ctx)

	// Peer replication: gRPC registration migrated into NewServer. The
	// primary-side push goroutine lives here because it depends on the
	// lifecycle ctx.
	if s.peerService != nil {
		peerRole := s.cfg.Peers.Role
		if peerRole == "" {
			peerRole = "primary"
		}
		if peerRole == "primary" {
			primary := peer.NewPrimary(s.serverRoot, s.cfg.Peers.Servers, s.peerTLS)
			s.peerPrimary = primary
			go func() {
				if err := primary.Start(ctx); err != nil && ctx.Err() == nil {
					slog.Error("peer primary error", "error", err)
				}
			}()
		}
	}

	// Hub auto-failover: construction migrated into NewServer; only the
	// goroutine start stays here.
	if s.hubFailover != nil {
		go s.hubFailover.Run(ctx)
	}
}

// startAsyncFlushers starts the staggered SQLite write batchers
// (last-seen, run-result, traits) plus the traits-snapshot pruner.
// Offsets avoid SQLite write-lock contention on the default 10s
// interval.
func (s *Server) startAsyncFlushers(ctx context.Context) {
	stateFlusher := fleet.NewStateFlusher(s.fleetIndex, s.queries, s.metrics)
	go stateFlusher.Start(ctx, s.lastSeenFlush, 0)
	go s.runBatcher.Start(ctx, s.lastSeenFlush, s.lastSeenFlush/3)
	go s.traitsBatch.Start(ctx, s.lastSeenFlush, 2*s.lastSeenFlush/3)
	go s.traitsPruner.Start(ctx, s.traitsPruneInterval)
}

// startRetentionAndBackup starts the task-run timeout reaper, the
// generic retention pruner, and (SQLite only, when configured) the
// Litestream WAL replicator.
func (s *Server) startRetentionAndBackup(ctx context.Context) {
	taskReaper := task.NewReaper(s.queries)
	go taskReaper.Start(ctx)

	retention := parseRetention(s.cfg.Database.Retention)
	retentionPruner := db.NewRetentionPruner(s.queries, s.db, retention)
	retentionPruner.SetPostgres(s.isPostgres)
	go retentionPruner.Start(ctx, 24*time.Hour)
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
		go func() {
			if err := replicator.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("litestream replication failed", "error", err)
			}
		}()
	}
}

// startGRPCServer binds the gRPC listener and starts the serve
// goroutine. Returns an error if the listener cannot bind.
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
	go func() {
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
	}()

	// Start metrics collector in background.
	var fleetSource metrics.FleetSource
	if s.fleetIndex != nil {
		fleetSource = &metricsFleetAdapter{fleetIndex: s.fleetIndex}
	}
	metricsCollector := metrics.NewCollector(s.queries, s.db, fleetSource, s.metrics, s.baseInterval, s.offlineThreshold)
	go metricsCollector.Run(ctx, s.metricsInterval)

	// Start stale envoy checker — fires envoy.stale events on transition.
	go s.runStaleChecker(ctx)

	// Expire any overdue compliance waivers daily.
	go func() {
		for {
			_ = s.queries.ExpireComplianceWaivers(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
			}
		}
	}()

	// Start secret rotation watcher (founder and standalone only).
	if s.mode != config.ModeJoiner && s.cfg.Watcher.Enabled {
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
			go func() {
				if err := w.Run(ctx); err != nil && ctx.Err() == nil {
					slog.Error("secret watcher error", "error", err)
				}
			}()
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
	// Start framework-aware drift alerter (background, not hot path).
	// Scans recent runs every 5 minutes, enriches drift events with framework/control context.
	go s.runFrameworkDriftAlerter(ctx)

	// Start compliance threshold checker (threshold from checkin config, default 0 = disabled).
	complianceThreshold := s.cfg.CheckIn.ComplianceThreshold
	if complianceThreshold > 0 {
		go s.runComplianceChecker(ctx, complianceThreshold)
		go s.runSecurityPostureSnapshot(ctx)
	}
}

// startSnapshotters runs the four daily snapshot loops: convergence,
// risk posture, MTTR stats, and standards-compliance scores. Each takes
// an initial snapshot at startup so the sparklines aren't empty on a
// fresh boot.
func (s *Server) startSnapshotters(ctx context.Context) {
	// Start compliance sparkline snapshotting (daily snapshot for the in-memory ring buffer + DB).
	go func() {
		// Take an initial snapshot immediately so the sparkline isn't empty on startup.
		s.fleetIndex.SnapshotConvergence()
		s.persistConvergenceSnapshot(ctx)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.fleetIndex.SnapshotConvergence()
				s.persistConvergenceSnapshot(ctx)
			}
		}
	}()

	// Start daily risk posture snapshotting.
	go func() {
		s.persistRiskSnapshot(ctx)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.persistRiskSnapshot(ctx)
			}
		}
	}()

	// Start MTTR stats computation (daily, on same cadence as compliance snapshots).
	go func() {
		s.computeAndStoreMTTR(ctx)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.computeAndStoreMTTR(ctx)
			}
		}
	}()

	// Start daily standards compliance snapshotting through the unified scorer.
	go func() {
		s.persistComplianceSnapshot(ctx)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.persistComplianceSnapshot(ctx)
			}
		}
	}()
}

// startGRC starts the GRC evidence pusher's loop. No-op if no GRC
// integrations are configured.
func (s *Server) startGRC(ctx context.Context) {
	if s.grcPusher != nil {
		go s.grcPusher.Start(ctx)
	}
}

// startNVDEnricher starts the NVD CVE enricher's reactor. No-op when
// cfg.Risk.NVD.Enabled is false. The enricher feeds the AI assistant's
// CVE on-demand lookup path as well.
func (s *Server) startNVDEnricher(ctx context.Context) {
	if s.nvdEnricher == nil {
		return
	}
	go s.nvdEnricher.Run(ctx, func() []string {
		return collectFleetCVEIDs(s.fleetIndex)
	})
}

// startStockpileWatcher runs the ADR-025 Slice 3 stockpile-in-gitback
// watcher. Nil when the stockpile project hasn't been founded yet
// (vigocli spanner stockpile init) — a future SIGHUP / restart picks it
// up.
func (s *Server) startStockpileWatcher(ctx context.Context) {
	if s.stockpileWatcher == nil {
		return
	}
	w := s.stockpileWatcher
	go func() {
		if err := w.Run(ctx); err != nil {
			slog.Warn("stockpile watcher exited", "error", err)
		}
	}()
}

// startAIWarmup pre-warms the Ollama model so the first user request
// doesn't hit a 60-90 second cold-load delay. No-op for non-Ollama
// providers.
func (s *Server) startAIWarmup() {
	if ollamaP, ok := s.aiProvider.(*vigoai.OllamaProvider); ok {
		go ollamaP.WarmModel(s.cfg.AI.Model)
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
	go s.runCuratorFeed(ctx, 5*time.Minute)

	swarmPruner := swarm.NewRetentionPruner(7 * 24 * time.Hour)
	swarmPruner.SetTracker(s.peerTracker)
	swarmPruner.SetManifestStore(s.manifestStore)
	swarmPruner.SetLivenessTTL(parseLivenessTTL(s.cfg.Swarm.ManifestLivenessTTL))
	go swarmPruner.Start(ctx, 24*time.Hour)
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

// startReporter runs the bolt status reporter on joiner nodes. The
// reporter signs every announcement with the bolt identity loaded
// during NewServer; nil signing key falls back to legacy unsigned
// reports (accepted by peers during mixed-version rollouts). Bootstrap
// peers come from spanner.transport.bootstrap_peers (ADR-026); gossip
// will extend this set once 2A.2 wires the CRDT merge.
func (s *Server) startReporter(ctx context.Context) {
	if s.mode != config.ModeJoiner || len(s.cfg.Spanner.Transport.BootstrapPeers) == 0 {
		return
	}
	reporter := spanner.NewReporter(
		formatLocalBoltPubkey(s.boltID),
		s.cfg.Spanner.SpannerID,
		boltSigningKeyOrNil(s.boltID),
		s.cfg.Spanner.Transport.BootstrapPeers,
		s.cfg.Server.TLS.CertFile,
		s.cfg.Server.TLS.KeyFile,
		s.cfg.Server.TLS.CAFile,
		s.fleetIndex,
		30*time.Second,
	)
	reporter.SetConfigVersion(s.configStore.Version)
	go reporter.Run(ctx)
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
	go s.spannerGossip.Run(ctx)
}
