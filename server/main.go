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
	"strings"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hobnob/hobnob/server/api"
	"github.com/hobnob/hobnob/server/bootstrap"
	"github.com/hobnob/hobnob/server/ca"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
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

	slog.Info("server config loaded",
		"grpc_listen", cfg.Server.GRPCListen,
		"api_listen", cfg.Server.APIListen,
	)

	// Connect to PostgreSQL.
	pool, err := pgxpool.New(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}
	slog.Info("database connected")

	// Run migrations.
	absPath, err := filepath.Abs(migrationsDir)
	if err != nil {
		return fmt.Errorf("resolving migrations path: %w", err)
	}
	migDSN := dsnToMigrateScheme(cfg.Database.DSN)
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
	queries := db.New(pool)
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

	// Create webhook dispatcher.
	dispatcher := webhook.NewDispatcher(queries, webhook.DispatcherConfig{
		Timeout:       whTimeout,
		MaxRetries:    cfg.Tuning.Webhook.MaxRetries,
		MaxConcurrent: cfg.Tuning.Webhook.MaxConcurrent,
		RetryDelay:    whRetryDelay,
	})

	// Create and start gRPC server.
	grpcSrv, _, lastSeenBatch, traitsPruner, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:         cfg.Server.TLS.CertFile,
		KeyFile:          cfg.Server.TLS.KeyFile,
		CAFile:           cfg.Server.TLS.CAFile,
		GRPCPort:         cfg.Server.GRPCListen,
		Registry:         reg,
		ConfigStore:      configStore,
		Queries:          queries,
		BaseInterval:     baseInterval,
		Dispatcher:       dispatcher,
		TraitsDir:        traitsDir,
		SignatureWindow:  sigWindow,
		NodeCacheTTL:     nodeCacheTTL,
		TraitsScannerTTL: traitsScannerTTL,
	})
	if err != nil {
		return fmt.Errorf("creating gRPC server: %w", err)
	}

	// Start async last_seen batcher.
	go lastSeenBatch.Start(ctx, lastSeenFlush)

	// Start async traits pruner.
	go traitsPruner.Start(ctx, traitsPruneInterval)

	// Start secret rotation watcher if enabled and provider supports versioning.
	if cfg.Watcher.Enabled {
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

	// Load CA for bootstrap CSR signing (optional — only if ca_key_file is configured).
	var gwOpts []api.GatewayOption
	if cfg.Server.TLS.CAKeyFile != "" {
		certAuthority, err := ca.Load(cfg.Server.TLS.CAFile, cfg.Server.TLS.CAKeyFile)
		if err != nil {
			return fmt.Errorf("loading CA for bootstrap: %w", err)
		}
		slog.Info("bootstrap CA loaded, CSR signing enabled")

		// Parse cert validity duration.
		certValidity := 365 * 24 * time.Hour
		if cfg.Bootstrap.CertValidity != "" {
			if parsed, err := time.ParseDuration(cfg.Bootstrap.CertValidity); err == nil {
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

	// Start REST gateway with UI.
	version := envOrDefault("HOBNOB_VERSION", "dev")
	agentDir := envOrDefault("HOBNOB_AGENT_DIR", "dist/agent")
	gwMux, err := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version, gwOpts...)
	if err != nil {
		return fmt.Errorf("creating REST gateway: %w", err)
	}

	httpSrv := &http.Server{
		Addr:    cfg.Server.APIListen,
		Handler: gwMux,
	}

	go func() {
		slog.Info("REST gateway starting", "addr", cfg.Server.APIListen)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("REST gateway error", "error", err)
		}
	}()

	slog.Info("server ready",
		"grpc", cfg.Server.GRPCListen,
		"rest", cfg.Server.APIListen,
	)

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

// dsnToMigrateScheme converts a postgres DSN to the pgx5 scheme used by
// golang-migrate. Handles both postgres:// and postgresql:// prefixes.
func dsnToMigrateScheme(dsn string) string {
	switch {
	case strings.HasPrefix(dsn, "postgres://"):
		return "pgx5://" + dsn[len("postgres://"):]
	case strings.HasPrefix(dsn, "postgresql://"):
		return "pgx5://" + dsn[len("postgresql://"):]
	default:
		return dsn
	}
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
