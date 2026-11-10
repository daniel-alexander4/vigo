// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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
	// Every background goroutine runs under this ctx, so they all wind down
	// when Run returns for any reason, not only on the caller's cancel.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.startPeerReplication(ctx)
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
	s.startSpannerAutoAdmit(ctx)

	s.ready.Store(true)
	slog.Info("server ready",
		"mode", s.mode,
		"grpc", s.cfg.Server.GRPCListen,
		"rest", s.cfg.Server.APIListen,
	)

	<-ctx.Done()
	s.ready.Store(false)
	slog.Info("shutting down...")
	s.shutdownListeners(shutdownTimeout)

	slog.Info("server stopped")
	return nil
}

// shutdownTimeout bounds each listener's graceful stop. Held AgentStreams and
// TunnelStreams are long-lived, so GracefulStop alone could wait forever; past
// the deadline the gRPC servers are stopped hard and the HTTP servers closed.
const shutdownTimeout = 10 * time.Second

// bgDrainTimeout bounds how long Close waits for background goroutines (the
// flushers' final batch) before closing the DB anyway.
const bgDrainTimeout = 15 * time.Second

// goBG starts a server-owned background goroutine under panic recovery and
// tracks it on s.bg so Close can wait for it. Only for goroutines that exit
// when Run's ctx is cancelled.
func (s *Server) goBG(name string, fn func()) {
	s.bg.Add(1)
	daemon.SafeGo(name, func() {
		defer s.bg.Done()
		fn()
	})
}

// waitBackground waits for every goBG goroutine, up to timeout.
func (s *Server) waitBackground(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		s.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("background goroutines still running at shutdown; closing the database anyway", "waited", timeout)
	}
}

// gracefulStopper is the subset of *grpc.Server that shutdown uses.
type gracefulStopper interface {
	GracefulStop()
	Stop()
}

// stopGRPCBounded runs GracefulStop and falls back to Stop when it has not
// returned within timeout (held streams never drain on their own).
func stopGRPCBounded(name string, srv gracefulStopper, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("gRPC graceful stop timed out; forcing stop", "server", name, "timeout", timeout)
		srv.Stop()
		<-done
	}
}

// shutdownHTTPBounded drains an HTTP server for up to timeout, then closes it.
func shutdownHTTPBounded(name string, srv *http.Server, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("HTTP shutdown did not complete; closing", "server", name, "error", err)
		_ = srv.Close()
	}
}

// shutdownListeners stops every listener concurrently, each bounded by timeout.
func (s *Server) shutdownListeners(timeout time.Duration) {
	var wg sync.WaitGroup
	stop := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	if s.grpcSrv != nil {
		stop(func() { stopGRPCBounded("grpc", s.grpcSrv, timeout) })
	}
	if s.operatorSrv != nil {
		stop(func() { stopGRPCBounded("operator", s.operatorSrv, timeout) })
	}
	if s.httpSrv != nil {
		stop(func() { shutdownHTTPBounded("rest", s.httpSrv, timeout) })
	}
	if s.s3Srv != nil {
		stop(func() { shutdownHTTPBounded("curator-s3", s.s3Srv, timeout) })
	}
	wg.Wait()
}

// startPeerReplication captures the lifecycle ctx for the HA push loop.
// gRPC registration happens in NewServer; the primary-side push loop is
// managed by setPrimaryPushing, which uses the ctx captured here so a runtime
// Promote/Demote can start or stop it without a restart.
func (s *Server) startPeerReplication(ctx context.Context) {
	if s.peerService == nil {
		return
	}
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
	s.goBG("state-flusher", func() { stateFlusher.Start(ctx, s.lastSeenFlush, 0) })
	s.goBG("run-batcher", func() { s.runBatcher.Start(ctx, s.lastSeenFlush, s.lastSeenFlush/3) })
	s.goBG("traits-batcher", func() { s.traitsBatch.Start(ctx, s.lastSeenFlush, 2*s.lastSeenFlush/3) })
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
	s.goBG("tickets-rotation", func() {
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
	s.goBG("task-reaper", func() { taskReaper.Start(ctx) })

	retention := parseRetention(s.cfg.Database.Retention)
	retentionPruner := db.NewRetentionPruner(s.queries, s.db, retention)
	retentionPruner.SetPostgres(s.isPostgres)
	s.goBG("retention-pruner", func() { retentionPruner.Start(ctx, 24*time.Hour) })
	slog.Info("retention pruner enabled", "retention", retention, "interval", "24h")

	if !s.isPostgres && s.cfg.Backup.URL != "" {
		replicator := backup.NewReplicator(backup.Config{
			URL:             s.cfg.Backup.URL,
			DBPath:          backup.SQLitePathFromDSN(s.cfg.Database.DSN),
			AccessKeyID:     s.cfg.Backup.AccessKeyID,
			SecretAccessKey: s.cfg.Backup.SecretAccessKey,
			Region:          s.cfg.Backup.Region,
			Endpoint:        s.cfg.Backup.Endpoint,
			ForcePathStyle:  s.cfg.Backup.ForcePathStyle,
			Retention:       s.cfg.Backup.Retention,
			SyncInterval:    s.cfg.Backup.SyncInterval,
		}, backup.NewMetrics(s.promReg))
		s.goBG("litestream-replicator", func() {
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
		// Plaintext server (operatorSrv), not the mTLS grpcSrv — the gateway
		// dials this socket with insecure credentials.
		if err := s.operatorSrv.Serve(udsLis); err != nil {
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
	sigMode := string(s.cfg.CheckIn.StreamSignature)
	if sigMode == "" {
		sigMode = "all"
	}
	s.metrics.CheckinSignatureMode.WithLabelValues(sigMode).Set(1)
	serverStartTime := time.Now()
	s.goBG("uptime-tracker", func() {
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
	var configcrateSource metrics.ConfigcrateSource
	if s.configStore != nil {
		configcrateSource = s.configStore
	}
	metricsCollector := metrics.NewCollector(s.db, fleetSource, swarmSource, configcrateSource, s.metrics, s.baseInterval, s.offlineThreshold)
	s.goBG("metrics-collector", func() { metricsCollector.Run(ctx, s.metricsInterval) })

	// Start stale envoy checker — fires envoy.stale events on transition.
	s.goBG("stale-checker", func() { s.runStaleChecker(ctx) })

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
			s.goBG("secret-watcher", func() {
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
// cfg.CheckIn.ComplianceThreshold > 0; the audit re-verifier and the
// security-posture snapshot always run.
func (s *Server) startConvergenceAlerters(ctx context.Context) {
	// Warm + periodically re-anchor the audit-chain verifier (boot full
	// re-verify after any downtime, then daily). Always on — independent of
	// the compliance threshold below.
	s.goBG("audit-reverify", func() { s.runAuditReverify(ctx) })

	// Start framework-aware drift alerter (background, not hot path).
	// Scans recent runs every 5 minutes, enriches drift events with framework/control context.
	s.goBG("framework-drift-alerter", func() { s.runFrameworkDriftAlerter(ctx) })

	// Start compliance threshold checker (threshold from checkin config, default 0 = disabled).
	complianceThreshold := s.cfg.CheckIn.ComplianceThreshold
	if complianceThreshold > 0 {
		s.goBG("compliance-checker", func() { s.runComplianceChecker(ctx, complianceThreshold) })
	}

	// Security-posture snapshots run on their own schedule regardless of the
	// compliance threshold: the posture history is independent of it.
	s.goBG("security-posture-snapshot", func() { s.runSecurityPostureSnapshot(ctx) })
}

// startSnapshotters runs the four daily snapshot loops: convergence,
// risk posture, MTTR stats, and standards-compliance scores. Each takes
// an initial snapshot at startup so the sparklines aren't empty on a
// fresh boot.
func (s *Server) startSnapshotters(ctx context.Context) {
	// Start compliance sparkline snapshotting (daily snapshot for the in-memory ring buffer + DB).
	s.goBG("convergence-snapshot", func() {
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
	s.goBG("risk-snapshot", func() {
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
	s.goBG("mttr-stats", func() {
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
	s.goBG("compliance-snapshot", func() {
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
		s.goBG("grc-pusher", func() { s.grcPusher.Start(ctx) })
	}
}

// startNVDEnricher starts the NVD CVE enricher's reactor. No-op when
// cfg.Risk.NVD.Enabled is false. The enricher feeds the AI assistant's
// CVE on-demand lookup path as well.
func (s *Server) startNVDEnricher(ctx context.Context) {
	if s.nvdEnricher == nil {
		return
	}
	s.goBG("nvd-enricher", func() {
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
	s.goBG("stacks-watcher", func() {
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
	s.goBG("curator-feed", func() { s.runCuratorFeed(ctx, 5*time.Minute) })

	swarmPruner := swarm.NewRetentionPruner(7 * 24 * time.Hour)
	swarmPruner.SetTracker(s.peerTracker)
	swarmPruner.SetManifestStore(s.manifestStore)
	swarmPruner.SetLivenessTTL(parseLivenessTTL(s.cfg.Swarm.ManifestLivenessTTL))
	s.goBG("swarm-pruner", func() { swarmPruner.Start(ctx, 24*time.Hour) })
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
	s.goBG("spanner-gossip", func() { s.spannerGossip.Run(ctx) })
}

// startSpannerAutoAdmit runs the config-driven hands-off peering loop
// (ADR-026): when spanner.peers is set, a joining bolt that has no
// founder row yet POSTs its self-signed identity to each peer until the
// founder admits it and returns a founder-signed roster, which is
// CRDT-merged to seed the local roster; the gossiper maintains
// convergence from there. No-op in standalone mode (gossiper nil), when
// no peers are configured, or once a founder row is already local (the
// founder itself, or a joiner that already bootstrapped). Runs off the
// check-in hot path with exponential backoff.
func (s *Server) startSpannerAutoAdmit(ctx context.Context) {
	if s.spannerGossip == nil || len(s.cfg.Spanner.Peers) == 0 {
		return
	}
	peers := s.cfg.Spanner.Peers
	hostname := s.cfg.Server.Hostname
	patterns := s.cfg.Spanner.Patterns
	s.goBG("spanner-auto-admit", func() {
		s.spannerGossip.AutoAdmit(ctx, peers, hostname, patterns)
	})
}
