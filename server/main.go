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
	"math"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
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

	pb "github.com/daniel-alexander4/vigo/gen/proto/vigo"
	vigoai "github.com/daniel-alexander4/vigo/server/ai"
	"github.com/daniel-alexander4/vigo/server/api"
	"github.com/daniel-alexander4/vigo/ui"
	"github.com/daniel-alexander4/vigo/server/audit"
	vigoauth "github.com/daniel-alexander4/vigo/server/auth"
	"github.com/daniel-alexander4/vigo/server/backup"
	"github.com/daniel-alexander4/vigo/server/bootstrap"
	"github.com/daniel-alexander4/vigo/server/ca"
	"github.com/daniel-alexander4/vigo/server/convergence"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/grc"
	"github.com/daniel-alexander4/vigo/server/publish"
	"github.com/daniel-alexander4/vigo/server/db"
	"github.com/daniel-alexander4/vigo/server/spanner"
	"github.com/daniel-alexander4/vigo/server/hardening"
	"github.com/daniel-alexander4/vigo/server/fleet"
	isgrpc "github.com/daniel-alexander4/vigo/server/grpc"
	"github.com/daniel-alexander4/vigo/server/license"
	"github.com/daniel-alexander4/vigo/server/metrics"
	"github.com/daniel-alexander4/vigo/server/registry"
	"github.com/daniel-alexander4/vigo/server/secrets"
	vigosmtp "github.com/daniel-alexander4/vigo/server/smtp"
	"github.com/daniel-alexander4/vigo/server/task"
	"github.com/daniel-alexander4/vigo/server/peer"
	"github.com/daniel-alexander4/vigo/server/risk"
	"github.com/daniel-alexander4/vigo/server/traits"
	"github.com/daniel-alexander4/vigo/server/watcher"
	"github.com/daniel-alexander4/vigo/server/webhook"
	"github.com/daniel-alexander4/vigo/server/workflows"
)

// version is set at build time via -ldflags="-X main.version=1.2.3".
// The default should match the VERSION file at the repo root.
var version = "0.1.3"

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
	if hasFlag("--version") || hasFlag("-v") {
		fmt.Println("vigosrv " + version)
		return nil
	}

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
	configFile := envOrDefault("VIGO_CONFIG_FILE", "server.yaml")
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
	migrationsDir := envOrDefault("VIGO_MIGRATIONS_DIR", stringOrDefault(cfg.Paths.Migrations, "migrations"))
	traitsDir := envOrDefault("VIGO_CUSTOM_TRAITS_DIR", stringOrDefault(cfg.Paths.CustomTraits, "custom-traits"))
	docsDir := envOrDefault("VIGO_DOCS_DIR", stringOrDefault(cfg.Paths.Docs, "docs"))
	taskDir := envOrDefault("VIGO_TASK_DIR", stringOrDefault(cfg.Paths.Tasks, "stack/tasks"))
	workflowsDir := envOrDefault("VIGO_WORKFLOWS_DIR", stringOrDefault(cfg.Paths.Workflows, "stack/workflows"))
	agentDir := envOrDefault("VIGO_AGENT_DIR", stringOrDefault(cfg.Paths.AgentDist, "dist/agent"))
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

	// One-time fixup: rename compliance_history → convergence_history if the
	// DB was created before the convergence rename. Safe to run on fresh DBs
	// (compliance_history won't exist, so the rename is a no-op).
	migrateConvergenceHistory(sqlDB)

	// Initialize query layer early — needed for license envoy count.
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
	//
	// license.mode controls counting:
	//   "local" (default): count enrolled nodes on this server only.
	//   "aggregate": hub sums node counts across all spanner spokes.
	//                spokes skip enforcement entirely.
	licenseMode := cfg.License.Mode
	if licenseMode == "" {
		licenseMode = "local"
	}

	// In aggregate mode, spokes skip enforcement — the hub handles it.
	if licenseMode == "aggregate" && role == config.RoleSpoke {
		slog.Info("license mode aggregate: spoke skips enforcement (hub enforces)")
	}

	// Hub variable is declared here but assigned later (after config store).
	// The closure captures the pointer and reads it at check time (hourly),
	// which is well after hub initialization completes.
	var hub *spanner.Hub

	envoyCounter := func(ctx context.Context) (int, error) {
		// Aggregate mode on hub: sum node counts from all spokes.
		if licenseMode == "aggregate" && hub != nil {
			total := 0
			for _, s := range hub.SpokeStatuses(ctx) {
				total += int(s.EnvoyCount)
			}
			// Also count nodes managed directly by the hub itself.
			envoys, err := queries.ListEnvoys(ctx)
			if err == nil {
				for _, t := range envoys {
					if t.Revoked == 0 {
						total++
					}
				}
			}
			return total, nil
		}

		// Local mode (default): count enrolled nodes on this server.
		envoys, err := queries.ListEnvoys(ctx)
		if err != nil {
			return 0, err
		}
		count := 0
		for _, t := range envoys {
			if t.Revoked == 0 {
				count++
			}
		}
		return count, nil
	}

	// Spokes in aggregate mode use a no-op enforcer that always passes.
	var enforcer *license.Enforcer
	if licenseMode == "aggregate" && role == config.RoleSpoke {
		enforcer, err = license.NewEnforcer(licenseDir, licenseGraceDir, cfg.Server.TLS.CAFile,
			func(_ context.Context) (int, error) { return 0, nil }, // always zero — hub enforces
			func() { cancel() })
	} else {
		enforcer, err = license.NewEnforcer(licenseDir, licenseGraceDir, cfg.Server.TLS.CAFile, envoyCounter, func() { cancel() })
	}
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

	// Wire audit writer into auth provider for login/logout/failed-auth logging.
	switch p := authProvider.(type) {
	case *vigoauth.BasicProvider:
		p.SetAuditWriter(auditWriter)
	case *vigoauth.OIDCProvider:
		p.SetAuditWriter(auditWriter)
	case *vigoauth.IsowebauthProvider:
		p.SetAuditWriter(auditWriter)
	}

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
	var smtpEvents map[string]*vigosmtp.EventOverride
	if len(cfg.SMTP.Events) > 0 {
		smtpEvents = make(map[string]*vigosmtp.EventOverride, len(cfg.SMTP.Events))
		for name, ec := range cfg.SMTP.Events {
			smtpEvents[name] = &vigosmtp.EventOverride{
				Recipients: ec.Recipients,
			}
		}
	}

	var digestCfg *vigosmtp.DigestConfig
	if cfg.SMTP.Digest.Interval != "" {
		if interval, pErr := time.ParseDuration(cfg.SMTP.Digest.Interval); pErr == nil && interval > 0 {
			digestEvents := make(map[string]bool, len(cfg.SMTP.Digest.Events))
			for _, e := range cfg.SMTP.Digest.Events {
				digestEvents[e] = true
			}
			digestCfg = &vigosmtp.DigestConfig{
				Interval: interval,
				Events:   digestEvents,
			}
		}
	}

	smtpNotifier := vigosmtp.NewNotifier(vigosmtp.Config{
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
		smtpNotifier.Notify(vigosmtp.Event{
			Name:      "config.reload.failure",
			Timestamp: time.Now().UTC(),
			Details:   map[string]any{"error": err.Error()},
		})
	})

	// Connect to spokes (hub mode only).
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

	// Load task definitions.
	taskStore := task.NewStore()
	if err := taskStore.Load(taskDir); err != nil {
		return fmt.Errorf("loading task definitions: %w", err)
	}
	slog.Info("task definitions loaded", "count", len(taskStore.List()), "dir", taskDir)

	// Create and rehydrate FleetIndex.
	fleetIndex := fleet.NewFleetIndex()
	if err := fleet.Rehydrate(ctx, fleetIndex, queries, runStore, baseInterval); err != nil {
		return fmt.Errorf("rehydrating fleet index: %w", err)
	}

	// Self-enroll: the server registers itself as its own envoy so it can
	// manage its own state (backups, monitoring, etc.) via standard modules.
	// Create async run batcher for delta events.
	runBatcher := fleet.NewRunBatcher(runStore)

	// Initialize bundle signer for compiled promises.
	bundleSigner, bundleMaxAge := initBundleSigner(cfg)

	// Probe the agent binary version for auto-update comparison.
	// This is the actual version baked into the agent binary, not the server version.
	agentVersion := bootstrap.ProbeAgentVersion(agentDir)
	if agentVersion == "" {
		agentVersion = version // fallback to server version if no agent binary found
	}
	slog.Info("agent version for auto-update", "agent_version", agentVersion)

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
		TaskStore:            taskStore,
		FleetIndex:           fleetIndex,
		RunBatcher:           runBatcher,
		BundleSigner:        bundleSigner,
		BundleMaxAge:        bundleMaxAge,
		FallbackServers:     fallbackServers,
		AuditWriter:         auditWriter,
		ServerVersion:       agentVersion,
		AgentBinAvailable: func() bool {
			for _, goos := range []string{"linux", "darwin", "freebsd", "openbsd", "netbsd", "illumos", "windows"} {
				for _, goarch := range []string{"amd64", "arm64"} {
					if _, err := os.Stat(bootstrap.BinaryPath(agentDir, goos, goarch)); err == nil {
						return true
					}
				}
			}
			return false
		},
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

		pb.RegisterVigoPeerServer(grpcSrv, peerSvc)
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

	// Start task timeout reaper — marks expired task runs as timed out.
	taskReaper := task.NewReaper(queries)
	go taskReaper.Start(ctx)

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
				smtpNotifier.Notify(vigosmtp.Event{
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
	var fleetSource metrics.FleetSource
	if fleetIndex != nil {
		fleetSource = &metricsFleetAdapter{fleetIndex: fleetIndex}
	}
	metricsCollector := metrics.NewCollector(queries, sqlDB, fleetSource, baseInterval)
	go metricsCollector.Run(ctx, metricsInterval)

	// Start stale envoy checker — fires envoy.stale SMTP events on transition.
	go runStaleChecker(ctx, queries, fleetIndex, smtpNotifier, baseInterval)

	// Start compliance sparkline snapshotting (daily snapshot for the in-memory ring buffer + DB).
	go func() {
		// Take an initial snapshot immediately so the sparkline isn't empty on startup.
		fleetIndex.SnapshotConvergence()
		persistConvergenceSnapshot(ctx, queries, fleetIndex)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fleetIndex.SnapshotConvergence()
				persistConvergenceSnapshot(ctx, queries, fleetIndex)
			}
		}
	}()

	// Start daily risk posture snapshotting.
	go func() {
		persistRiskSnapshot(ctx, queries, fleetIndex, baseInterval)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				persistRiskSnapshot(ctx, queries, fleetIndex, baseInterval)
			}
		}
	}()

	// Start daily standards compliance snapshotting.
	go func() {
		persistComplianceSnapshot(ctx, queries, fleetIndex, configStore)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				persistComplianceSnapshot(ctx, queries, fleetIndex, configStore)
			}
		}
	}()

	// Start MTTR stats computation (daily, on same cadence as compliance snapshots).
	go func() {
		computeAndStoreMTTR(ctx, queries)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				computeAndStoreMTTR(ctx, queries)
			}
		}
	}()

	// Start framework-aware drift alerter (background, not hot path).
	// Scans recent runs every 5 minutes, enriches drift events with framework/control context.
	go runFrameworkDriftAlerter(ctx, queries, configStore, smtpNotifier, fleetIndex, baseInterval)

	// Start scheduled evidence email delivery (weekly/monthly).
	if cfg.SMTP.Evidence.Schedule != "" {
		go runEvidenceEmailScheduler(ctx, cfg.SMTP.Evidence, smtpNotifier, queries, fleetIndex)
	}

	// Expire any overdue compliance waivers daily.
	go func() {
		for {
			_ = queries.ExpireComplianceWaivers(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
			}
		}
	}()

	// Start SMTP digest flush loop (if configured).
	go smtpNotifier.Start(ctx)

	// Start compliance threshold checker (if configured).
	complianceThreshold := 0
	if cfg.SMTP.Events != nil {
		if ec, ok := cfg.SMTP.Events["convergence.threshold"]; ok {
			complianceThreshold = ec.Threshold
		}
	}
	if complianceThreshold > 0 {
		go runComplianceChecker(ctx, queries, runStore, fleetIndex, smtpNotifier, baseInterval, complianceThreshold)
	go runSecurityPostureSnapshot(ctx, queries, fleetIndex)
	}

	// Start GRC evidence pusher if integrations are configured.
	if len(cfg.GRC.Integrations) > 0 {
		grcPusher := grc.NewPusher(cfg.GRC.Integrations, fleetIndex, configStore)
		go grcPusher.Start(ctx)
	}

	// Start NVD CVE enrichment if enabled.
	if cfg.Risk.NVD.Enabled {
		nvdKey := cfg.Risk.NVD.APIKey
		nvdCache := &nvdCacheAdapter{queries: queries}
		enricher := risk.NewNVDEnricher(nvdKey, nvdCache)
		go enricher.Run(ctx, func() []string {
			return collectFleetCVEIDs(fleetIndex)
		})
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
	taskHandler := api.NewTaskHandler(queries, agentSvc, cfg.Task.RequireDefinition)
	taskHandler.SetAuditWriter(auditWriter)
	queryHandler := api.NewQueryHandler(agentSvc)
	exportHandler := api.NewExportHandler(queries, fleetIndex, configStore, cfg.Export)
	reportHandler := api.NewReportHandler(queries, fleetIndex, configStore, version)
	cveHandler := api.NewCVEHandler(fleetIndex)
	remediationHandler := api.NewRemediationHandler(queries, fleetIndex)
	remediationHandler.SetAuditWriter(auditWriter)
	peerHandler := api.NewPeerHandler(peerSvc)

	// Auth-related API handlers.
	userStore := vigoauth.NewUserStore(queries)
	tokenStore := vigoauth.NewTokenStore(queries)
	usersHandler := api.NewUsersHandler(userStore, authMethod)
	usersHandler.SetAuditWriter(auditWriter)
	usersHandler.SetSecretsProvider(provider)
	tokensHandler := api.NewTokensHandler(tokenStore, authMethod)
	tokensHandler.SetAuditWriter(auditWriter)

	// Seed default admin account on first startup (no password — must be set via CLI).
	if count, err := userStore.CountUsers(ctx); err == nil && count == 0 {
		if _, err := userStore.Create(ctx, db.InsertUserParams{
			Username: "admin",
			Role:     "admin",
		}); err != nil {
			slog.Warn("failed to seed admin user", "error", err)
		} else {
			slog.Info("seeded default admin account (no password — run: vigogo secrets set-web-password vigo/web/auth/admin)")
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

	// Wire workflow engine for auto-triggered workflows on trait changes.
	agentSvc.SetWorkflowTrigger(workflowEngine)

	// Initialize AI assistant (optional — disabled by default).
	var aiHandler *api.AIHandler
	var assistant *vigoai.Assistant
	if cfg.AI.Enabled {
		if err := vigoai.ValidateConfig(cfg.AI); err != nil {
			return fmt.Errorf("ai config: %w", err)
		}

		aiProvider, aiErr := createAIProvider(ctx, cfg.AI)
		if aiErr != nil {
			slog.Warn("AI assistant disabled: provider init failed", "error", aiErr)
		} else {
			assistant = vigoai.NewAssistant(aiProvider, queries, configStore, runStore, baseInterval, cfg.AI, docsDir)
			aiHandler = api.NewAIHandler(assistant)
			slog.Info("AI assistant enabled", "provider", aiProvider.Name(), "model", cfg.AI.Model)

			// Pre-warm Ollama model in background so first user request doesn't
			// hit a 60-90 second cold-load delay.
			if ollamaP, ok := aiProvider.(*vigoai.OllamaProvider); ok {
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
		taskHandler.RegisterRoutes(mux)
		queryHandler.RegisterRoutes(mux)
		workflowHandler.RegisterRoutes(mux)
		exportHandler.RegisterRoutes(mux)
		reportHandler.RegisterRoutes(mux)
		remediationHandler.RegisterRoutes(mux)
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

		gwMux, uiHandler, gwErr := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version, docsDir, assistant, hub, runStore, fleetIndex, authMethod, enforcer, cfg.Publish, peerSvc, auditWriter, gwOpts...)
		if gwErr != nil {
			return fmt.Errorf("creating REST gateway: %w", gwErr)
		}
		// Wire branding and CVE searcher into UI handler.
		uiHandler.SetBranding(cfg.Branding)
		uiHandler.SetCVESearcher(&cveSearchAdapter{cveHandler})

		// Wire report builder into UI handler for HTMX fragment rendering.
		uiHandler.SetReportBuilder(&ui.ReportBuilder{
			Executive: func(r *http.Request) (any, error) {
				return reportHandler.BuildExecutiveReport(r)
			},
			Framework: func(r *http.Request, slug string) (any, error) {
				return reportHandler.BuildFrameworkReport(r, slug)
			},
		})
		uiHandler.SetIntegrationsConfig(cfg.Integrations, cfg.GRC)
		licenseHandler.RegisterRoutes(gwMux)
		doctorHandler.RegisterRoutes(gwMux)
		spannerHandler.RegisterRoutes(gwMux)
		peerHandler.RegisterRoutes(gwMux)
		taskHandler.RegisterRoutes(gwMux)
		queryHandler.RegisterRoutes(gwMux)
		workflowHandler.RegisterRoutes(gwMux)
		exportHandler.RegisterRoutes(gwMux)
		reportHandler.RegisterRoutes(gwMux)
		cveHandler.RegisterRoutes(gwMux)
		remediationHandler.RegisterRoutes(gwMux)
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
			reporter.SetConfigVersion(configStore.Version)
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

// runStaleChecker periodically scans envoys and fires a envoy.stale SMTP
// persistConvergenceSnapshot writes the current convergence counters to the
// compliance_history table for long-term storage (survives server restarts).
func persistConvergenceSnapshot(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex) {
	summary := fleetIdx.ConvergenceSummary()
	if summary.Total == 0 {
		return
	}
	if err := queries.UpsertConvergenceSnapshot(ctx, db.UpsertConvergenceSnapshotParams{
		Total:          int64(summary.Total),
		Converged:      int64(summary.Converged),
		RecurringDrift: int64(summary.RecurringDrift),
		Errors:         int64(summary.Errors),
		LostContact:    int64(summary.LostContact),
		NoData:         int64(summary.NoData),
	}); err != nil {
		slog.Error("failed to persist compliance snapshot", "error", err)
	}
}

// persistRiskSnapshot computes the fleet-wide risk score and writes a daily
// snapshot to the risk_posture_history table.
func persistRiskSnapshot(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex, baseInterval time.Duration) {
	allEnvoys := fleetIdx.AllEnvoys()
	if len(allEnvoys) == 0 {
		return
	}
	inputs := make([]risk.EnvoyInput, len(allEnvoys))
	for i, es := range allEnvoys {
		inputs[i] = risk.EnvoyInput{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
		}
	}
	fr := risk.ScoreFleet(inputs, baseInterval, 0)
	if err := queries.UpsertRiskPostureSnapshot(ctx, db.UpsertRiskPostureSnapshotParams{
		AvgScore:     int64(fr.AvgScore),
		MaxScore:     int64(fr.MaxScore),
		DistLow:      int64(fr.Distribution[risk.LevelLow]),
		DistMedium:   int64(fr.Distribution[risk.LevelMedium]),
		DistHigh:     int64(fr.Distribution[risk.LevelHigh]),
		DistCritical: int64(fr.Distribution[risk.LevelCritical]),
		ScoredEnvoys: int64(fr.ScoredEnvoys),
		TotalEnvoys:  int64(fr.TotalEnvoys),
	}); err != nil {
		slog.Error("failed to persist risk snapshot", "error", err)
	}
}

// metricsFleetAdapter wraps fleet.FleetIndex to satisfy metrics.FleetSource,
// breaking the metrics→fleet import cycle.
type metricsFleetAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *metricsFleetAdapter) ConvergenceSummary() metrics.ConvergenceSnapshot {
	s := a.fleetIndex.ConvergenceSummary()
	return metrics.ConvergenceSnapshot{
		Converged:      s.Converged,
		RecurringDrift: s.RecurringDrift,
		Errors:         s.Errors,
		LostContact:    s.LostContact,
		NoData:         s.NoData,
	}
}

func (a *metricsFleetAdapter) AllEnvoySnapshots() []metrics.EnvoySnapshot {
	all := a.fleetIndex.AllEnvoys()
	snapshots := make([]metrics.EnvoySnapshot, len(all))
	for i, es := range all {
		snapshots[i] = metrics.EnvoySnapshot{
			ID:                es.ID,
			Hostname:          es.Hostname,
			Revoked:           es.Revoked,
			LastSeen:          es.LastSeen,
			ConvergenceStatus: es.ConvergenceStatus,
			Traits:            es.Traits,
		}
	}
	return snapshots
}

// nvdCacheAdapter adapts db.Queries to the risk.CVSSCache interface.
type nvdCacheAdapter struct {
	queries *db.Queries
}

func (a *nvdCacheAdapter) GetCVSS(ctx context.Context, cveID string) (*risk.CVSSResult, error) {
	row, err := a.queries.GetCVECVSS(ctx, cveID)
	if err != nil {
		return nil, err
	}
	// Skip if stale (> 7 days).
	if time.Since(row.FetchedAt) > 7*24*time.Hour {
		return nil, nil
	}
	return &risk.CVSSResult{
		CVEID:      row.CveID,
		CVSSScore:  row.CvssScore,
		CVSSVector: row.CvssVector,
		Severity:   row.Severity,
	}, nil
}

func (a *nvdCacheAdapter) UpsertCVSS(ctx context.Context, r risk.CVSSResult) error {
	return a.queries.UpsertCVECVSS(ctx, db.UpsertCVECVSSParams{
		CveID:      r.CVEID,
		CvssScore:  r.CVSSScore,
		CvssVector: r.CVSSVector,
		Severity:   r.Severity,
	})
}

// collectFleetCVEIDs extracts unique CVE IDs from the unified
// security_scan.cves.N.id traits across all envoys.
func collectFleetCVEIDs(fleetIdx *fleet.FleetIndex) []string {
	seen := make(map[string]bool)
	for _, es := range fleetIdx.AllEnvoys() {
		if es.Revoked || es.Traits == nil {
			continue
		}
		for k, v := range es.Traits {
			if strings.HasPrefix(k, traits.PrefixCVEs) && strings.HasSuffix(k, "."+traits.CVEFieldID) && strings.HasPrefix(v, "CVE-") {
				seen[v] = true
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// computeAndStoreMTTR scans yesterday's failed/partial runs, finds their
// resolutions, and writes aggregate MTTR stats. Runs daily off the hot path.
func computeAndStoreMTTR(ctx context.Context, queries *db.Queries) {
	yesterdayTime := time.Now().AddDate(0, 0, -1)
	yesterday := yesterdayTime.Format("2006-01-02")

	failed, err := queries.ListFailedRunsForDate(ctx, yesterdayTime)
	if err != nil {
		slog.Error("mttr: failed to list failed runs", "date", yesterday, "error", err)
		return
	}

	var durations []int64
	resolved := 0
	open := 0

	for _, f := range failed {
		next, err := queries.GetNextSuccessfulRun(ctx, db.GetNextSuccessfulRunParams{
			EnvoyID:   f.EnvoyID,
			StartedAt: f.StartedAt,
		})
		if err != nil {
			open++
			continue
		}
		dur := next.StartedAt.Sub(f.StartedAt).Seconds()
		durations = append(durations, int64(dur))
		resolved++
	}

	var avgSec, p50Sec, p95Sec int64
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var sum int64
		for _, d := range durations {
			sum += d
		}
		avgSec = sum / int64(len(durations))
		p50Sec = durations[len(durations)/2]
		p95idx := int(float64(len(durations)) * 0.95)
		if p95idx >= len(durations) {
			p95idx = len(durations) - 1
		}
		p95Sec = durations[p95idx]
	}

	if err := queries.UpsertMTTRStats(ctx, db.UpsertMTTRStatsParams{
		RecordedAt:     yesterday,
		TotalIncidents: int64(len(failed)),
		Resolved:       int64(resolved),
		MttrSeconds:    avgSec,
		P50Seconds:     p50Sec,
		P95Seconds:     p95Sec,
		OpenIncidents:  int64(open),
	}); err != nil {
		slog.Error("mttr: failed to persist stats", "date", yesterday, "error", err)
	} else if len(failed) > 0 {
		slog.Info("mttr stats computed", "date", yesterday, "incidents", len(failed), "resolved", resolved, "mttr_avg_s", avgSec)
	}
}

// runFrameworkDriftAlerter periodically scans recent drift events and sends
// enriched notifications that include the affected framework controls.
// Runs off the hot path — the check-in handler sends basic drift.detected;
// this job adds framework context asynchronously.
func runFrameworkDriftAlerter(ctx context.Context, queries *db.Queries, configStore *config.ConfigStore, notifier *vigosmtp.Notifier, fleetIdx *fleet.FleetIndex, interval time.Duration) {
	if notifier == nil || !notifier.Enabled() || configStore == nil {
		return
	}

	// Track which runs we've already alerted on.
	lastChecked := time.Now()

	alertInterval := interval * 2
	if alertInterval < time.Minute {
		alertInterval = time.Minute
	}

	ticker := time.NewTicker(alertInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Find runs with changes since last check.
			runs, err := queries.ListAllRuns(ctx, 50)
			if err != nil {
				continue
			}

			envoyMap := make(map[string]string) // envoy ID → hostname
			if fleetIdx != nil {
				for _, ts := range fleetIdx.AllEnvoys() {
					envoyMap[ts.ID] = ts.Hostname
				}
			}

			for _, r := range runs {
				if r.StartedAt.Before(lastChecked) {
					break
				}
				if r.ChangedCount == 0 {
					continue
				}

				hostname := envoyMap[r.EnvoyID]
				if hostname == "" {
					continue
				}

				// Look up which frameworks/controls are affected.
				trace := configStore.Trace(hostname)
				if !trace.Matched || len(trace.ComplianceTags) == 0 {
					continue
				}

				var affected []string
				for _, ct := range trace.ComplianceTags {
					for fw, ids := range ct.Frameworks {
						for _, id := range ids {
							affected = append(affected, fw+": "+id)
						}
					}
				}

				if len(affected) == 0 {
					continue
				}

				// Truncate to avoid huge emails.
				display := strings.Join(affected, ", ")
				if len(display) > 200 {
					display = display[:200] + "..."
				}

				notifier.Notify(vigosmtp.Event{
					Name:      "convergence.drift",
					Timestamp: r.StartedAt,
					Envoy: &vigosmtp.EnvoyInfo{
						ID:       r.EnvoyID,
						Hostname: hostname,
					},
					Details: map[string]any{
						"run_id":            r.ID,
						"changed":           r.ChangedCount,
						"affected_controls": display,
					},
				})
			}

			lastChecked = time.Now()
		}
	}
}

// runEvidenceEmailScheduler sends periodic compliance evidence emails.
func runEvidenceEmailScheduler(ctx context.Context, cfg config.EvidenceEmailConfig, notifier *vigosmtp.Notifier, queries *db.Queries, fleetIdx *fleet.FleetIndex) {
	if notifier == nil || !notifier.Enabled() {
		return
	}

	var interval time.Duration
	switch cfg.Schedule {
	case "weekly":
		interval = 7 * 24 * time.Hour
	case "monthly":
		interval = 30 * 24 * time.Hour
	default:
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if fleetIdx == nil {
				continue
			}

			summary := fleetIdx.ConvergenceSummary()
			total := summary.Total
			pct := float64(0)
			if total > 0 {
				pct = float64(summary.Converged) / float64(total) * 100
			}

			// Get MTTR for the period.
			to := time.Now().Format("2006-01-02")
			from := time.Now().Add(-interval).Format("2006-01-02")
			mttrDisplay := "-"
			if stats, err := queries.ListMTTRStats(ctx, db.ListMTTRStatsParams{
				FromRecordedAt: from,
				ToRecordedAt:   to,
			}); err == nil && len(stats) > 0 {
				var sumMTTR, resolved int64
				for _, s := range stats {
					sumMTTR += s.MttrSeconds * s.Resolved
					resolved += s.Resolved
				}
				if resolved > 0 {
					avg := sumMTTR / resolved
					if avg < 60 {
						mttrDisplay = fmt.Sprintf("%ds", avg)
					} else if avg < 3600 {
						mttrDisplay = fmt.Sprintf("%dm %ds", avg/60, avg%60)
					} else {
						mttrDisplay = fmt.Sprintf("%dh %dm", avg/3600, (avg%3600)/60)
					}
				}
			}

			recipients := cfg.Recipients
			if len(recipients) == 0 {
				// Fall back to default SMTP recipients (handled by notifier).
				recipients = nil
			}

			notifier.Notify(vigosmtp.Event{
				Name:      "compliance.evidence",
				Timestamp: time.Now().UTC(),
				Details: map[string]any{
					"schedule":       cfg.Schedule,
					"period_from":    from,
					"period_to":      to,
					"compliance_pct": fmt.Sprintf("%.1f", pct),
					"converged":      summary.Converged,
					"total":          total,
					"drifted":        summary.RecurringDrift,
					"errors":         summary.Errors,
					"mttr":           mttrDisplay,
					"evidence_url":   "/compliance/evidence?from=" + from + "&to=" + to,
				},
			})

			slog.Info("compliance evidence email sent",
				"schedule", cfg.Schedule,
				"compliance_pct", fmt.Sprintf("%.1f%%", pct),
				"period", from+" to "+to,
			)
		}
	}
}

// notification when a envoy transitions from non-stale to stale. A envoy
// is stale if it hasn't checked in within 2x the base interval. The checker
// runs at the base interval frequency.
func runStaleChecker(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex, notifier *vigosmtp.Notifier, baseInterval time.Duration) {
	if notifier == nil || !notifier.Enabled() {
		return
	}

	staleThreshold := 2 * baseInterval
	wasStale := make(map[string]bool) // envoy ID → was stale last tick

	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Use FleetIndex when available (zero DB queries).
			if fleetIdx != nil {
				allEnvoys := fleetIdx.AllEnvoys()
				nowStale := make(map[string]bool, len(wasStale))
				for _, ts := range allEnvoys {
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
					notifier.Notify(vigosmtp.Event{
						Name:      "envoy.stale",
						Timestamp: time.Now().UTC(),
						Envoy: &vigosmtp.EnvoyInfo{
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

			envoys, err := queries.ListEnvoys(ctx)
			if err != nil {
				slog.Error("stale checker: listing envoys", "error", err)
				continue
			}

			nowStale := make(map[string]bool, len(wasStale))
			for _, t := range envoys {
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

				notifier.Notify(vigosmtp.Event{
					Name:      "envoy.stale",
					Timestamp: time.Now().UTC(),
					Envoy: &vigosmtp.EnvoyInfo{
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

// runComplianceChecker periodically computes the convergence percentage across
// all active envoys and fires a convergence.threshold SMTP notification when
// it drops below the configured threshold. Only fires once per transition.
func runComplianceChecker(ctx context.Context, queries *db.Queries, runStore db.RunStore, fleetIdx *fleet.FleetIndex, notifier *vigosmtp.Notifier, baseInterval time.Duration, threshold int) {
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
				summary := fleetIdx.ConvergenceSummary()
				total := summary.Total
				if total == 0 {
					continue
				}
				pct := (summary.Converged * 100) / total
				belowNow := pct < threshold

				if belowNow && !wasBelowThreshold {
					notifier.Notify(vigosmtp.Event{
						Name:      "convergence.threshold",
						Timestamp: time.Now().UTC(),
						Details: map[string]any{
							"compliant_pct": pct,
							"threshold":     threshold,
							"converged":     summary.Converged,
							"total":         total,
						},
					})
				}
				wasBelowThreshold = belowNow
				continue
			}

			envoys, err := queries.ListEnvoys(ctx)
			if err != nil {
				slog.Error("compliance checker: listing envoys", "error", err)
				continue
			}

			// Collect active envoy IDs.
			var ids []string
			envoyMap := make(map[string]db.Envoy)
			for _, t := range envoys {
				if t.Revoked != 0 {
					continue
				}
				ids = append(ids, t.ID)
				envoyMap[t.ID] = t
			}
			if len(ids) == 0 {
				continue
			}

			latestRuns, err := runStore.LatestRunPerEnvoy(ctx, ids)
			if err != nil {
				slog.Error("compliance checker: fetching latest runs", "error", err)
				continue
			}

			compliantCount := 0
			for _, id := range ids {
				t := envoyMap[id]
				var recent []convergence.RunInfo
				if run, ok := latestRuns[id]; ok {
					recent = []convergence.RunInfo{{
						Status:       run.Status,
						ChangedCount: int64(run.ChangedCount),
					}}
				}
				lastSeen := time.Time{}
				if t.LastSeen.Valid {
					lastSeen = t.LastSeen.Time
				}
				if convergence.ComputeConvergence(recent, lastSeen, baseInterval) == "converged" {
					compliantCount++
				}
			}

			pct := (compliantCount * 100) / len(ids)
			belowNow := pct < threshold

			if belowNow && !wasBelowThreshold {
				notifier.Notify(vigosmtp.Event{
					Name:      "convergence.threshold",
					Timestamp: time.Now().UTC(),
					Details: map[string]any{
						"compliant_pct": pct,
						"threshold":     threshold,
						"converged":     compliantCount,
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
func createAIProvider(ctx context.Context, cfg config.AISection) (vigoai.Provider, error) {
	switch cfg.Provider {
	case "ollama", "":
		p := vigoai.NewOllamaProvider(cfg.BaseURL)
		if err := p.EnsureModel(ctx, cfg.Model); err != nil {
			return nil, fmt.Errorf("ollama: %w", err)
		}
		return p, nil
	case "claude":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("claude provider requires ai.api_key")
		}
		return vigoai.NewClaudeProvider(cfg.APIKey), nil
	case "openai":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("openai provider requires ai.api_key")
		}
		return vigoai.NewOpenAIProvider(cfg.APIKey), nil
	case "openai-compat":
		return vigoai.NewOpenAICompatProvider(cfg.BaseURL, cfg.APIKey), nil
	default:
		return nil, fmt.Errorf("unknown AI provider: %s", cfg.Provider)
	}
}

// createAuthProvider builds the appropriate auth provider from config.
func createAuthProvider(ctx context.Context, cfg *config.ServerConfig, sqlDB *sql.DB, queries *db.Queries, secretsProvider secrets.Provider) (vigoauth.Provider, error) {
	method := cfg.Auth.Method
	if method == "" {
		method = "basic"
	}

	// Parse idle timeout (default 15m per HIPAA 164.312(a)(2)(iii)).
	var idleTimeout time.Duration
	if cfg.Auth.SessionIdleTimeout != "" {
		d, err := time.ParseDuration(cfg.Auth.SessionIdleTimeout)
		if err != nil {
			return nil, fmt.Errorf("parsing session_idle_timeout: %w", err)
		}
		idleTimeout = d
	}

	switch method {
	case "basic":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries)
		return vigoauth.NewBasicProvider(sessions, userStore, tokenStore, secretsProvider), nil

	case "oidc":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries)
		return vigoauth.NewOIDCProvider(ctx, vigoauth.OIDCConfig{
			Issuer:       cfg.Auth.OIDC.Issuer,
			ClientID:     cfg.Auth.OIDC.ClientID,
			ClientSecret: cfg.Auth.OIDC.ClientSecret,
			RedirectURL:  cfg.Auth.OIDC.RedirectURL,
			Scopes:       cfg.Auth.OIDC.Scopes,
		}, sessions, userStore, tokenStore)

	case "isowebauth":
		sessions := vigoauth.NewSessionManager(sqlDB, idleTimeout)
		userStore := vigoauth.NewUserStore(queries)
		tokenStore := vigoauth.NewTokenStore(queries)
		return vigoauth.NewIsowebauthProvider(vigoauth.IsowebauthConfig{
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


// cveSearchAdapter adapts *api.CVEHandler to ui.CVESearcher.
type cveSearchAdapter struct {
	handler *api.CVEHandler
}

func (a *cveSearchAdapter) SearchCVE(cveID string) (int, []ui.CVEMatch) {
	result := a.handler.Search(cveID)
	matches := make([]ui.CVEMatch, len(result.Hosts))
	for i, h := range result.Hosts {
		matches[i] = ui.CVEMatch{
			Hostname:  h.Hostname,
			Package:   h.Package,
			Installed: h.Installed,
			Fixed:     h.Fixed,
			Severity:  h.Severity,
			Scanner:   h.Scanner,
		}
	}
	return result.TotalHosts, matches
}

// runSecurityPostureSnapshot takes a daily security posture snapshot.
// All CVE counts are unique/deduplicated — read directly from agent traits.
// Runs once at startup and then every 24 hours.
func runSecurityPostureSnapshot(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex) {
	takeSnapshot := func() {
		envoys := fleetIdx.AllEnvoys()
		var critical, high, medium, low, hardening, integrity, rootkit, malware, scanned, total int64
		for _, e := range envoys {
			if e.Revoked {
				continue
			}
			total++
			if e.Traits == nil {
				continue
			}
			hasSecScan := false
			for k, v := range e.Traits {
				var n int64
				fmt.Sscanf(v, "%d", &n)
				switch k {
				case traits.UniqueCritical:
					critical += n
					hasSecScan = true
				case traits.UniqueHigh:
					high += n
				case traits.UniqueMedium:
					medium += n
				case traits.UniqueLow:
					low += n
				case traits.LynisScore:
					hardening += n
				case traits.AideChanged:
					if n > 0 {
						integrity++
					}
				case traits.RkhunterWarnings:
					if n > 0 {
						rootkit++
					}
				case traits.ChkrootkitInfected:
					if n > 0 {
						rootkit++
					}
				case traits.ClamavInfected:
					if n > 0 {
						malware++
					}
				}
			}
			if hasSecScan {
				scanned++
			}
		}
		avgHardening := int64(0)
		if scanned > 0 {
			avgHardening = hardening / scanned
		}

		if err := queries.InsertSecurityPostureSnapshot(ctx, db.InsertSecurityPostureSnapshotParams{
			CveCritical:        critical,
			CveHigh:            high,
			CveMedium:          medium,
			CveLow:             low,
			AvgHardening:       avgHardening,
			IntegrityIncidents: integrity,
			RootkitWarnings:    rootkit,
			MalwareDetections:  malware,
			ScannedHosts:       scanned,
			TotalHosts:         total,
		}); err != nil {
			slog.Error("security posture snapshot failed", "error", err)
		} else {
			slog.Info("security posture snapshot recorded",
				"unique_cve_critical", critical, "unique_cve_high", high, "scanned", scanned, "total", total)
		}
	}

	takeSnapshot()

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			takeSnapshot()
		}
	}
}

// migrateConvergenceHistory renames the old compliance_history table (created
// before the convergence rename) to convergence_history with updated column
// names. No-op if compliance_history doesn't exist (fresh DB).
func migrateConvergenceHistory(sqlDB *sql.DB) {
	// Check if the old table exists.
	var count int
	err := sqlDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='compliance_history'").Scan(&count)
	if err != nil || count == 0 {
		return
	}
	// Check if convergence_history already exists (shouldn't on old DBs).
	var convCount int
	sqlDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='convergence_history'").Scan(&convCount)
	if convCount > 0 {
		sqlDB.Exec("DROP TABLE compliance_history")
		return
	}
	stmts := []string{
		"ALTER TABLE compliance_history RENAME TO convergence_history",
		"ALTER TABLE convergence_history RENAME COLUMN compliant TO converged",
		"ALTER TABLE convergence_history RENAME COLUMN drifted TO recurring_drift",
		"ALTER TABLE convergence_history RENAME COLUMN failing TO errors",
		"ALTER TABLE convergence_history RENAME COLUMN stale TO lost_contact",
		"ALTER TABLE convergence_history RENAME COLUMN unknown TO no_data",
	}
	for _, stmt := range stmts {
		if _, err := sqlDB.Exec(stmt); err != nil {
			slog.Warn("convergence history migration step failed", "stmt", stmt, "error", err)
			return
		}
	}
	slog.Info("migrated compliance_history → convergence_history")
}

// persistComplianceSnapshot writes per-framework standards compliance
// scores to the compliance_history table. Runs daily.
func persistComplianceSnapshot(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex, configStore *config.ConfigStore) {
	if configStore == nil {
		return
	}
	envoys := fleetIdx.AllEnvoys()
	var activeHostnames []string
	for _, e := range envoys {
		if !e.Revoked {
			activeHostnames = append(activeHostnames, e.Hostname)
		}
	}

	type fwEntry struct {
		name        string
		standardKey string
	}
	fwDefs := []fwEntry{
		{"HIPAA", "hipaa"},
		{"HITRUST CSF", "hitrust"},
		{"SOC 2", "soc2"},
		{"PCI DSS v4.0", "pci-dss"},
		{"CIS Benchmarks", "cis"},
		{"NIST 800-53", "nist-800-53"},
		{"ISO 27001", "iso-27001"},
		{"GDPR", "gdpr"},
		{"NERC CIP", "nerc-cip"},
		{"Cyber Essentials+", "cyber-essentials"},
		{"IEC 62443", "iec-62443"},
		{"SOX", "sox"},
		{"FINRA", "finra"},
		{"MiFID II", "mifid2"},
		{"CCSS", "ccss"},
		{"FDA 21 CFR Part 11", "fda-21cfr11"},
		{"Gaming Commission", "gaming"},
		{"CIS Windows", "cis-windows"},
	}

	for _, fw := range fwDefs {
		var totalControls, totalCovered, totalMissing int
		envoyCount := 0
		for _, hostname := range activeHostnames {
			cov := configStore.EnvoyStandardCoverage(hostname, fw.standardKey)
			if cov == nil || cov.TotalControls == 0 {
				continue
			}
			envoyCount++
			totalControls = cov.TotalControls
			totalCovered += len(cov.CoveredControls)
			totalMissing += len(cov.MissingControls)
		}
		scorePct := float64(0)
		satisfied := 0
		notSatisfied := 0
		if envoyCount > 0 && totalControls > 0 {
			avgCovered := float64(totalCovered) / float64(envoyCount)
			avgMissing := float64(totalMissing) / float64(envoyCount)
			scorePct = math.Round(avgCovered/float64(totalControls)*1000) / 10
			satisfied = int(math.Round(avgCovered))
			notSatisfied = int(math.Round(avgMissing))
		}
		if err := queries.UpsertComplianceSnapshot(ctx, db.UpsertComplianceSnapshotParams{
			Framework:     fw.name,
			ScorePercent:  scorePct,
			Satisfied:     int64(satisfied),
			Partial:       0,
			NotSatisfied:  int64(notSatisfied),
			TotalControls: int64(totalControls),
		}); err != nil {
			slog.Error("failed to persist compliance snapshot", "framework", fw.name, "error", err)
		}
	}
	slog.Info("compliance snapshot recorded", "frameworks", len(fwDefs))
}
