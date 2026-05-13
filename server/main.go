// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"os/signal"
	"path/filepath"
	"runtime"
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

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	vigoai "git.alexander4.io/vigo/server/ai"
	"git.alexander4.io/vigo/server/api"
	"git.alexander4.io/vigo/ui"
	"git.alexander4.io/vigo/server/audit"
	vigoauth "git.alexander4.io/vigo/server/auth"
	"git.alexander4.io/vigo/server/backup"
	"git.alexander4.io/vigo/server/bootstrap"
	"git.alexander4.io/vigo/server/ca"
	"git.alexander4.io/vigo/server/compliancedocs"
	"git.alexander4.io/vigo/server/convergence"
	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/grc"
	"git.alexander4.io/vigo/server/publish"
	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/spanner"
	"git.alexander4.io/vigo/server/hardening"
	"git.alexander4.io/vigo/server/fleet"
	"git.alexander4.io/vigo/server/freshness"
	isgrpc "git.alexander4.io/vigo/server/grpc"
	licfmt "git.alexander4.io/tessera/license/products/vigo"
	"git.alexander4.io/vigo/server/license"
	"git.alexander4.io/vigo/server/metrics"
	"git.alexander4.io/vigo/server/registry"
	vigoscrier "git.alexander4.io/vigo/server/scrier"
	"git.alexander4.io/vigo/server/secrets"
	"git.alexander4.io/vigo/server/integrations"
	"git.alexander4.io/vigo/server/task"
	"git.alexander4.io/vigo/server/peer"
	"git.alexander4.io/vigo/server/risk"
	"git.alexander4.io/vigo/server/swarm"
	"git.alexander4.io/vigo/server/traits"
	"git.alexander4.io/vigo/server/watcher"
	"git.alexander4.io/vigo/server/workflows"
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
	docsDir := envOrDefault("VIGO_DOCS_DIR", stringOrDefault(cfg.Paths.Docs, "/seed/docs"))
	taskDir := envOrDefault("VIGO_TASK_DIR", stringOrDefault(cfg.Paths.Tasks, filepath.Join(publish.LiveDir, "tasks")))
	workflowsDir := envOrDefault("VIGO_WORKFLOWS_DIR", stringOrDefault(cfg.Paths.Workflows, filepath.Join(publish.LiveDir, "workflows")))
	agentDir := envOrDefault("VIGO_AGENT_DIR", stringOrDefault(cfg.Paths.AgentDist, "dist/agent"))
	licenseDir := stringOrDefault(cfg.Paths.LicenseDir, licfmt.DefaultLicenseDir)
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

	// fleetIndex is created further down (after the enforcer — it's what the
	// enforcer reads for the active count). The closure captures the
	// pointer so the real index is in place by the time the counter is
	// first called (enforcement loop runs on a 1-hour ticker).
	var fleetIndex *fleet.FleetIndex

	envoyCounter := func(ctx context.Context) (int, error) {
		// Aggregate mode on hub: sum node counts from all spokes + local.
		if licenseMode == "aggregate" && hub != nil {
			total := 0
			for _, s := range hub.SpokeStatuses(ctx) {
				total += int(s.EnvoyCount)
			}
			if fleetIndex != nil {
				total += fleetIndex.ActiveCount()
			}
			return total, nil
		}

		// Local mode (default): the in-memory FleetIndex is the source of
		// truth. It's seeded from the DB at startup via Rehydrate, and
		// every enrollment/revocation goes through the registry under its
		// serialization mutex which keeps the index in sync. Reading it
		// here is O(1) and can be called from the hot path without a
		// ListEnvoys scan.
		if fleetIndex == nil {
			return 0, nil // pre-rehydrate — gate runs later
		}
		return fleetIndex.ActiveCount(), nil
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
		configStore = config.NewEmptyConfigStore(publish.LiveDir, provider)
	}
	configStore.SetBackend(secretsCfg.Backend)
	configStore.SetServerYamlPath(configFile)
	configStore.SetEnabledStandards(cfg.Compliance.Standards)
	configStore.SetApprovedSources(cfg.Compliance.ApprovedSources)

	// Parse base interval for compliance stale threshold.
	baseInterval := 5 * time.Minute // default
	if cfg.CheckIn.Interval != "" {
		if parsed, err := time.ParseDuration(cfg.CheckIn.Interval); err == nil {
			baseInterval = parsed
		}
	}

	// Optional staleness threshold override. When empty, the per-envoy
	// threshold defaults to 2.5 × max(configured_interval, observed_cycle)
	// where observed_cycle is the agent's smoothed inter-CheckIn cadence.
	// When set, this literal duration takes effect across every
	// reachability surface (Offline pill, risk scorer, doctor API,
	// stale-envoy notifier, fleet rehydrator). Single SoT, single override.
	if cfg.CheckIn.OfflineThreshold != "" {
		if parsed, err := time.ParseDuration(cfg.CheckIn.OfflineThreshold); err == nil && parsed > 0 {
			freshness.SetOverride(parsed)
		} else if err != nil {
			slog.Warn("invalid checkin.offline_threshold; falling back to per-envoy 2.5× cadence",
				"value", cfg.CheckIn.OfflineThreshold, "error", err)
		}
	}

	// TTL for resolved fleet: cross-node config lookups (default 30s).
	if cfg.CheckIn.FleetLookupTTL != "" {
		if parsed, err := time.ParseDuration(cfg.CheckIn.FleetLookupTTL); err == nil && parsed > 0 {
			config.SetFleetLookupTTL(parsed)
		} else if err != nil {
			slog.Warn("invalid checkin.fleet_lookup_ttl; using default 30s",
				"value", cfg.CheckIn.FleetLookupTTL, "error", err)
		}
	}

	// Parse tuning parameters (all optional, zero values use defaults).
	sigWindow := parseDuration(cfg.Tuning.SignatureWindow, 0)
	lastSeenFlush := parseDuration(cfg.Tuning.LastSeenFlush, 10*time.Second)
	traitsPruneInterval := parseDuration(cfg.Tuning.TraitsPruneInterval, 5*time.Minute)

	// One-shot summary of the cadence + staleness wiring so future
	// "why does the envoys page disagree with reality?" investigations
	// are one journal grep away. Threshold is per-envoy at runtime
	// (2.5 × max(configured_interval, observed_cycle)); the value
	// logged here is the bootstrap floor when no agent has reported a
	// cadence yet.
	slog.Info("freshness wiring",
		"checkin.interval", baseInterval,
		"checkin.offline_threshold_override", cfg.CheckIn.OfflineThreshold,
		"freshness.bootstrap_threshold", freshness.StaleThresholdFromCycle(baseInterval, 0),
		"freshness.max_reported_cycle", freshness.MaxReportedCycle,
		"tuning.last_seen_flush", lastSeenFlush,
	)
	metricsInterval := parseDuration(cfg.Tuning.MetricsInterval, 30*time.Second)
	traitsScannerTTL := parseDuration(cfg.Tuning.TraitsScannerTTL, 0)
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

	// Create integrations dispatcher (native integrations only — no SMTP, no generic webhooks).
	integ := integrations.New(cfg.Integrations)
	if integ.Enabled() {
		slog.Info("integrations dispatcher enabled", "targets", integ.Count())
	}

	// Wire config reload failure events to integrations.
	configStore.SetOnReloadFailure(func(err error) {
		integ.Dispatch(integrations.Event{
			Name:      "config.reload.failure",
			Timestamp: time.Now().UTC(),
			Summary:   "config reload failed: " + err.Error(),
			Details:   map[string]any{"error": err.Error()},
		})
	})

	// (Scrier SSH auth is now resolved at session time from config modules —
	// no sync step needed. Use "vigocli scrier users map" for username mappings.)

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

	// Create and rehydrate FleetIndex. Assigns to the outer variable
	// captured by envoyCounter above.
	fleetIndex = fleet.NewFleetIndex()
	if err := fleet.Rehydrate(ctx, fleetIndex, queries, runStore, baseInterval, configStore.ApprovedSources()); err != nil {
		return fmt.Errorf("rehydrating fleet index: %w", err)
	}

	// Wire the enrollment gate + fleet index into the registry. These
	// collaborators are what make registry.Register's license check atomic
	// with the DB insert and the FleetIndex update, closing the TOCTOU
	// race and the bootstrap/admin bypass paths.
	reg.SetFleetIndex(fleetIndex)
	reg.SetEnrollmentGate(enforcer)

	// Sync envoy tags from matchfile. Tags are derived from the matchfile
	// (single source of truth) and kept only in FleetIndex; syncing at
	// startup ensures targeting works before any envoy has checked in.
	if configStore != nil {
		for _, ts := range fleetIndex.AllEnvoys() {
			if _, cfg := configStore.Match(ts.Hostname); cfg != nil {
				fleetIndex.SetTags(ts.ID, cfg.Tags)
			}
		}
	}

	// Self-enroll: the server registers itself as its own envoy so it can
	// manage its own state (backups, monitoring, etc.) via standard modules.
	// Create async run batcher for delta events.
	runBatcher := fleet.NewRunBatcher(runStore)

	// Initialize bundle signer for compiled promises.
	bundleSigner, bundleMaxAge := initBundleSigner(cfg)

	// Detect the agent binary version PER-ARCH for auto-update comparison.
	// Resolution order per arch: sidecar `<binary>.version` file → server-arch
	// runtime probe. Cross-arch binaries without sidecars yield empty entries,
	// which disable auto-update for that arch (safer than guessing the wrong
	// version and causing cross-arch update loops — see tshoot/log.md).
	agentVersions := bootstrap.DetectAgentVersions(agentDir)
	agentVersion := agentVersions[runtime.GOOS+"/"+runtime.GOARCH]
	if agentVersion == "" {
		agentVersion = version // fallback to server version for UI display only
	}
	slog.Info("agent version for auto-update (server arch)", "agent_version", agentVersion)

	// Build fallback server addresses from peer config (returned to agents on registration).
	var fallbackServers []string
	for _, pe := range cfg.Peers.Servers {
		fallbackServers = append(fallbackServers, pe.Addr)
	}

	// Create and start gRPC server.
	grpcSrv, _, traitsBatch, traitsPruner, agentSvc, adminSvc, err := isgrpc.NewServer(isgrpc.ServerConfig{
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
		Integrations:         integ,
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
		LicenseEnforcer:      enforcer,
		TaskStore:            taskStore,
		FleetIndex:           fleetIndex,
		RunBatcher:           runBatcher,
		BundleSigner:        bundleSigner,
		BundleMaxAge:        bundleMaxAge,
		FallbackServers:     fallbackServers,
		AuditWriter:         auditWriter,
		ServerVersions:      agentVersions,
		GlobalObserveOnly:   cfg.CheckIn.ObserveOnly,
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
	} else if size, err := db.DatabaseSizeBytes(sqlDB); err == nil && size > 1024*1024*1024 {
		// Unbounded growth on a database past 1 GiB is the same shape as the
		// home-lab regression that surfaced in 0.38.x — a 31 GB DB making the
		// dashboard's recent-activity query unrunnable. Log a one-shot warning
		// so the operator notices before queries start crossing the browser
		// timeout. One-shot at startup; not chatty.
		slog.Warn("database retention pruning is disabled and DB is large",
			"size_bytes", size,
			"hint", "set database.retention in server.yaml (e.g. \"30d\") and run `vigocli db prune --older-than 30d` once to reclaim space")
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
			w := watcher.NewWatcher(vp, watcher.NewDBAdapter(queries), configStore, integ, pollInterval, watchPaths)
			w.SetConfigReloader(configStore)
			w.SetNotifyFunc(func(event string, details map[string]any) {
				integ.Dispatch(integrations.Event{
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

	// Start stale envoy checker — fires envoy.stale events on transition.
	go runStaleChecker(ctx, queries, fleetIndex, integ, baseInterval)

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

	// Daily standards compliance snapshot — goroutine started below after
	// reportHandler is constructed so the snapshot job can route through the
	// unified scorer.

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
	go runFrameworkDriftAlerter(ctx, queries, configStore, integ, fleetIndex, baseInterval)

	// Evidence email scheduler removed — compliance evidence is delivered via
	// GRC integrations (Vanta, Drata, etc.), not email.

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
	// SMTP notifier removed — all events route through integrations dispatcher.

	// Start compliance threshold checker (threshold from checkin config, default 0 = disabled).
	complianceThreshold := cfg.CheckIn.ComplianceThreshold
	if complianceThreshold > 0 {
		go runComplianceChecker(ctx, queries, runStore, fleetIndex, integ, baseInterval, complianceThreshold)
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
		Integrations:   integ.Enabled(),
		Role:           role,
		SecretsBackend: secretsCfg.Backend,
		CycleLookup: func(envoyID string) time.Duration {
			if fleetIndex == nil {
				return 0
			}
			return fleetIndex.EnvoyCycle(envoyID)
		},
	})
	// Seed the doctor handler's swarm-config view so the
	// swarm_pattern_order check has data on the first /doctor call.
	// Refreshed on every reload below alongside the agentSvc / filecast /
	// uiHandler swarm-config setters.
	doctorHandler.SetSwarmConfig(cfg.Swarm)

	spannerHandler := api.NewSpannerHandler(role, cfg.Spanner.SpokeID, hub)
	taskHandler := api.NewTaskHandler(queries, agentSvc, cfg.Task.RequireDefinition)
	taskHandler.SetAuditWriter(auditWriter)
	queryHandler := api.NewQueryHandler(agentSvc)
	exportHandler := api.NewExportHandler(queries, fleetIndex, configStore, cfg.Export)
	reportHandler := api.NewReportHandler(queries, fleetIndex, configStore, version)

	// Start daily standards compliance snapshotting through the unified scorer.
	go func() {
		persistComplianceSnapshot(ctx, queries, configStore, reportHandler)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				persistComplianceSnapshot(ctx, queries, configStore, reportHandler)
			}
		}
	}()
	cveHandler := api.NewCVEHandler(fleetIndex)
	peerHandler := api.NewPeerHandler(peerSvc)

	// Auth-related API handlers.
	userStore := vigoauth.NewUserStore(queries)
	tokenStore := vigoauth.NewTokenStore(queries)
	usersHandler := api.NewUsersHandler(userStore, authMethod)
	usersHandler.SetAuditWriter(auditWriter)
	usersHandler.SetSecretsProvider(provider)
	tokensHandler := api.NewTokensHandler(tokenStore, authMethod)
	tokensHandler.SetAuditWriter(auditWriter)

	scrierAPIHandler := api.NewScrierHandler(queries, authMethod)
	scrierAPIHandler.SetAuditWriter(auditWriter)

	// Seed default admin account on first startup (no password — must be set via CLI).
	if count, err := userStore.CountUsers(ctx); err == nil && count == 0 {
		if _, err := userStore.Create(ctx, db.InsertUserParams{
			Username: "admin",
			Role:     "admin",
		}); err != nil {
			slog.Warn("failed to seed admin user", "error", err)
		} else {
			slog.Info("seeded default admin account (no password — run: vigocli secrets set web vigo/web/auth/admin)")
		}
	}

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

	// Wire the cross-envoy lockbox cleanup callback fired by
	// AdminService.RevokeEnvoy / DeleteEnvoy (0.41.3+). Dispatches one
	// `_apply-evict --hostname <h> --all-users` task to every envoy,
	// which causes each agent to drop the retired host's pubkey from
	// every locally-enrolled user's recipients list. Idempotent;
	// best-effort.
	adminSvc.SetSwarmCleanup(func(ctx context.Context, hostname string) error {
		cmd := fmt.Sprintf("vigo swarm lockbox _apply-evict --hostname %s --all-users", hostname)
		_, err := agentSvc.DispatchPush(ctx, isgrpc.DispatchParams{
			Command:     cmd,
			TargetSpec:  "*",
			Operator:    "envoy.retire",
			TimeoutSecs: 60,
		})
		return err
	})

	// Wire stream_edit guardrails.
	agentSvc.SetStreamEditConfig(cfg.StreamEdit)
	if !cfg.StreamEdit.StreamEditEnabled() {
		slog.Warn("stream_edit globally disabled — all stream_edit attributes will be stripped from policy bundles")
	}

	// Gitback is fully P2P as of 0.23.0 — no server-side ClaimTable,
	// no trait-driven Builder, no SetCairn wiring. See
	// `plan_cairn_p2p_first.md` for the rearchitecture history.

	// Initialize Scrier remote access (optional — disabled by default).
	var scrierHandler *vigoscrier.Handler
	if cfg.Scrier.Enabled {
		tm := isgrpc.NewTunnelManager(
			cfg.Scrier.ScrierGuacdAddr(),
			cfg.Scrier.ScrierProxyHostname(),
			cfg.Scrier.ScrierAllowedPorts(),
			cfg.Scrier.ScrierMaxSessions(),
			agentSvc.StreamMgr(),
			fleetIndex,
		)
		agentSvc.SetTunnelManager(tm)
		scrierHandler = vigoscrier.NewHandler(tm, fleetIndex, queries, configStore, version, baseInterval)
		scrierHandler.SetAuditWriter(auditWriter)
		slog.Info("scrier remote access enabled",
			"guacd", cfg.Scrier.ScrierGuacdAddr(),
			"max_sessions", cfg.Scrier.ScrierMaxSessions(),
			"allowed_ports", cfg.Scrier.ScrierAllowedPorts(),
		)
	}

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
		reportHandler.SetAuditWriter(auditWriter)
		usersHandler.RegisterRoutes(mux)
		tokensHandler.RegisterRoutes(mux)
		scrierAPIHandler.RegisterRoutes(mux)
		if aiHandler != nil {
			aiHandler.RegisterRoutes(mux)
		}
		authProvider.RegisterRoutes(mux)
		if scrierHandler != nil {
			scrierHandler.RegisterRoutes(mux)
		}
		// Serve license files so agents can download them during bootstrap.
		mux.HandleFunc("GET /license/LICENSE", serveLicenseFile(serverRoot, "LICENSE"))
		mux.HandleFunc("GET /license/COMMERCIAL-TERMS.md", serveLicenseFile(serverRoot, "COMMERCIAL-TERMS.md"))
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
				bh.SetIntegrations(integ)
				bh.SetFleetIndex(fleetIndex)
				bh.SetLicenseEnforcer(enforcer)
			})

			if len(cfg.Bootstrap.TrustedPatterns) > 0 {
				for _, tp := range cfg.Bootstrap.TrustedPatterns {
					slog.Info("trusted enrollment pattern", "pattern", tp.Pattern, "cidrs", tp.CIDRs)
				}
			}
		}

		// Compliance docs store: operator-uploaded artifacts for
		// documented-scope controls. Only LocalProvider exposes the
		// Encrypt/Decrypt envelope today; under isopass or any other
		// backend the store stays nil and the REST routes return 503.
		var complianceDocsStore *compliancedocs.Store
		if lp, ok := provider.(*secrets.LocalProvider); ok {
			docsCfg := cfg.Compliance.Docs
			cdStore, cdErr := compliancedocs.New(
				docsCfg.ComplianceDocsDir(),
				queries,
				lp,
				compliancedocs.Caps{
					MaxDocBytes:          docsCfg.ComplianceMaxDocBytes(),
					MaxPerFrameworkBytes: docsCfg.ComplianceMaxPerFrameworkBytes(),
					MaxTotalBytes:        docsCfg.ComplianceMaxTotalBytes(),
				},
				docsCfg.ComplianceMimeAllowlist(),
			)
			if cdErr != nil {
				slog.Warn("compliance docs store unavailable", "error", cdErr)
			} else {
				complianceDocsStore = cdStore
				reportHandler.SetDocsResolver(cdStore)
				slog.Info("compliance docs store ready",
					"dir", docsCfg.ComplianceDocsDir(),
					"max_doc_bytes", docsCfg.ComplianceMaxDocBytes())
			}
		}

		gwMux, uiHandler, gwErr := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version, docsDir, assistant, hub, runStore, fleetIndex, authMethod, enforcer, cfg.Publish, cfg.Branding, peerSvc, auditWriter, complianceDocsStore, gwOpts...)
		if gwErr != nil {
			return fmt.Errorf("creating REST gateway: %w", gwErr)
		}
		// Wire branding, hostname, observe-only, and CVE searcher into UI handler.
		uiHandler.SetAgentVersion(agentVersion)
		uiHandler.SetBranding(cfg.Branding)
		uiHandler.SetTraitQueryDispatcher(&traitQueryAdapter{agentSvc: agentSvc})
		uiHandler.SetServerHostname(cfg.Server.Hostname)
		uiHandler.SetServerConfigPath(configFile)
		uiHandler.SetGlobalObserveOnly(cfg.CheckIn.ObserveOnly)
		uiHandler.SetCVESearcher(&cveSearchAdapter{cveHandler})
		uiHandler.SetHealthChecker(&healthCheckAdapter{doctorHandler})

		// Wire report builder into UI handler for HTMX fragment rendering.
		uiHandler.SetReportBuilder(&ui.ReportBuilder{
			Executive: func(r *http.Request) (any, error) {
				return reportHandler.BuildExecutiveReport(r)
			},
			Framework: func(r *http.Request, slug string) (any, error) {
				return reportHandler.BuildFrameworkReport(r, slug)
			},
			EnvoyFrameworkStatus: reportHandler.ComputeEnvoyFrameworkStatus,
			EnvoyFrameworkDetail: reportHandler.ComputeEnvoyFrameworkDetail,
		})
		uiHandler.SetIntegrationsConfig(cfg.Integrations, cfg.GRC)
		uiHandler.SetTraitLookup(&traitLookupAdapter{fleetIndex: fleetIndex})
		fleetCache := ui.NewFleetCache(queries, configStore, cfg.CheckIn.ObserveOnly, baseInterval)
		fleetCache.SetFleetSource(&uiFleetSourceAdapter{fleetIndex: fleetIndex})
		uiHandler.SetFleetCache(fleetCache)
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
		usersHandler.RegisterRoutes(gwMux)
		tokensHandler.RegisterRoutes(gwMux)
		scrierAPIHandler.RegisterRoutes(gwMux)
		// Fleet pubkey distribution. Lives outside the swarm-enabled
		// gate so agents can bootstrap their gitback peer-pubkey
		// TOFU cache regardless of whether THIS server has the swarm
		// substrate enabled — the endpoint just reads FleetIndex,
		// which is always populated. Without this hoist, agents on
		// swarm-disabled servers would get 404s every 15 minutes
		// from their bootstrap loop.
		api.NewFleetPubkeysHandler(fleetIndex).RegisterRoutes(gwMux)

		// Swarm: peer-to-peer content distribution. The substrate runs on
		// this server iff the server-as-envoy hostname matches the swarm
		// pattern list — the canonical assumption is the server hosts a
		// local agent under cfg.Server.Hostname (CLAUDE.md). An empty
		// pattern list disables the substrate entirely.
		if config.HostMatchesPatterns(cfg.Server.Hostname, cfg.Swarm.Enabled) {
			seedDir := cfg.Swarm.SeedDir
			if seedDir == "" {
				seedDir = "/var/lib/vigo/swarm/seed"
			}
			if err := os.MkdirAll(seedDir, 0755); err != nil {
				return fmt.Errorf("creating swarm seed dir: %w", err)
			}
			staleThreshold := baseInterval * 2
			if staleThreshold < 60*time.Second {
				staleThreshold = 60 * time.Second
			}
			peerTracker := swarm.NewPeerTracker(staleThreshold)
			peerTracker.SetActiveChecker(fleetIndex.IsActive)
			// Manifest persistence sits next to seed/, so a server
			// restart preserves intent (revocations + the active set
			// agents are allowed to refresh via gossip). Without
			// this, an empty post-restart manifest gets re-poisoned
			// by stale `swarm_seeds` traits within seconds (root
			// cause of the 2026-04-24 incident). Filename is
			// `server-manifest.json` to stay clear of the local
			// agent's own `manifest.json` at the same path — the
			// two share the dir via the swarm bind-mount but use
			// independent on-disk schemas.
			manifestPath := filepath.Join(filepath.Dir(seedDir), "server-manifest.json")
			manifestStore, err := swarm.NewPersistentManifestStore(manifestPath, time.Second)
			if err != nil {
				return fmt.Errorf("loading swarm manifest: %w", err)
			}
			slog.Info("swarm manifest persistence enabled", "path", manifestPath)
			defer manifestStore.Stop()
			lodeHandler := api.NewLodeHandler(seedDir, peerTracker, fleetIndex, manifestStore, queries)
			lodeHandler.SetForcePusher(agentSvc)
			lodeHandler.SetSwarmConfig(cfg.Swarm)
			lodeHandler.SetAuditWriter(auditWriter)
			lodeHandler.RegisterRoutes(gwMux)
			swarmAPIHandler := api.NewSwarmHandler(peerTracker, manifestStore, fleetIndex)
			swarmAPIHandler.SetCleanupDispatcher(agentSvc)
			swarmAPIHandler.RegisterRoutes(gwMux)
			uiHandler.SetSwarmAPI(swarmAPIHandler)
			agentSvc.SetSwarm(peerTracker, manifestStore)
			agentSvc.SetSwarmConfig(cfg.Swarm)
			uiHandler.SetSwarm(peerTracker, &swarmHostnameAdapter{fleetIndex: fleetIndex}, cfg.Swarm)
			slog.Info("swarm enabled", "seed_dir", seedDir, "port", cfg.Swarm.Port)

			// Track pattern lists across reloads so we can audit operator
			// edits to each subsystem's authorization. Patterns are what
			// the operator actually edited in YAML — per-host transition
			// detection is unnecessary because pattern flips are gate-only
			// in 0.41.0+ (the destructive blacklist dispatch path was
			// retired with the lockbox rework — destructive scrub is
			// now usercrate-flag-driven, not envoy-pattern-driven).
			lastSwarmPatterns := append([]string(nil), cfg.Swarm.Enabled...)
			lastPuddlePatterns := append([]string(nil), cfg.Swarm.Puddle.Enabled...)
			lastGitbackPatterns := append([]string(nil), cfg.Swarm.Gitback.Enabled...)
			lastLongdrawerPatterns := append([]string(nil), cfg.Swarm.Longdrawer.Enabled...)
			lastFilecastPatterns := append([]string(nil), cfg.Swarm.Filecast.Enabled...)

			configStore.SetOnReloadSuccess(func() {
				// Re-read server.yaml directly — ConfigStore.Reload() only
				// refreshes worksite YAML, not server.yaml. Same pattern
				// reloadComplianceStandards uses.
				newCfg, err := config.LoadServerConfig(configFile, provider, secretsCfg.Backend)
				if err != nil {
					slog.Warn("config reload: failed to re-read server.yaml for swarm section", "error", err)
					return
				}
				agentSvc.SetSwarmConfig(newCfg.Swarm)
				uiHandler.SetSwarm(peerTracker, &swarmHostnameAdapter{fleetIndex: fleetIndex}, newCfg.Swarm)
				lodeHandler.SetSwarmConfig(newCfg.Swarm)
				doctorHandler.SetSwarmConfig(newCfg.Swarm)

				if !sameStringSet(lastSwarmPatterns, newCfg.Swarm.Enabled) {
					recordSwarmPatternsChange(ctx, auditWriter, "swarm", lastSwarmPatterns, newCfg.Swarm.Enabled)
					lastSwarmPatterns = append([]string(nil), newCfg.Swarm.Enabled...)
				}
				if !sameStringSet(lastPuddlePatterns, newCfg.Swarm.Puddle.Enabled) {
					recordSwarmPatternsChange(ctx, auditWriter, "puddle", lastPuddlePatterns, newCfg.Swarm.Puddle.Enabled)
					lastPuddlePatterns = append([]string(nil), newCfg.Swarm.Puddle.Enabled...)
				}
				if !sameStringSet(lastGitbackPatterns, newCfg.Swarm.Gitback.Enabled) {
					recordSwarmPatternsChange(ctx, auditWriter, "gitback", lastGitbackPatterns, newCfg.Swarm.Gitback.Enabled)
					lastGitbackPatterns = append([]string(nil), newCfg.Swarm.Gitback.Enabled...)
				}
				if !sameStringSet(lastLongdrawerPatterns, newCfg.Swarm.Longdrawer.Enabled) {
					recordSwarmPatternsChange(ctx, auditWriter, "longdrawer", lastLongdrawerPatterns, newCfg.Swarm.Longdrawer.Enabled)
					lastLongdrawerPatterns = append([]string(nil), newCfg.Swarm.Longdrawer.Enabled...)
				}
				if !sameStringSet(lastFilecastPatterns, newCfg.Swarm.Filecast.Enabled) {
					recordSwarmPatternsChange(ctx, auditWriter, "filecast", lastFilecastPatterns, newCfg.Swarm.Filecast.Enabled)
					lastFilecastPatterns = append([]string(nil), newCfg.Swarm.Filecast.Enabled...)
				}
			})

			swarmPruner := swarm.NewRetentionPruner(7 * 24 * time.Hour)
			swarmPruner.SetTracker(peerTracker)
			swarmPruner.SetManifestStore(manifestStore)
			swarmPruner.SetLivenessTTL(parseLivenessTTL(cfg.Swarm.ManifestLivenessTTL))
			go swarmPruner.Start(ctx, 24*time.Hour)
		}

		if aiHandler != nil {
			aiHandler.RegisterRoutes(gwMux)
		}
		dbHandler := api.NewDatabaseHandler(sqlDB, queries, dbDriver, cfg.Database.DSN)
		dbHandler.RegisterRoutes(gwMux)
		authProvider.RegisterRoutes(gwMux)
		if scrierHandler != nil {
			scrierHandler.RegisterRoutes(gwMux)
			uiHandler.SetScrierEnabled(true)
		}
		// Serve license files so agents can download them during bootstrap.
		gwMux.HandleFunc("GET /license/LICENSE", serveLicenseFile(serverRoot, "LICENSE"))
		gwMux.HandleFunc("GET /license/COMMERCIAL-TERMS.md", serveLicenseFile(serverRoot, "COMMERCIAL-TERMS.md"))
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
		Total:     int64(summary.Total),
		Converged: int64(summary.Converged),
		Degraded:  int64(summary.Degraded),
		Failed:    int64(summary.Failed),
		NoData:    int64(summary.NoData),
		Changed:   int64(summary.Changed),
		Diverged:  int64(summary.Diverged),
		Offline:   int64(summary.Offline),
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
			ObservedCycle:     fleetIdx.EnvoyCycle(es.ID),
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
		Converged: s.Converged,
		Degraded:  s.Degraded,
		Failed:    s.Failed,
		NoData:    s.NoData,
		Changed:   s.Changed,
		Diverged:  s.Diverged,
		Offline:   s.Offline,
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
			ObservedCycle:     a.fleetIndex.EnvoyCycle(es.ID),
		}
	}
	return snapshots
}

// uiFleetSourceAdapter adapts *fleet.FleetIndex to ui.FleetSource so
// the dashboard convergence pills go through the same in-memory
// counters as the metrics collector — eliminating the previous drift
// between FleetCache (rebuilt every ~10s from the DB) and FleetIndex.
type uiFleetSourceAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *uiFleetSourceAdapter) ConvergenceSummary() ui.FleetSummary {
	c := a.fleetIndex.ConvergenceSummary()
	return ui.FleetSummary{
		Total:     c.Total,
		Converged: c.Converged,
		Degraded:  c.Degraded,
		Failed:    c.Failed,
		NoData:    c.NoData,
		Changed:   c.Changed,
		Diverged:  c.Diverged,
		Offline:   c.Offline,
	}
}

// EnvoyCycle resolves an envoy's smoothed observed inter-CheckIn
// cadence from the live FleetIndex. Used by the FleetCache so per-envoy
// staleness thresholds reflect agent-reported cadence rather than
// configured interval alone.
func (a *uiFleetSourceAdapter) EnvoyCycle(id string) time.Duration {
	return a.fleetIndex.EnvoyCycle(id)
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
func runFrameworkDriftAlerter(ctx context.Context, queries *db.Queries, configStore *config.ConfigStore, integ *integrations.Dispatcher, fleetIdx *fleet.FleetIndex, interval time.Duration) {
	if integ == nil || configStore == nil {
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

				integ.Dispatch(integrations.Event{
					Name:      "convergence.drift",
					Timestamp: r.StartedAt,
					Hostname:  hostname,
					EnvoyID:   r.EnvoyID,
					Summary:   fmt.Sprintf("drift on %s affected %s", hostname, display),
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

// notification when a envoy transitions from non-stale to stale. The
// "is stale" predicate lives in package freshness (2.5× the base
// interval) and is shared with the UI, doctor, and risk scorer. The
// checker runs at the base interval frequency. It always updates the
// FleetIndex offline flag; SMTP notifications are optional.
func runStaleChecker(ctx context.Context, queries *db.Queries, fleetIdx *fleet.FleetIndex, integ *integrations.Dispatcher, baseInterval time.Duration) {
	wasStale := make(map[string]bool) // envoy ID → was stale last tick
	canNotify := integ != nil

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
					stale := freshness.IsStaleFromCycle(ts.LastSeen, baseInterval, fleetIdx.EnvoyCycle(ts.ID))
					fleetIdx.SetOffline(ts.ID, stale)
					if !stale {
						continue
					}
					nowStale[ts.ID] = true
					if wasStale[ts.ID] {
						continue
					}
					if canNotify {
						integ.Dispatch(integrations.Event{
							Name:      "envoy.stale",
							Timestamp: time.Now().UTC(),
							Hostname:  ts.Hostname,
							EnvoyID:   ts.ID,
							Summary:   fmt.Sprintf("%s stale since %s", ts.Hostname, ts.LastSeen.Format("15:04 UTC")),
							Details:   map[string]any{"last_seen": ts.LastSeen.Format("2006-01-02 15:04:05 UTC")},
						})
						integ.Dispatch(integrations.Event{
							Name:      "envoy.offline",
							Timestamp: time.Now().UTC(),
							Hostname:  ts.Hostname,
							EnvoyID:   ts.ID,
							Summary:   fmt.Sprintf("%s went offline", ts.Hostname),
							Details:   map[string]any{"last_seen": ts.LastSeen.Format("2006-01-02 15:04:05 UTC")},
						})
					}
				}
				// Detect envoys that recovered (were stale, now not).
				if canNotify {
					for id := range wasStale {
						if !nowStale[id] {
							if ts, ok, _ := fleetIdx.GetEnvoyLite(id); ok {
								integ.Dispatch(integrations.Event{
									Name:      "envoy.online",
									Timestamp: time.Now().UTC(),
									Hostname:  ts.Hostname,
									EnvoyID:   id,
									Summary:   fmt.Sprintf("%s back online", ts.Hostname),
								})
							}
						}
					}
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
				// DB-only fallback path (fleetIdx unavailable): no observed cycle
				// data, so the threshold is derived from configured interval alone.
				if !freshness.IsStaleFromCycle(t.LastSeen.Time, baseInterval, 0) {
					continue
				}

				nowStale[t.ID] = true
				if wasStale[t.ID] {
					continue // already notified
				}

				if canNotify {
					integ.Dispatch(integrations.Event{
						Name:      "envoy.stale",
						Timestamp: time.Now().UTC(),
						Hostname:  t.Hostname,
						EnvoyID:   t.ID,
						Summary:   fmt.Sprintf("%s stale since %s", t.Hostname, t.LastSeen.Time.Format("15:04 UTC")),
						Details:   map[string]any{"last_seen": t.LastSeen.Time.Format("2006-01-02 15:04:05 UTC")},
					})
				}
			}
			wasStale = nowStale
		}
	}
}

// runComplianceChecker periodically computes the convergence percentage across
// all active envoys and fires a convergence.threshold SMTP notification when
// it drops below the configured threshold. Only fires once per transition.
func runComplianceChecker(ctx context.Context, queries *db.Queries, runStore db.RunStore, fleetIdx *fleet.FleetIndex, integ *integrations.Dispatcher, baseInterval time.Duration, threshold int) {
	if integ == nil || threshold <= 0 {
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
					integ.Dispatch(integrations.Event{
						Name:      "convergence.threshold",
						Timestamp: time.Now().UTC(),
						Summary:   fmt.Sprintf("convergence %d%% below threshold %d%%", pct, threshold),
						Details: map[string]any{
							"converged_pct": pct,
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
				var recent []convergence.RunInfo
				if run, ok := latestRuns[id]; ok {
					recent = []convergence.RunInfo{{
						Status:       run.Status,
						ChangedCount: int64(run.ChangedCount),
					}}
				}
				if convergence.ComputeConvergence(recent) == "converged" {
					compliantCount++
				}
			}

			pct := (compliantCount * 100) / len(ids)
			belowNow := pct < threshold

			if belowNow && !wasBelowThreshold {
				integ.Dispatch(integrations.Event{
					Name:      "convergence.threshold",
					Timestamp: time.Now().UTC(),
					Summary:   fmt.Sprintf("convergence %d%% below threshold %d%%", pct, threshold),
					Details: map[string]any{
						"converged_pct": pct,
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

func (a *cveSearchAdapter) SearchCVE(cveID string, runningOnly bool) (int, []ui.CVEMatch) {
	result := a.handler.Search(cveID, runningOnly)
	matches := make([]ui.CVEMatch, len(result.Hosts))
	for i, h := range result.Hosts {
		running := make([]ui.CVERunningBinaryRef, len(h.RunningBinaries))
		for j, rb := range h.RunningBinaries {
			running[j] = ui.CVERunningBinaryRef{
				BinaryPath:     rb.BinaryPath,
				Package:        rb.Package,
				PackageVersion: rb.PackageVersion,
				PidCount:       rb.PidCount,
				Deleted:        rb.Deleted,
			}
		}
		matches[i] = ui.CVEMatch{
			Hostname:        h.Hostname,
			Package:         h.Package,
			Installed:       h.Installed,
			Fixed:           h.Fixed,
			Severity:        h.Severity,
			Scanner:         h.Scanner,
			RunningBinaries: running,
		}
	}
	return result.TotalHosts, matches
}

// serveLicenseFile returns an http.HandlerFunc that serves a license file from the server root.
func serveLicenseFile(serverRoot, filename string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(serverRoot, filename))
	}
}

// healthCheckAdapter adapts *api.DoctorHandler to ui.HealthChecker.
type healthCheckAdapter struct {
	handler *api.DoctorHandler
}

func (a *healthCheckAdapter) RunChecks() (string, []ui.HealthCheck) {
	status, checks := a.handler.RunChecks()
	uiChecks := make([]ui.HealthCheck, len(checks))
	for i, c := range checks {
		uiChecks[i] = ui.HealthCheck{
			Name:    c.Name,
			Status:  string(c.Status),
			Scope:   string(c.Scope),
			Message: c.Message,
		}
	}
	return status, uiChecks
}

// traitQueryAdapter wraps grpc.AgentService to satisfy ui.TraitQueryDispatcher.
// Used by the security drawer's live-detail fetch and any future
// fragment handler that needs a single-envoy on-demand trait pull.
type traitQueryAdapter struct {
	agentSvc *isgrpc.AgentService
}

func (a *traitQueryAdapter) DispatchTraitQuery(ctx context.Context, traitPath, hostname string, timeout time.Duration) (string, bool, error) {
	res, err := a.agentSvc.DispatchQuery(ctx, traitPath, hostname, timeout)
	if err != nil {
		return "", false, err
	}
	for _, entry := range res.Responded {
		if entry.Found {
			return entry.Value, true, nil
		}
	}
	return "", false, nil
}

// traitLookupAdapter wraps fleet.FleetIndex to satisfy ui.TraitLookup.
type traitLookupAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *traitLookupAdapter) EnvoyTrait(envoyID, key string) string {
	if state := a.fleetIndex.GetEnvoy(envoyID); state != nil {
		return state.Traits[key]
	}
	return ""
}

func (a *traitLookupAdapter) EnvoyTagsByHostname(hostname string) []string {
	if a.fleetIndex == nil || hostname == "" {
		return nil
	}
	for _, e := range a.fleetIndex.AllEnvoys() {
		if e.Hostname == hostname {
			if len(e.Tags) == 0 {
				return nil
			}
			out := make([]string, len(e.Tags))
			copy(out, e.Tags)
			return out
		}
	}
	return nil
}

// swarmHostnameAdapter wraps fleet.FleetIndex to satisfy ui.SwarmHostnameResolver.
type swarmHostnameAdapter struct {
	fleetIndex *fleet.FleetIndex
}

func (a *swarmHostnameAdapter) ResolveHostname(envoyID string) string {
	if state := a.fleetIndex.GetEnvoy(envoyID); state != nil {
		return state.Hostname
	}
	return ""
}

func (a *swarmHostnameAdapter) ResolveIP(ip string) string {
	for _, es := range a.fleetIndex.AllEnvoys() {
		if es.IP == ip {
			return es.Hostname
		}
	}
	return ""
}

func (a *swarmHostnameAdapter) MatchingEnvoys(target string) []string {
	var names []string
	for _, es := range a.fleetIndex.AllEnvoys() {
		if !es.Revoked && swarm.MatchTarget(es.Hostname, es.Traits, target) {
			names = append(names, es.Hostname)
		}
	}
	return names
}

// parseLivenessTTL parses the `swarm.manifest_liveness_ttl` config knob
// into a time.Duration. Empty / zero / negative input disables the
// liveness sweep (the pruner skips it). Default 24h matches the
// documented behavior on operators who omit the knob.
func parseLivenessTTL(s string) time.Duration {
	if s == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		slog.Warn("swarm: invalid manifest_liveness_ttl, defaulting to 24h",
			"value", s, "error", err)
		return 24 * time.Hour
	}
	if d < 0 {
		return 0
	}
	return d
}

// sameStringSet reports whether `a` and `b` contain the same elements
// regardless of order or duplicates. Used by the publish hook to decide
// whether a subsystem's authorization patterns materially changed across
// a reload.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		// Length mismatch is sufficient when callers don't pass dups —
		// they don't here, so the cheap check is correct.
		seen := make(map[string]struct{}, len(a))
		for _, s := range a {
			seen[s] = struct{}{}
		}
		for _, s := range b {
			if _, ok := seen[s]; !ok {
				return false
			}
		}
		// Same elements but different cardinalities — treat as changed.
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := seen[s]; !ok {
			return false
		}
	}
	return true
}

// recordSwarmPatternsChange emits a tamper-evident audit-chain entry
// whenever a swarm authorization pattern list changes. `substrate` is
// "swarm" for the top-level pattern, or one of
// "puddle"/"gitback"/"longdrawer"/"filecast" for the per-subsystem
// patterns. The summary states the pattern delta; the payload carries
// the full new list so a reviewer can reconstruct post-change state
// without joining log streams. Failures are logged at WARN — the audit
// chain is best-effort from the publish hook (the server can't roll
// back a config publish that already landed on disk just because a
// chain insert failed).
func recordSwarmPatternsChange(ctx context.Context, w *audit.Writer, substrate string, oldList, newList []string) {
	if w == nil {
		return
	}
	eventType := "swarm.enabled"
	if substrate != "swarm" {
		eventType = fmt.Sprintf("swarm.%s.enabled", substrate)
	}
	added, removed := diffStringSets(oldList, newList)
	summary := blacklistChangeSummary(added, removed)
	payload := fmt.Sprintf(`{"substrate":%q,"added":%s,"removed":%s,"new_list":%s}`,
		substrate, jsonStringArray(added), jsonStringArray(removed), jsonStringArray(newList))
	if err := w.Record(ctx, eventType, "config.reload", "config", summary, payload); err != nil {
		slog.Warn("audit: failed to record swarm patterns change",
			"substrate", substrate, "added", added, "removed", removed, "error", err)
	}
}

// diffStringSets returns (added, removed) — elements present in next but
// not prev, and elements present in prev but not next. Stable order is the
// order of appearance in the respective list.
func diffStringSets(prev, next []string) (added, removed []string) {
	prevSet := make(map[string]struct{}, len(prev))
	for _, s := range prev {
		prevSet[s] = struct{}{}
	}
	nextSet := make(map[string]struct{}, len(next))
	for _, s := range next {
		nextSet[s] = struct{}{}
	}
	for _, s := range next {
		if _, ok := prevSet[s]; !ok {
			added = append(added, s)
		}
	}
	for _, s := range prev {
		if _, ok := nextSet[s]; !ok {
			removed = append(removed, s)
		}
	}
	return added, removed
}

func blacklistChangeSummary(added, removed []string) string {
	switch {
	case len(added) > 0 && len(removed) > 0:
		return fmt.Sprintf("added: %s; removed: %s",
			strings.Join(added, ", "), strings.Join(removed, ", "))
	case len(added) > 0:
		return "added: " + strings.Join(added, ", ")
	case len(removed) > 0:
		return "removed: " + strings.Join(removed, ", ")
	default:
		return "no change"
	}
}

func jsonStringArray(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "[]"
	}
	return string(b)
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

// persistComplianceSnapshot writes per-framework standards compliance
// scores to the compliance_history table. Runs daily. Routes through the
// unified scorer (api.ReportHandler.ComputeFrameworkScoreCtx) so the
// snapshot numbers agree with every live UI page.
func persistComplianceSnapshot(ctx context.Context, queries *db.Queries, configStore *config.ConfigStore, reportHandler *api.ReportHandler) {
	if configStore == nil || reportHandler == nil {
		return
	}

	type fwEntry struct {
		name        string
		slug        string
		standardKey string
	}
	fwDefs := []fwEntry{
		{"HIPAA", "compliance", "hipaa"},
		{"HITRUST CSF", "hitrust", "hitrust"},
		{"SOC 2", "soc2", "soc2"},
		{"PCI DSS v4.0", "pci", "pci-dss"},
		{"NIST 800-53", "nist", "nist-800-53"},
		{"ISO 27001", "iso27001", "iso-27001"},
		{"GDPR", "gdpr", "gdpr"},
		{"NERC CIP", "nerccip", "nerc-cip"},
		{"Cyber Essentials+", "cyberessentials", "cyber-essentials"},
		{"IEC 62443", "iec62443", "iec-62443"},
		{"SOX", "sox", "sox"},
		{"FINRA", "finra", "finra"},
		{"MiFID II", "mifid2", "mifid2"},
		{"CCSS", "ccss", "ccss"},
		{"FDA 21 CFR Part 11", "fda21cfr11", "fda-21cfr11"},
		{"Gaming Commission", "gaming", "gaming"},
		{"CIS Windows", "ciswindows", "cis-windows"},
		{"CIS Ubuntu", "cisubuntu", "cis-ubuntu"},
		{"CIS RHEL", "cisrhel", "cis-rhel"},
		{"ABA Cybersecurity", "abacyber", "aba-cyber"},
		{"NY DFS Cyber (23 NYCRR 500)", "nydfscyber", "ny-dfs-cyber"},
	}

	recorded := 0
	for _, fw := range fwDefs {
		if !configStore.IsStandardEnabled(fw.standardKey) {
			continue
		}
		score, ok := reportHandler.ComputeFrameworkScoreCtx(ctx, fw.slug)
		if !ok {
			slog.Warn("compliance snapshot: scorer returned not-ok", "framework", fw.name, "slug", fw.slug)
			continue
		}
		if err := queries.UpsertComplianceSnapshot(ctx, db.UpsertComplianceSnapshotParams{
			Framework:     fw.name,
			ScorePercent:  score.Fleet.CoveragePercent,
			Satisfied:     int64(score.Fleet.Covered),
			Partial:       int64(score.Fleet.Partial),
			NotSatisfied:  int64(score.Fleet.Missing),
			TotalControls: int64(score.Total),
		}); err != nil {
			slog.Error("failed to persist compliance snapshot", "framework", fw.name, "error", err)
			continue
		}
		recorded++
	}
	slog.Info("compliance snapshot recorded", "frameworks", recorded)
}
