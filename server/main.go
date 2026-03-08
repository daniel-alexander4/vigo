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

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hobnob/hobnob/server/api"
	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/db"
	isgrpc "github.com/hobnob/hobnob/server/grpc"
	"github.com/hobnob/hobnob/server/metrics"
	"github.com/hobnob/hobnob/server/registry"
	"github.com/hobnob/hobnob/server/secrets"
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
	configFile := envOrDefault("HOBNOB_CONFIG_FILE", "server-configs/server.yaml")
	clientConfigsDir := envOrDefault("HOBNOB_CLIENT_CONFIGS_DIR", "client-configs")
	migrationsDir := envOrDefault("HOBNOB_MIGRATIONS_DIR", "migrations")
	traitsDir := envOrDefault("HOBNOB_TRAITS_DIR", "traits")

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
	migDSN := "pgx5://" + cfg.Database.DSN[len("postgres://"):]
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

	configStore, err := config.NewConfigStore(clientConfigsDir, provider)
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
	baseInterval := 30 * time.Minute // default
	if cfg.CheckIn.BaseInterval != "" {
		if parsed, err := time.ParseDuration(cfg.CheckIn.BaseInterval); err == nil {
			baseInterval = parsed
		}
	}

	// Create webhook dispatcher.
	dispatcher := webhook.NewDispatcher(queries)

	// Create and start gRPC server.
	grpcSrv, _, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:     cfg.Server.TLS.CertFile,
		KeyFile:      cfg.Server.TLS.KeyFile,
		CAFile:       cfg.Server.TLS.CAFile,
		GRPCPort:     cfg.Server.GRPCListen,
		Registry:     reg,
		ConfigStore:  configStore,
		Queries:      queries,
		BaseInterval: baseInterval,
		Dispatcher:   dispatcher,
		TraitsDir:    traitsDir,
	})
	if err != nil {
		return fmt.Errorf("creating gRPC server: %w", err)
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
	go metricsCollector.Run(ctx, 30*time.Second)

	// Start REST gateway with UI.
	version := envOrDefault("HOBNOB_VERSION", "dev")
	agentDir := envOrDefault("HOBNOB_AGENT_DIR", "dist/agent")
	gwMux, err := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile, queries, configStore, baseInterval, cfg.Server.APIListen, agentDir, version)
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

// envOrDefault returns the environment variable value or a default.
func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
