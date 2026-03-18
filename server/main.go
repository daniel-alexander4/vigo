// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"database/sql"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	hobnobai "github.com/hobnob/hobnob/server/ai"
	"github.com/hobnob/hobnob/server/api"
	"github.com/hobnob/hobnob/server/audit"
	hobnobauth "github.com/hobnob/hobnob/server/auth"
	"github.com/hobnob/hobnob/server/backup"
	"github.com/hobnob/hobnob/server/bootstrap"
	"github.com/hobnob/hobnob/server/ca"
	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/publish"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/spanner"
	"github.com/hobnob/hobnob/server/hardening"
	"github.com/hobnob/hobnob/server/fleet"
	isgrpc "github.com/hobnob/hobnob/server/grpc"
	"github.com/hobnob/hobnob/server/license"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/secrets"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/push"
	"github.com/hobnob/hobnob/server/peer"
	"github.com/hobnob/hobnob/server/watcher"
	"github.com/hobnob/hobnob/server/webhook"
	"github.com/hobnob/hobnob/server/workflows"
)

// version is set at build time via -ldflags="-X main.version=1.2.3".
var version = "dev"

func main() {
	if err := run(); err != nil {
		var se *config.SecretErrors
		if errors.As(err, &se) {
			fmt.Fprint(os.Stderr, se.Format())
		} else {
			slog.Error("server failed", "error", err)
		}
		os.Exit(1)
	}
}

func run() error {
	// --- Binary integrity and anti-tamper checks ---
	hardening.DetectDebugger()
	hardening.DetectInjection()
	if err := hardening.VerifyBinarySignature(); err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Determine config file path (the one env var that must remain — it
	// bootstraps everything else).
	configFile := envOrDefault("HOBNOB_CONFIG_FILE", "server.yaml")
	serverRoot := filepath.Dir(configFile)

	// Load secrets backend config (first pass — no secret resolution).
	secretsCfg, err := config.LoadSecretsConfig(configFile)
	if err != nil {
		return fmt.Errorf("loading secrets config: %w", err)
	}

	// Initialize secrets provider from config.
	provider, err := secrets.NewFromConfig(secretsCfg)
	if err != nil {
		return fmt.Errorf("initializing secrets provider: %w", err)
	}
	defer provider.Close()

	slog.Info("secrets provider initialized", "backend", secretsCfg.Backend)

	// Auto-generate self-signed TLS certs on first start (local backend only).
	if _, ok := provider.(*secrets.LocalProvider); ok {
		tlsDir := filepath.Join(filepath.Dir(configFile), "tls")
		extraSANs := config.LoadTLSSANs(configFile)
		if err := secrets.EnsureTLSCerts(tlsDir, extraSANs...); err != nil {
			return fmt.Errorf("auto-generating TLS certificates: %w", err)
		}
	}

	// Load full server config with secret resolution (second pass).
	cfg, err := config.LoadServerConfig(configFile, provider, secretsCfg.Backend)
	if err != nil {
		return fmt.Errorf("loading server config: %w", err)
	}

	// Resolve directory paths: env var > server.yaml paths: > default.
	migrationsDir := envOrDefault("HOBNOB_MIGRATIONS_DIR", stringOrDefault(cfg.Paths.Migrations, "migrations"))
	traitsDir := envOrDefault("HOBNOB_CUSTOM_TRAITS_DIR", stringOrDefault(cfg.Paths.CustomTraits, "custom-traits"))
	docsDir := envOrDefault("HOBNOB_DOCS_DIR", stringOrDefault(cfg.Paths.Docs, "docs"))
	pushDir := envOrDefault("HOBNOB_PUSH_DIR", stringOrDefault(cfg.Paths.Tasks, "tasks"))
	workflowsDir := envOrDefault("HOBNOB_WORKFLOWS_DIR", stringOrDefault(cfg.Paths.Workflows, "workflows"))
	agentDir := envOrDefault("HOBNOB_AGENT_DIR", stringOrDefault(cfg.Paths.AgentDist, "dist/agent"))
	licenseDir := stringOrDefault(cfg.Paths.LicenseDir, license.DefaultLicenseDir)
	licenseGraceDir := licenseDir

	// Determine spanner role.
	role := cfg.Spanner.Role
	if role == "" {
		role = config.RoleStandalone
	}
	if err := cfg.Spanner.Validate(); err != nil {
		return err
	}

	slog.Info("server config loaded",
		"role", role,
		"grpc_listen", cfg.Server.GRPCListen,
		"api_listen", cfg.Server.APIListen,
	)

	// Apply Go runtime tuning early (before allocations ramp up).
	applyRuntimeTuning(cfg.Tuning)

	// Determine database driver.
	dbDriver := cfg.Database.Driver
	if dbDriver == "" {
		dbDriver = "sqlite"
	}
	isPostgres := dbDriver == "postgres"

	// Open database connection.
	var sqlDB *sql.DB
	if isPostgres {
		sqlDB, err = sql.Open("postgres", cfg.Database.DSN)
	} else {
		sqlDB, err = sql.Open("sqlite3", cfg.Database.DSN)
	}
	if err != nil {
		return fmt.Errorf("opening database (%s): %w", dbDriver, err)
	}
	defer sqlDB.Close()

	// SQLite-specific tuning.
	if !isPostgres {
		pragmas := []struct{ sql, desc string }{
			{"PRAGMA foreign_keys = ON", "enabling foreign keys"},
			{"PRAGMA journal_mode = WAL", "setting WAL mode"},
			{"PRAGMA busy_timeout = 5000", "setting busy timeout"},
			{"PRAGMA synchronous = NORMAL", "setting synchronous mode"},
			{"PRAGMA cache_size = -64000", "setting cache size"},
			{"PRAGMA temp_store = MEMORY", "setting temp store"},
			{"PRAGMA auto_vacuum = INCREMENTAL", "setting auto vacuum"},
		}
		for _, p := range pragmas {
			if _, err := sqlDB.ExecContext(ctx, p.sql); err != nil {
				return fmt.Errorf("%s: %w", p.desc, err)
			}
		}
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}
	slog.Info("database connected", "driver", dbDriver)

	// Run migrations (use driver-specific migration directory for Postgres).
	migDir := migrationsDir
	if isPostgres {
		migDir = filepath.Join(migrationsDir, "postgres")
	}
	absPath, err := filepath.Abs(migDir)
	if err != nil {
		return fmt.Errorf("resolving migrations path: %w", err)
	}
	var migDSN string
	if isPostgres {
		migDSN = cfg.Database.DSN
	} else {
		migDSN = "sqlite3://" + cfg.Database.DSN
	}
	mig, err := migrate.New("file://"+absPath, migDSN)
	if err != nil {
		return fmt.Errorf("creating migrate instance: %w", err)
	}
	if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("running migrations: %w", err)
	}
	srcErr, dbErr := mig.Close()
	if srcErr != nil {
		return fmt.Errorf("closing migrate source: %w", srcErr)
	}
	if dbErr != nil {
		return fmt.Errorf("closing migrate db: %w", dbErr)
	}
	slog.Info("migrations complete")

	// Initialize query layer early — needed for license thrall count.
	// For Postgres, wrap DB with placeholder rewriter so sqlc-generated
	// queries (which use ?) work with Postgres ($1, $2, ...).
	var dbtx db.DBTX = sqlDB
	if isPostgres {
		dbtx = &db.PgDBTX{DB: sqlDB}
	}
	queries := db.New(dbtx)

	// Initialize tamper-evident audit writer.
	auditWriter := audit.NewWriter(queries)

	// Load and validate license.
	thrallCounter := func(ctx context.Context) (int, error) {
		thralls, err := queries.ListThralls(ctx)
		if err != nil {
			return 0, err
		}
		count := 0
		for _, t := range thralls {
			if t.Revoked == 0 {
				count++
			}
		}
		return count, nil
	}

	enforcer, err := license.NewEnforcer(licenseDir, licenseGraceDir, cfg.Server.TLS.CAFile, thrallCounter, func() { cancel() })
	if err != nil {
		return fmt.Errorf("license: %w", err)
	}

	slog.Info("license loaded",
		"license_id", enforcer.GetStatus().License.LicenseID,
		"stage", enforcer.GetStatus().Stage.String(),
	)

	// Spanner requires a paid license — reject hub/spoke on free tier.
	if role != config.RoleStandalone && enforcer.IsFreeLicense() {
		return fmt.Errorf("spanner (role %q) requires a paid license — the built-in free license does not support spanner", role)
	}

	// Set up SIGHUP handler for license reload.
	sighupCh := make(chan os.Signal, 1)
	signal.Notify(sighupCh, syscall.SIGHUP)
	go func() {
		for range sighupCh {
			slog.Info("received SIGHUP, reloading license...")
			if err := enforcer.Reload(); err != nil {
				slog.Error("license reload failed", "error", err)
			} else {
				s := enforcer.GetStatus()
				slog.Info("license reloaded", "stage", s.Stage.String())
			}
		}
	}()

	// Start license enforcer periodic check.
	go enforcer.Run(ctx)

	// Initialize auth provider.
	authProvider, err := createAuthProvider(ctx, cfg, sqlDB, queries, provider)
	if err != nil {
		return fmt.Errorf("initializing auth: %w", err)
	}
	authMethod := authProvider.Method()
	slog.Info("auth provider initialized", "method", authMethod)

	// Initialize subsystems.
	reg := registry.NewRegistry(queries)

	// Warn if .live directory has overly permissive permissions.
	if warning := publish.CheckLivePermissions(publish.LiveDir); warning != "" {
		slog.Warn(warning)
	}

	configStore, err := config.NewConfigStore(publish.LiveDir, provider, false)
	if err != nil {
		slog.Error("config loaded with errors — starting with empty config", "error", err)
		configStore = config.NewEmptyConfigStore(provider)
	}
	configStore.SetBackend(secretsCfg.Backend)


	// Parse base interval for compliance stale threshold.
	baseInterval := 5 * time.Minute // default
	if cfg.CheckIn.Interval != "" {
		if parsed, err := time.ParseDuration(cfg.CheckIn.Interval); err == nil {
			baseInterval = parsed
		}
	}

	// Parse tuning parameters (all optional, zero values use defaults).
	sigWindow := parseDuration(cfg.Tuning.SignatureWindow, 0)
	lastSeenFlush := parseDuration(cfg.Tuning.LastSeenFlush, 10*time.Second)
	traitsPruneInterval := parseDuration(cfg.Tuning.TraitsPruneInterval, 5*time.Minute)
	metricsInterval := parseDuration(cfg.Tuning.MetricsInterval, 30*time.Second)
	traitsScannerTTL := parseDuration(cfg.Tuning.TraitsScannerTTL, 0)
	whTimeout := parseDuration(cfg.Tuning.Webhook.Timeout, 0)
	whRetryDelay := parseDuration(cfg.Tuning.Webhook.RetryDelay, 0)
	maxConnAge := parseDuration(cfg.Tuning.MaxConnectionAge, 0)
	maxConnIdle := parseDuration(cfg.Tuning.MaxConnectionIdle, 0)
	kaTime := parseDuration(cfg.Tuning.KeepaliveTime, 0)
	kaTimeout := parseDuration(cfg.Tuning.KeepaliveTimeout, 0)

	// Create run store backend.
	var runStore db.RunStore
	switch cfg.Tuning.RunStore {
	case "memory":
		runStore = db.NewMemRunStore(cfg.Tuning.RunStoreCapacity)
		slog.Info("run store backend: memory", "capacity", cfg.Tuning.RunStoreCapacity)
	default:
		runStore = db.NewDBRunStore(queries)
		slog.Info("run store backend: database")
	}

	// Create webhook dispatcher (hub and standalone only — spokes skip webhooks).
	var dispatcher *webhook.Dispatcher
	if role != config.RoleSpoke {
		dispatcher = webhook.NewDispatcher(queries, webhook.DispatcherConfig{
			Timeout:       whTimeout,
			MaxRetries:    cfg.Tuning.Webhook.MaxRetries,
			MaxConcurrent: cfg.Tuning.Webhook.MaxConcurrent,
			RetryDelay:    whRetryDelay,
		})
	}

	// Create SMTP notifier (disabled when smtp.host is empty).
	var smtpEvents map[string]*hobnobsmtp.EventOverride
	if len(cfg.SMTP.Events) > 0 {
		smtpEvents = make(map[string]*hobnobsmtp.EventOverride, len(cfg.SMTP.Events))
		for name, ec := range cfg.SMTP.Events {
			smtpEvents[name] = &hobnobsmtp.EventOverride{
				Recipients: ec.Recipients,
			}
		}
	}

	var digestCfg *hobnobsmtp.DigestConfig
	if cfg.SMTP.Digest.Interval != "" {
		if interval, pErr := time.ParseDuration(cfg.SMTP.Digest.Interval); pErr == nil && interval > 0 {
			digestEvents := make(map[string]bool, len(cfg.SMTP.Digest.Events))
			for _, e := range cfg.SMTP.Digest.Events {
				digestEvents[e] = true
			}
			digestCfg = &hobnobsmtp.DigestConfig{
				Interval: interval,
				Events:   digestEvents,
			}
		}
	}

	smtpNotifier := hobnobsmtp.NewNotifier(hobnobsmtp.Config{
		Host:       cfg.SMTP.Host,
		Port:       cfg.SMTP.Port,
		From:       cfg.SMTP.From,
		Username:   cfg.SMTP.Username,
		Password:   cfg.SMTP.Password,
		TLS:        cfg.SMTP.TLS,
		Recipients: cfg.SMTP.Recipients,
		Events:     smtpEvents,
		Digest:     digestCfg,
	})
	if smtpNotifier.Enabled() {
		slog.Info("SMTP notifications enabled", "host", cfg.SMTP.Host, "recipients", len(cfg.SMTP.Recipients))
	}

	// Wire SMTP notification for config reload failures.
	configStore.SetOnReloadFailure(func(err error) {
		smtpNotifier.Notify(hobnobsmtp.Event{
			Name:      "config.reload.failure",
			Timestamp: time.Now().UTC(),
			Details:   map[string]any{"error": err.Error()},
		})
	})

	// Connect to spokes (hub mode only).
	var hub *spanner.Hub
	if role == config.RoleHub {
		hub, err = spanner.NewHub(ctx, cfg.Spanner.Spokes, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile)
		if err != nil {
			return fmt.Errorf("connecting to spokes: %w", err)
		}
		defer hub.Close()
		slog.Info("spanner hub initialized", "spokes", hub.SpokeCount())

		// Start auto-failover if enabled.
		if cfg.Spanner.AutoFailover.Enabled {
			foThreshold := cfg.Spanner.AutoFailover.Threshold
			if foThreshold <= 0 {
				foThreshold = 3
			}
			foInterval := parseDuration(cfg.Spanner.AutoFailover.Interval, 30*time.Second)
			failover := spanner.NewFailover(hub, foThreshold, foInterval)
			go failover.Run(ctx)
		}
	}

	// Load push definitions.
	pushStore := push.NewStore()
	if err := pushStore.Load(pushDir); err != nil {
		return fmt.Errorf("loading push definitions: %w", err)
	}
	slog.Info("push definitions loaded", "count", len(pushStore.List()), "dir", pushDir)

	// Create and rehydrate FleetIndex.
	fleetIndex := fleet.NewFleetIndex()
	if err := fleet.Rehydrate(ctx, fleetIndex, queries, runStore, baseInterval); err != nil {
		return fmt.Errorf("rehydrating fleet index: %w", err)
	}

	// Self-enroll: the server registers itself as its own thrall so it can
	// manage its own state (backups, monitoring, etc.) via standard modules.
	// Create async run batcher for delta events.
	runBatcher := fleet.NewRunBatcher(runStore)

	// Initialize bundle signer for compiled promises.
	bundleSigner, bundleMaxAge := initBundleSigner(cfg)

	// Build fallback server addresses from peer config (returned to agents on registration).
	var fallbackServers []string
	for _, pe := range cfg.Peers.Servers {
		fallbackServers = append(fallbackServers, pe.Addr)
	}

	// Create and start gRPC server.
	grpcSrv, _, traitsBatch, traitsPruner, agentSvc, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:             cfg.Server.TLS.CertFile,
		KeyFile:              cfg.Server.TLS.KeyFile,
		CAFile:               cfg.Server.TLS.CAFile,
		GRPCPort:             cfg.Server.GRPCListen,
		Registry:             reg,
		ConfigStore:          configStore,
		Queries:              queries,
		RunStore:             runStore,
		Interval:             baseInterval,
		JitterPercent:        int32(cfg.CheckIn.JitterPercent),
		Dispatcher:           dispatcher,
		TraitsDir:            traitsDir,
		SignatureWindow:      sigWindow,
		TraitsScannerTTL:     traitsScannerTTL,
		MaxConcurrentStreams: cfg.Tuning.MaxConcurrentStreams,
		GRPCReadBuffer:      cfg.Tuning.GRPCReadBuffer,
		GRPCWriteBuffer:     cfg.Tuning.GRPCWriteBuffer,
		MaxConnectionAge:    maxConnAge,
		MaxConnectionIdle:   maxConnIdle,
		KeepaliveTime:       kaTime,
		KeepaliveTimeout:    kaTimeout,
		StreamSendBuffer:    cfg.Tuning.StreamSendBuffer,
		Hub:                  hub,
		SMTPNotifier:         smtpNotifier,
		LicenseEnforcer:      enforcer,
		PushStore:            pushStore,
		FleetIndex:           fleetIndex,
		RunBatcher:           runBatcher,
		BundleSigner:        bundleSigner,
		BundleMaxAge:        bundleMaxAge,
		FallbackServers:     fallbackServers,
		AuditWriter:         auditWriter,
		ServerVersion:       version,
	})
	if err != nil {
		return fmt.Errorf("creating gRPC server: %w", err)
	}

	// Wire peer replication (when peers section is configured).
	var peerSvc *peer.Service
	if len(cfg.Peers.Servers) > 0 {
		peerRole := cfg.Peers.Role
		if peerRole == "" {
			peerRole = "primary"
		}

		peerSvc = peer.NewService(serverRoot, peerRole, func() {
			if err := configStore.Reload(); err != nil {
				slog.Error("peer-triggered config reload failed", "error", err)
			}
		})
		peerSvc.SetAuditWriter(auditWriter)

		// Build TLS config for peer-to-peer communication (used by both roles
		// for split-brain resolution and primary push).
		peerTLS, peerTLSErr := buildPeerTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile)
		if peerTLSErr != nil {
			return fmt.Errorf("building peer TLS config: %w", peerTLSErr)
		}

		// Configure peer addresses for split-brain resolution during Promote.
		var peerAddrs []string
		for _, entry := range cfg.Peers.Servers {
			peerAddrs = append(peerAddrs, entry.Addr)
		}
		peerSvc.SetPeers(peerAddrs, peerTLS)

		pb.RegisterHobnobPeerServer(grpcSrv, peerSvc)
		slog.Info("peer replication enabled", "role", peerRole, "peers", len(cfg.Peers.Servers))

		// Primary role: start pushing file changes to peers.
		if peerRole == "primary" {
			primary := peer.NewPrimary(serverRoot, cfg.Peers.Servers, peerTLS)
			go func() {
				if err := primary.Start(ctx); err != nil && ctx.Err() == nil {
					slog.Error("peer primary error", "error", err)
				}
			}()
			defer primary.Stop()
		}
	}

	// Start async flushers with staggered offsets to avoid SQLite write lock
	// contention. For default 10s interval → offsets of 0s, ~3.3s, ~6.6s.
	stateFlusher := fleet.NewStateFlusher(fleetIndex, queries)
	go stateFlusher.Start(ctx, lastSeenFlush, 0)
	go runBatcher.Start(ctx, lastSeenFlush, lastSeenFlush/3)
	go traitsBatch.Start(ctx, lastSeenFlush, 2*lastSeenFlush/3)

	// Start async traits pruner.
	go traitsPruner.Start(ctx, traitsPruneInterval)

	// Start push timeout reaper — marks expired pushes as timed out.
	pushReaper := push.NewReaper(queries)
	go pushReaper.Start(ctx)

	// Start retention pruner (if configured).
	if retention := parseRetention(cfg.Database.Retention); retention > 0 {
		retentionPruner := db.NewRetentionPruner(queries, sqlDB, retention)
		retentionPruner.SetPostgres(isPostgres)
		go retentionPruner.Start(ctx, 24*time.Hour)
		slog.Info("retention pruner enabled", "retention", retention, "interval", "24h")
	}

	// Start Litestream WAL replication (SQLite only, when backup.url is configured).
	if !isPostgres && cfg.Backup.URL != "" {
		replicator := backup.NewReplicator(backup.Config{
			URL:             cfg.Backup.URL,
			DBPath:          cfg.Database.DSN,
			AccessKeyID:     cfg.Backup.AccessKeyID,
			SecretAccessKey: cfg.Backup.SecretAccessKey,
			Region:          cfg.Backup.Region,
			Endpoint:        cfg.Backup.Endpoint,
			ForcePathStyle:  cfg.Backup.ForcePathStyle,
			Retention:       cfg.Backup.Retention,
			SyncInterval:    cfg.Backup.SyncInterval,
		})
		go func() {
			if err := replicator.Start(ctx); err != nil && ctx.Err() == nil {
				slog.Error("litestream replication failed", "error", err)
			}
		}()
	}

	// Start secret rotation watcher (hub and standalone only).
	if role != config.RoleSpoke && cfg.Watcher.Enabled {
		if vp, ok := provider.(watcher.VersionProvider); ok {
			watchPaths := configStore.SecretPaths()
			pollInterval := 5 * time.Minute
			if cfg.Watcher.PollInterval != "" {
				if parsed, err := time.ParseDuration(cfg.Watcher.PollInterval); err == nil {
					pollInterval = parsed
				}
			}
			w := watcher.NewWatcher(vp, watcher.NewDBAdapter(queries), configStore, dispatcher, pollInterval, watchPaths)
			w.SetConfigReloader(configStore)
			w.SetNotifyFunc(func(event string, details map[string]any) {
				smtpNotifier.Notify(hobnobsmtp.Event{
					Name:      event,
					Timestamp: time.Now().UTC(),
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

	grpcLis, err := net.Listen("tcp", cfg.Server.GRPCListen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", cfg.Server.GRPCListen, err)
	}

	go func() {
		slog.Info("gRPC server starting", "addr", cfg.Server.GRPCListen)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			slog.Error("gRPC server error", "error", err)
		}
	}()

	// Set build info metric and start uptime tracker.
	// version is set at package level via -ldflags at build time.
	metrics.BuildInfo.WithLabelValues(version).Set(1)
	serverStartTime := time.Now()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			metrics.UptimeSeconds.Set(time.Since(serverStartTime).Seconds())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	// Start metrics collector in background.
	metricsCollector := metrics.NewCollector(queries, sqlDB)
	go metricsCollector.Run(ctx, metricsInterval)

	// Start stale thrall checker — fires thrall.stale SMTP events on transition.
	go runStaleChecker(ctx, queries, fleetIndex, smtpNotifier, baseInterval)

	// Start compliance sparkline snapshotting (daily snapshot for the in-memory ring buffer + DB).
	go func() {
		// Take an initial snapshot immediately so the sparkline isn't empty on startup.
		fleetIndex.SnapshotCompliance()
		persistComplianceSnapshot(ctx, queries, fleetIndex)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fleetIndex.SnapshotCompliance()
				persistComplianceSnapshot(ctx, queries, fleetIndex)
			}
		}
	}()

	// Start SMTP digest flush loop (if configured).
	go smtpNotifier.Start(ctx)

	// Start compliance threshold checker (if configured).
	complianceThreshold := 0
	if cfg.SMTP.Events != nil {
		if ec, ok := cfg.SMTP.Events["compliance.threshold"]; ok {
			complianceThreshold = ec.Threshold
		}
	}
	if complianceThreshold > 0 {
		go runComplianceChecker(ctx, queries, runStore, fleetIndex, smtpNotifier, baseInterval, complianceThreshold)
	}

	// Start REST gateway (spoke serves API + metrics only; hub/standalone adds UI + bootstrap).
	var httpSrv *http.Server
	licenseHandler := api.NewLicenseHandler(enforcer)
	doctorHandler := api.NewDoctorHandler(api.DoctorConfig{
		SQLDB:          sqlDB,
		CertFile:       cfg.Server.TLS.CertFile,
		KeyFile:        cfg.Server.TLS.KeyFile,
		Enforcer:       enforcer,
		ConfigStore:    configStore,
		Queries:        queries,
		Interval:       baseInterval,
		Hub:            hub,
		SMTPHost:       cfg.SMTP.Host,
		SMTPPort:       cfg.SMTP.Port,
		Role:           role,
		SecretsBackend: secretsCfg.Backend,
	})

	spannerHandler := api.NewSpannerHandler(role, cfg.Spanner.SpokeID, hub)
	pushHandler := api.NewPushHandler(queries, agentSvc, cfg.Push.RequireDefinition)
	pushHandler.SetAuditWriter(auditWriter)
	queryHandler := api.NewQueryHandler(agentSvc)
	exportHandler := api.NewExportHandler(queries, fleetIndex, configStore, cfg.Export)
	peerHandler := api.NewPeerHandler(peerSvc)

	// Auth-related API handlers.
	userStore := hobnobauth.NewUserStore(queries)
	tokenStore := hobnobauth.NewTokenStore(queries)
	usersHandler := api.NewUsersHandler(userStore, authMethod)
	tokensHandler := api.NewTokensHandler(tokenStore, authMethod)

	// Seed default admin account on first startup (no password — must be set via CLI).
	if count, err := userStore.CountUsers(ctx); err == nil && count == 0 {
		if _, err := userStore.Create(ctx, db.InsertUserParams{
			Username: "admin",
			Role:     "admin",
		}); err != nil {
			slog.Warn("failed to seed admin user", "error", err)
		} else {
			slog.Info("seeded default admin account (no password — run: hobnobcli secrets set-web-password hobnob/web/auth/admin)")
		}
	}

	// Backup API handler.
	backupHandler := api.NewBackupHandler(configFile, serverRoot, dbDriver, cfg.Database.DSN, version, authMethod)

	// Load workflow definitions and create engine.
	workflowEngine := workflows.NewEngine(queries, agentSvc)
	if err := workflowEngine.Load(workflowsDir); err != nil {
		return fmt.Errorf("loading workflows: %w", err)
	}
	slog.Info("workflows loaded", "count", len(workflowEngine.List()), "dir", workflowsDir)

	// Mark interrupted workflows as failed on startup.
	if err := workflows.RecoverInterrupted(ctx, queries); err != nil {
		slog.Warn("failed to recover interrupted workflows", "error", err)
	}

	workflowHandler := api.NewWorkflowHandler(queries, workflowEngine)

	// Initialize AI assistant (optional — disabled by default).
	var aiHandler *api.AIHandler
	var assistant *hobnobai.Assistant
	if cfg.AI.Enabled {
		if err := hobnobai.ValidateConfig(cfg.AI); err != nil {
			return fmt.Errorf("ai config: %w", err)
		}

		aiProvider, aiErr := createAIProvider(ctx, cfg.AI)
		if aiErr != nil {
			slog.Warn("AI assistant disabled: provider init failed", "error", aiErr)
		} else {
			assistant = hobnobai.NewAssistant(aiProvider, queries, configStore, runStore, baseInterval, cfg.AI, docsDir)
			aiHandler = api.NewAIHandler(assistant)
			slog.Info("AI assistant enabled", "provider", aiProvider.Name(), "model", cfg.AI.Model)

			// Pre-warm Ollama model in background so first user request doesn't
			// hit a 60-90 second cold-load delay.
			if ollamaP, ok := aiProvider.(*hobnobai.OllamaProvider); ok {
				go ollamaP.WarmModel(cfg.AI.Model)
			}
		}
	}

	if role == config.RoleSpoke {
		mux, gwErr := api.NewSpokeGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Spanner.SpokeID)
		if gwErr != nil {
			return fmt.Errorf("creating spoke gateway: %w", gwErr)
		}
		licenseHandler.RegisterRoutes(mux)
		doctorHandler.RegisterRoutes(mux)
		spannerHandler.RegisterRoutes(mux)
		peerHandler.RegisterRoutes(mux)
		pushHandler.RegisterRoutes(mux)
		queryHandler.RegisterRoutes(mux)
		workflowHandler.RegisterRoutes(mux)
		exportHandler.RegisterRoutes(mux)
		usersHandler.RegisterRoutes(mux)
		tokensHandler.RegisterRoutes(mux)
		backupHandler.RegisterRoutes(mux)
		if aiHandler != nil {
			aiHandler.RegisterRoutes(mux)
		}
		authProvider.RegisterRoutes(mux)
		httpSrv = &http.Server{
			Addr:    cfg.Server.APIListen,
			Handler: authProvider.Middleware(api.MaxBodyMiddleware(mux)),
		}
	} else {
		// Load CA for bootstrap CSR signing (optional — only if ca_key_file is configured).
		var gwOpts []api.GatewayOption
		if cfg.Server.TLS.CAKeyFile != "" {
			certAuthority, caErr := ca.Load(cfg.Server.TLS.CAFile, cfg.Server.TLS.CAKeyFile)
			if caErr != nil {
				return fmt.Errorf("loading CA for bootstrap: %w", caErr)
			}
			slog.Info("bootstrap CA loaded, CSR signing enabled")

			// Parse cert validity duration.
			certValidity := 365 * 24 * time.Hour
			if cfg.Bootstrap.CertValidity != "" {
				if parsed, pErr := time.ParseDuration(cfg.Bootstrap.CertValidity); pErr == nil {
					certValidity = parsed
				}
			}

			gwOpts = append(gwOpts, func(bh *bootstrap.Handler) {
				bh.SetCA(certAuthority)
				bh.SetRegistry(reg)
				bh.SetTrustedPatterns(cfg.Bootstrap.TrustedPatterns)
				bh.SetCertValidity(certValidity)
				bh.SetDispatcher(dispatcher)
			})

			if len(cfg.Bootstrap.TrustedPatterns) > 0 {
				for _, tp := range cfg.Bootstrap.TrustedPatterns {
					slog.Info("trusted enrollment pattern", "pattern", tp.Pattern, "cidrs", tp.CIDRs)
				}
			}
		}

		gwMux, gwErr := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version, docsDir, assistant, hub, runStore, fleetIndex, authMethod, enforcer, cfg.Publish, peerSvc, auditWriter, gwOpts...)
		if gwErr != nil {
			return fmt.Errorf("creating REST gateway: %w", gwErr)
		}
		licenseHandler.RegisterRoutes(gwMux)
		doctorHandler.RegisterRoutes(gwMux)
		spannerHandler.RegisterRoutes(gwMux)
		peerHandler.RegisterRoutes(gwMux)
		pushHandler.RegisterRoutes(gwMux)
		queryHandler.RegisterRoutes(gwMux)
		workflowHandler.RegisterRoutes(gwMux)
		exportHandler.RegisterRoutes(gwMux)
		usersHandler.RegisterRoutes(gwMux)
		tokensHandler.RegisterRoutes(gwMux)
		backupHandler.RegisterRoutes(gwMux)
		if aiHandler != nil {
			aiHandler.RegisterRoutes(gwMux)
		}
		dbHandler := api.NewDatabaseHandler(sqlDB, queries, dbDriver, cfg.Database.DSN)
		dbHandler.RegisterRoutes(gwMux)
		authProvider.RegisterRoutes(gwMux)
		httpSrv = &http.Server{
			Addr:    cfg.Server.APIListen,
			Handler: authProvider.Middleware(api.MaxBodyMiddleware(gwMux)),
		}
	}

	go func() {
		slog.Info("REST gateway starting", "addr", cfg.Server.APIListen, "tls", true)
		if err := httpSrv.ListenAndServeTLS(cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
			slog.Error("REST gateway error", "error", err)
		}
	}()

	// Start change approval expiry loop (expires pending changes past their timeout).
	if cfg.Publish.ApprovalRequired {
		go api.StartExpiryLoop(ctx, queries)
		slog.Info("change approval workflow enabled", "timeout", stringOrDefault(cfg.Publish.ApprovalTimeout, "24h"))
	}

	if role == config.RoleSpoke {
		// Start spoke status reporter if hub address is configured.
		if cfg.Spanner.HubAddr != "" {
			reporter := spanner.NewReporter(
				cfg.Spanner.SpokeID,
				cfg.Spanner.HubAddr,
				cfg.Spanner.HubFallbackAddrs,
				cfg.Server.TLS.CertFile,
				cfg.Server.TLS.KeyFile,
				cfg.Server.TLS.CAFile,
				fleetIndex,
				30*time.Second,
			)
			go reporter.Run(ctx)
		}

		slog.Info("spoke ready",
			"spoke_id", cfg.Spanner.SpokeID,
			"grpc", cfg.Server.GRPCListen,
			"api", cfg.Server.APIListen,
		)
	} else {
		slog.Info("server ready",
			"role", role,
			"grpc", cfg.Server.GRPCListen,
			"rest", cfg.Server.APIListen,
		)
	}

	// Wait for shutdown signal.
	<-ctx.Done()
	slog.Info("shutting down...")

	grpcSrv.GracefulStop()
	if err := httpSrv.Shutdown(context.Background()); err != nil {
		slog.Error("REST gateway shutdown error", "error", err)
	}

	slog.Info("server stopped")
	return nil
}

// hasFlag checks if a flag is present in os.Args.
func hasFlag(flag string) bool {
	for _, arg := range os.Args[1:] {
		if arg == flag {
			return true
		}
	}
	return false
}

// envOrDefault returns the environment variable value or a default.
// initBundleSigner creates a BundleSigner by generating an ED25519 signing key.
// The key is ephemeral (generated per server start). Returns nil if key generation
// fails (signing is optional — bundles will be unsigned).
func initBundleSigner(cfg *config.ServerConfig) (*isgrpc.BundleSigner, uint32) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		slog.Error("failed to generate bundle signing key", "error", err)
		return nil, 0
	}

	signer := isgrpc.NewBundleSigner(priv)
	slog.Info("bundle signer initialized", "key_id", signer.KeyID())

	// Parse bundle_max_age from config (default 24h = 86400 seconds).
	maxAge := parseDuration(cfg.CheckIn.BundleMaxAge, 24*time.Hour)
	maxAgeSeconds := uint32(maxAge.Seconds())

	return signer, maxAgeSeconds
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stringOrDefault returns s if non-empty, otherwise def.
func stringOrDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// parseDuration parses a duration string, returning def if s is empty or invalid.
// parseRetention parses a retention string like "90d" or "2160h" into a
// time.Duration. Supports "d" suffix for days. Returns 0 if empty or invalid.
func parseRetention(s string) time.Duration {
	if s == "" {
		return 0
	}
	// Support "Nd" shorthand for days.
	if len(s) > 1 && s[len(s)-1] == 'd' {
		var days int
		if _, err := fmt.Sscanf(s, "%dd", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("invalid retention duration, pruning disabled", "value", s, "error", err)
		return 0
	}
	return d
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// applyRuntimeTuning configures Go GC and memory limits from the tuning section.
func applyRuntimeTuning(t config.TuningSection) {
	if t.GOGC != nil {
		old := debug.SetGCPercent(*t.GOGC)
		slog.Info("Go GC tuning applied", "gogc", *t.GOGC, "previous", old)
	}
	if t.MemoryLimit != "" {
		limit := parseMemoryLimit(t.MemoryLimit)
		if limit > 0 {
			old := debug.SetMemoryLimit(limit)
			slog.Info("Go memory limit applied", "limit", t.MemoryLimit, "bytes", limit, "previous", old)
		}
	}
}

// parseMemoryLimit parses a human-readable memory size (e.g. "4GiB", "512MiB",
// "1073741824") into bytes. Supports B, KiB, MiB, GiB, TiB suffixes.
func parseMemoryLimit(s string) int64 {
	if s == "" {
		return 0
	}

	type suffix struct {
		name string
		mult int64
	}
	suffixes := []suffix{
		{"TiB", 1 << 40},
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"B", 1},
	}

	for _, sf := range suffixes {
		if len(s) > len(sf.name) && s[len(s)-len(sf.name):] == sf.name {
			var n int64
			if _, err := fmt.Sscanf(s[:len(s)-len(sf.name)], "%d", &n); err == nil && n > 0 {
				return n * sf.mult
			}
		}
	}

	// Try plain integer (bytes).
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		return n
	}

	slog.Warn("invalid memory_limit, ignoring", "value", s)
	return 0
}

// runStaleChecker periodically scans thralls and fires a thrall.stale SMTP
// persistComplianceSnapshot writes the current compliance counters to the
// compliance_history table for long-term storage (survives server restarts).
func persistComplianceSnapshot(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex) {
	summary := fleetIdx.ComplianceSummary()
	if summary.Total == 0 {
		return
	}
	if err := queries.UpsertComplianceSnapshot(ctx, db.UpsertComplianceSnapshotParams{
		Total:          int64(summary.Total),
		Compliant:      int64(summary.Compliant),
		RecurringDrift: int64(summary.RecurringDrift),
		Errors:         int64(summary.Errors),
		LostContact:    int64(summary.LostContact),
		NoData:         int64(summary.NoData),
	}); err != nil {
		slog.Error("failed to persist compliance snapshot", "error", err)
	}
}

// notification when a thrall transitions from non-stale to stale. A thrall
// is stale if it hasn't checked in within 2x the base interval. The checker
// runs at the base interval frequency.
func runStaleChecker(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex, notifier *hobnobsmtp.Notifier, baseInterval time.Duration) {
	if notifier == nil || !notifier.Enabled() {
		return
	}

	staleThreshold := 2 * baseInterval
	wasStale := make(map[string]bool) // thrall ID → was stale last tick

	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Use FleetIndex when available (zero DB queries).
			if fleetIdx != nil {
				allThralls := fleetIdx.AllThralls()
				nowStale := make(map[string]bool, len(wasStale))
				for _, ts := range allThralls {
					if ts.Revoked || ts.LastSeen.IsZero() {
						continue
					}
					if time.Since(ts.LastSeen) <= staleThreshold {
						continue
					}
					nowStale[ts.ID] = true
					if wasStale[ts.ID] {
						continue
					}
					notifier.Notify(hobnobsmtp.Event{
						Name:      "thrall.stale",
						Timestamp: time.Now().UTC(),
						Thrall: &hobnobsmtp.ThrallInfo{
							ID: ts.ID, Hostname: ts.Hostname, IP: ts.IP,
						},
						Details: map[string]any{
							"last_seen": ts.LastSeen.Format("2006-01-02 15:04:05 UTC"),
						},
					})
				}
				wasStale = nowStale
				continue
			}

			thralls, err := queries.ListThralls(ctx)
			if err != nil {
				slog.Error("stale checker: listing thralls", "error", err)
				continue
			}

			nowStale := make(map[string]bool, len(wasStale))
			for _, t := range thralls {
				if t.Revoked != 0 {
					continue
				}
				if !t.LastSeen.Valid {
					continue
				}
				if time.Since(t.LastSeen.Time) <= staleThreshold {
					continue
				}

				nowStale[t.ID] = true
				if wasStale[t.ID] {
					continue // already notified
				}

				notifier.Notify(hobnobsmtp.Event{
					Name:      "thrall.stale",
					Timestamp: time.Now().UTC(),
					Thrall: &hobnobsmtp.ThrallInfo{
						ID:       t.ID,
						Hostname: t.Hostname,
						IP:       t.IpAddress,
					},
					Details: map[string]any{
						"last_seen": t.LastSeen.Time.Format("2006-01-02 15:04:05 UTC"),
					},
				})
			}
			wasStale = nowStale
		}
	}
}

// runComplianceChecker periodically computes the compliance percentage across
// all active thralls and fires a compliance.threshold SMTP notification when
// it drops below the configured threshold. Only fires once per transition.
func runComplianceChecker(ctx context.Context, queries *db.Queries, runStore db.RunStore, fleetIdx *fleet.FleetIndex, notifier *hobnobsmtp.Notifier, baseInterval time.Duration, threshold int) {
	if notifier == nil || !notifier.Enabled() || threshold <= 0 {
		return
	}

	slog.Info("compliance threshold checker started", "threshold", threshold)

	wasBelowThreshold := false
	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Use FleetIndex when available (O(1) compliance summary).
			if fleetIdx != nil {
				summary := fleetIdx.ComplianceSummary()
				total := summary.Total
				if total == 0 {
					continue
				}
				pct := (summary.Compliant * 100) / total
				belowNow := pct < threshold

				if belowNow && !wasBelowThreshold {
					notifier.Notify(hobnobsmtp.Event{
						Name:      "compliance.threshold",
						Timestamp: time.Now().UTC(),
						Details: map[string]any{
							"compliant_pct": pct,
							"threshold":     threshold,
							"compliant":     summary.Compliant,
							"total":         total,
						},
					})
				}
				wasBelowThreshold = belowNow
				continue
			}

			thralls, err := queries.ListThralls(ctx)
			if err != nil {
				slog.Error("compliance checker: listing thralls", "error", err)
				continue
			}

			// Collect active thrall IDs.
			var ids []string
			thrallMap := make(map[string]db.Thrall)
			for _, t := range thralls {
				if t.Revoked != 0 {
					continue
				}
				ids = append(ids, t.ID)
				thrallMap[t.ID] = t
			}
			if len(ids) == 0 {
				continue
			}

			latestRuns, err := runStore.LatestRunPerThrall(ctx, ids)
			if err != nil {
				slog.Error("compliance checker: fetching latest runs", "error", err)
				continue
			}

			compliantCount := 0
			for _, id := range ids {
				t := thrallMap[id]
				var recent []compliance.RunInfo
				if run, ok := latestRuns[id]; ok {
					recent = []compliance.RunInfo{{
						Status:       run.Status,
						ChangedCount: int64(run.ChangedCount),
					}}
				}
				lastSeen := time.Time{}
				if t.LastSeen.Valid {
					lastSeen = t.LastSeen.Time
				}
				if compliance.ComputeStatus(recent, lastSeen, baseInterval) == "compliant" {
					compliantCount++
				}
			}

			pct := (compliantCount * 100) / len(ids)
			belowNow := pct < threshold

			if belowNow && !wasBelowThreshold {
				notifier.Notify(hobnobsmtp.Event{
					Name:      "compliance.threshold",
					Timestamp: time.Now().UTC(),
					Details: map[string]any{
						"compliant_pct": pct,
						"threshold":     threshold,
						"compliant":     compliantCount,
						"total":         len(ids),
					},
				})
			}
			wasBelowThreshold = belowNow
		}
	}
}

// createAIProvider creates the LLM provider based on the AI config section.
// Returns an error if the provider cannot be initialized (e.g. Ollama unreachable).
func createAIProvider(ctx context.Context, cfg config.AISection) (hobnobai.Provider, error) {
	switch cfg.Provider {
	case "ollama", "":
		p := hobnobai.NewOllamaProvider(cfg.BaseURL)
		if err := p.EnsureModel(ctx, cfg.Model); err != nil {
			return nil, fmt.Errorf("ollama: %w", err)
		}
		return p, nil
	case "claude":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("claude provider requires ai.api_key")
		}
		return hobnobai.NewClaudeProvider(cfg.APIKey), nil
	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("openai provider requires ai.api_key")
		}
		return hobnobai.NewOpenAIProvider(cfg.APIKey), nil
	case "openai-compat":
		return hobnobai.NewOpenAICompatProvider(cfg.BaseURL, cfg.APIKey), nil
	default:
		return nil, fmt.Errorf("unknown AI provider: %s", cfg.Provider)
	}
}

// createAuthProvider builds the appropriate auth provider from config.
func createAuthProvider(ctx context.Context, cfg *config.ServerConfig, sqlDB *sql.DB, queries *db.Queries, secretsProvider secrets.Provider) (hobnobauth.Provider, error) {
	method := cfg.Auth.Method
	if method == "" {
		method = "basic"
	}

	switch method {
	case "basic":
		sessions := hobnobauth.NewSessionManager(sqlDB)
		userStore := hobnobauth.NewUserStore(queries)
		tokenStore := hobnobauth.NewTokenStore(queries)
		return hobnobauth.NewBasicProvider(sessions, userStore, tokenStore, secretsProvider), nil

	case "oidc":
		sessions := hobnobauth.NewSessionManager(sqlDB)
		userStore := hobnobauth.NewUserStore(queries)
		tokenStore := hobnobauth.NewTokenStore(queries)
		return hobnobauth.NewOIDCProvider(ctx, hobnobauth.OIDCConfig{
			Issuer:       cfg.Auth.OIDC.Issuer,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
		}, sessions, userStore, tokenStore)

	case "isowebauth":
		sessions := hobnobauth.NewSessionManager(sqlDB)
		userStore := hobnobauth.NewUserStore(queries)
		tokenStore := hobnobauth.NewTokenStore(queries)
		return hobnobauth.NewIsowebauthProvider(hobnobauth.IsowebauthConfig{
			Namespace: cfg.Auth.Isowebauth.Namespace,
		}, sessions, userStore, tokenStore)

	default:
		return nil, fmt.Errorf("unknown auth method: %q (must be basic, oidc, or isowebauth)", method)
	}
}

// buildPeerTLS creates a *tls.Config for the primary's outbound connections to peers.
// Uses the same server cert/key/CA as the main gRPC server (mutual TLS).
func buildPeerTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading cert/key: %w", err)
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
		RootCAs:      caPool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

