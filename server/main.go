// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"database/sql"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"

	hobnobai "github.com/hobnob/hobnob/server/ai"
	"github.com/hobnob/hobnob/server/api"
	"github.com/hobnob/hobnob/server/backup"
	"github.com/hobnob/hobnob/server/bootstrap"
	"github.com/hobnob/hobnob/server/ca"
	"github.com/hobnob/hobnob/server/compliance"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	"github.com/hobnob/hobnob/server/fleet"
	isgrpc "github.com/hobnob/hobnob/server/grpc"
	"github.com/hobnob/hobnob/server/license"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/secrets"
	hobnobsmtp "github.com/hobnob/hobnob/server/smtp"
	"github.com/hobnob/hobnob/server/tasks"
	"github.com/hobnob/hobnob/server/watcher"
	"github.com/hobnob/hobnob/server/webhook"
	"github.com/hobnob/hobnob/server/workflows"
)

func main() {
	if err := run(); err != nil {
		var se *config.SecretErrors
		var ie *config.IdempotencyErrors
		switch {
		case errors.As(err, &se):
			fmt.Fprint(os.Stderr, se.Format())
		case errors.As(err, &ie):
			fmt.Fprint(os.Stderr, ie.Format())
		default:
			slog.Error("server failed", "error", err)
		}
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Determine config paths relative to working directory.
	configFile := envOrDefault("HOBNOB_CONFIG_FILE", "server.yaml")
	nodeConfigsDir := envOrDefault("HOBNOB_THRALL_CONFIGS_DIR", ".")
	migrationsDir := envOrDefault("HOBNOB_MIGRATIONS_DIR", "migrations")
	traitsDir := envOrDefault("HOBNOB_CUSTOM_TRAITS_DIR", "custom-traits")

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

	// Load full server config with secret resolution (second pass).
	cfg, err := config.LoadServerConfig(configFile, provider)
	if err != nil {
		return fmt.Errorf("loading server config: %w", err)
	}

	// Determine federation role.
	role := cfg.Federation.Role
	if role == "" {
		role = config.RoleStandalone
	}
	if err := cfg.Federation.Validate(); err != nil {
		return err
	}

	slog.Info("server config loaded",
		"role", role,
		"grpc_listen", cfg.Server.GRPCListen,
		"api_listen", cfg.Server.APIListen,
	)

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

	// Load and validate license.
	licenseDir := envOrDefault("HOBNOB_LICENSE_DIR", license.DefaultLicenseDir)
	licenseGraceDir := envOrDefault("HOBNOB_LICENSE_GRACE_DIR", licenseDir)

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

	enforcer, err := license.NewEnforcer(licenseDir, licenseGraceDir, thrallCounter, func() { cancel() })
	if err != nil {
		return fmt.Errorf("license: %w", err)
	}

	slog.Info("license loaded",
		"license_id", enforcer.GetStatus().License.LicenseID,
		"stage", enforcer.GetStatus().Stage.String(),
	)

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

	// Initialize subsystems.
	reg := registry.NewRegistry(queries)

	dangerous := os.Getenv("HOBNOB_DANGEROUS") == "1" || hasFlag("--dangerous")
	configStore, err := config.NewConfigStore(nodeConfigsDir, provider, dangerous)
	if err != nil {
		return fmt.Errorf("initializing config store: %w", err)
	}

	// Start config file watcher in background.
	go func() {
		if err := configStore.Watch(ctx); err != nil && ctx.Err() == nil {
			slog.Error("config watcher error", "error", err)
		}
	}()

	// Parse base interval for compliance stale threshold.
	baseInterval := 5 * time.Minute // default
	if cfg.CheckIn.BaseInterval != "" {
		if parsed, err := time.ParseDuration(cfg.CheckIn.BaseInterval); err == nil {
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
	var hub *federation.Hub
	if role == config.RoleHub {
		hub, err = federation.NewHub(ctx, cfg.Federation.Spokes, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile)
		if err != nil {
			return fmt.Errorf("connecting to spokes: %w", err)
		}
		defer hub.Close()
		slog.Info("federation hub initialized", "spokes", hub.SpokeCount())
	}

	// Load task definitions.
	tasksDir := envOrDefault("HOBNOB_TASKS_DIR", "tasks")
	taskStore := tasks.NewStore()
	if err := taskStore.Load(tasksDir); err != nil {
		return fmt.Errorf("loading tasks: %w", err)
	}
	slog.Info("tasks loaded", "count", len(taskStore.List()), "dir", tasksDir)

	// Create and rehydrate FleetIndex.
	fleetIndex := fleet.NewFleetIndex()
	if err := fleet.Rehydrate(ctx, fleetIndex, queries, runStore, baseInterval); err != nil {
		return fmt.Errorf("rehydrating fleet index: %w", err)
	}

	// Create async run batcher for delta events.
	runBatcher := fleet.NewRunBatcher(runStore)

	// Initialize bundle signer for compiled promises.
	bundleSigner, bundleMaxAge := initBundleSigner(cfg)

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
		BaseInterval:         baseInterval,
		Dispatcher:           dispatcher,
		TraitsDir:            traitsDir,
		SignatureWindow:      sigWindow,
		TraitsScannerTTL:     traitsScannerTTL,
		MaxConcurrentStreams: cfg.Tuning.MaxConcurrentStreams,
		Hub:                  hub,
		SMTPNotifier:         smtpNotifier,
		LicenseEnforcer:      enforcer,
		TaskStore:            taskStore,
		FleetIndex:           fleetIndex,
		RunBatcher:           runBatcher,
		BundleSigner:        bundleSigner,
		BundleMaxAge:        bundleMaxAge,
	})
	if err != nil {
		return fmt.Errorf("creating gRPC server: %w", err)
	}

	// Start async flushers with staggered offsets to avoid SQLite write lock
	// contention. For default 10s interval → offsets of 0s, ~3.3s, ~6.6s.
	stateFlusher := fleet.NewStateFlusher(fleetIndex, queries)
	go stateFlusher.Start(ctx, lastSeenFlush, 0)
	go runBatcher.Start(ctx, lastSeenFlush, lastSeenFlush/3)
	go traitsBatch.Start(ctx, lastSeenFlush, 2*lastSeenFlush/3)

	// Start async traits pruner.
	go traitsPruner.Start(ctx, traitsPruneInterval)

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

	// Start metrics collector in background.
	metricsCollector := metrics.NewCollector(queries, sqlDB)
	go metricsCollector.Run(ctx, metricsInterval)

	// Start stale thrall checker — fires thrall.stale SMTP events on transition.
	go runStaleChecker(ctx, queries, fleetIndex, smtpNotifier, baseInterval)

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
		BaseInterval:   baseInterval,
		Hub:            hub,
		SMTPHost:       cfg.SMTP.Host,
		SMTPPort:       cfg.SMTP.Port,
		Role:           role,
		SecretsBackend: secretsCfg.Backend,
	})

	federationHandler := api.NewFederationHandler(role, cfg.Federation.SpokeID, hub)
	tasksHandler := api.NewTasksHandler(queries, agentSvc)
	queryHandler := api.NewQueryHandler(agentSvc)

	// Load workflow definitions and create engine.
	workflowsDir := envOrDefault("HOBNOB_WORKFLOWS_DIR", "workflows")
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
			assistant = hobnobai.NewAssistant(aiProvider, queries, configStore, runStore, baseInterval, cfg.AI)
			aiHandler = api.NewAIHandler(assistant)
			slog.Info("AI assistant enabled", "provider", aiProvider.Name(), "model", cfg.AI.Model)
		}
	}

	if role == config.RoleSpoke {
		mux, gwErr := api.NewSpokeGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Federation.SpokeID)
		if gwErr != nil {
			return fmt.Errorf("creating spoke gateway: %w", gwErr)
		}
		licenseHandler.RegisterRoutes(mux)
		doctorHandler.RegisterRoutes(mux)
		federationHandler.RegisterRoutes(mux)
		tasksHandler.RegisterRoutes(mux)
		queryHandler.RegisterRoutes(mux)
		workflowHandler.RegisterRoutes(mux)
		if aiHandler != nil {
			aiHandler.RegisterRoutes(mux)
		}
		httpSrv = &http.Server{
			Addr:    cfg.Server.APIListen,
			Handler: api.MaxBodyMiddleware(mux),
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

		version := envOrDefault("HOBNOB_VERSION", "dev")
		agentDir := envOrDefault("HOBNOB_AGENT_DIR", "dist/agent")
		gwMux, gwErr := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version, assistant, hub, runStore, fleetIndex, gwOpts...)
		if gwErr != nil {
			return fmt.Errorf("creating REST gateway: %w", gwErr)
		}
		licenseHandler.RegisterRoutes(gwMux)
		doctorHandler.RegisterRoutes(gwMux)
		federationHandler.RegisterRoutes(gwMux)
		tasksHandler.RegisterRoutes(gwMux)
		queryHandler.RegisterRoutes(gwMux)
		workflowHandler.RegisterRoutes(gwMux)
		if aiHandler != nil {
			aiHandler.RegisterRoutes(gwMux)
		}
		httpSrv = &http.Server{
			Addr:    cfg.Server.APIListen,
			Handler: api.MaxBodyMiddleware(gwMux),
		}
	}

	go func() {
		slog.Info("REST gateway starting", "addr", cfg.Server.APIListen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("REST gateway error", "error", err)
		}
	}()

	if role == config.RoleSpoke {
		slog.Info("spoke ready",
			"spoke_id", cfg.Federation.SpokeID,
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

// runStaleChecker periodically scans thralls and fires a thrall.stale SMTP
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
				var ri *compliance.RunInfo
				if run, ok := latestRuns[id]; ok {
					ri = &compliance.RunInfo{
						Status:       run.Status,
						ChangedCount: int64(run.ChangedCount),
					}
				}
				lastSeen := time.Time{}
				if t.LastSeen.Valid {
					lastSeen = t.LastSeen.Time
				}
				if compliance.ComputeStatus(ri, lastSeen, baseInterval) == "compliant" {
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
