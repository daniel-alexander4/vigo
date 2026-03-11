package main

import (
	"context"
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
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	"github.com/hobnob/hobnob/server/api"
	"github.com/hobnob/hobnob/server/bootstrap"
	"github.com/hobnob/hobnob/server/ca"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	"github.com/hobnob/hobnob/server/federation"
	isgrpc "github.com/hobnob/hobnob/server/grpc"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/secrets"
	"github.com/hobnob/hobnob/server/watcher"
	"github.com/hobnob/hobnob/server/webhook"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Determine config paths relative to working directory.
	configFile := envOrDefault("HOBNOB_CONFIG_FILE", "server.yaml")
	nodeConfigsDir := envOrDefault("HOBNOB_NODE_CONFIGS_DIR", ".")
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

	// Connect to SQLite.
	sqlDB, err := sql.Open("sqlite3", cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer sqlDB.Close()

	// Enable SQLite features.
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enabling foreign keys: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("setting WAL mode: %w", err)
	}

	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}
	slog.Info("database connected")

	// Run migrations.
	absPath, err := filepath.Abs(migrationsDir)
	if err != nil {
		return fmt.Errorf("resolving migrations path: %w", err)
	}
	migDSN := "sqlite3://" + cfg.Database.DSN
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

	// Initialize subsystems.
	queries := db.New(sqlDB)
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
	nodeCacheTTL := parseDuration(cfg.Tuning.NodeCacheTTL, 0)
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

	// Create and start gRPC server.
	grpcSrv, _, lastSeenBatch, traitsPruner, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:         cfg.Server.TLS.CertFile,
		KeyFile:          cfg.Server.TLS.KeyFile,
		CAFile:           cfg.Server.TLS.CAFile,
		GRPCPort:         cfg.Server.GRPCListen,
		Registry:         reg,
		ConfigStore:      configStore,
		Queries:          queries,
		RunStore:         runStore,
		BaseInterval:     baseInterval,
		Dispatcher:       dispatcher,
		TraitsDir:        traitsDir,
		SignatureWindow:  sigWindow,
		NodeCacheTTL:     nodeCacheTTL,
		TraitsScannerTTL: traitsScannerTTL,
		Hub:              hub,
	})
	if err != nil {
		return fmt.Errorf("creating gRPC server: %w", err)
	}

	// Start async last_seen batcher.
	go lastSeenBatch.Start(ctx, lastSeenFlush)

	// Start async traits pruner.
	go traitsPruner.Start(ctx, traitsPruneInterval)

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
	metricsCollector := metrics.NewCollector(queries)
	go metricsCollector.Run(ctx, metricsInterval)

	// Start REST gateway (spoke serves API + metrics only; hub/standalone adds UI + bootstrap).
	var httpSrv *http.Server
	if role == config.RoleSpoke {
		mux, gwErr := api.NewSpokeGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Federation.SpokeID)
		if gwErr != nil {
			return fmt.Errorf("creating spoke gateway: %w", gwErr)
		}
		httpSrv = &http.Server{
			Addr:    cfg.Server.APIListen,
			Handler: mux,
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
		gwMux, gwErr := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version, gwOpts...)
		if gwErr != nil {
			return fmt.Errorf("creating REST gateway: %w", gwErr)
		}
		httpSrv = &http.Server{
			Addr:    cfg.Server.APIListen,
			Handler: gwMux,
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
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseDuration parses a duration string, returning def if s is empty or invalid.
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
