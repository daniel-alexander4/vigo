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

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/isogov/isogov/server/api"
	"github.com/isogov/isogov/server/config"
	"github.com/isogov/isogov/server/db"
	isgrpc "github.com/isogov/isogov/server/grpc"
	"github.com/isogov/isogov/server/registry"
	"github.com/isogov/isogov/server/secrets"
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
	envFile := envOrDefault("ISOGOV_ENV_FILE", ".env")
	configFile := envOrDefault("ISOGOV_CONFIG_FILE", "config/server.yaml")
	clientsDir := envOrDefault("ISOGOV_CLIENTS_DIR", "config/clients")
	classificationFile := envOrDefault("ISOGOV_CLASSIFICATION_FILE", "config/classification.yaml")
	migrationsDir := envOrDefault("ISOGOV_MIGRATIONS_DIR", "migrations")

	// Initialize secrets provider (StaticProvider for dev, IsopassProvider for prod in Phase 3).
	provider, err := secrets.NewStaticProvider(envFile)
	if err != nil {
		return fmt.Errorf("initializing secrets provider: %w", err)
	}
	defer provider.Close()

	// Load server config with secret resolution.
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

	configStore, err := config.NewConfigStore(clientsDir, classificationFile, provider)
	if err != nil {
		return fmt.Errorf("initializing config store: %w", err)
	}

	// Start config file watcher in background.
	go func() {
		if err := configStore.Watch(ctx); err != nil && ctx.Err() == nil {
			slog.Error("config watcher error", "error", err)
		}
	}()

	// Create and start gRPC server.
	grpcSrv, err := isgrpc.NewServer(isgrpc.ServerConfig{
		CertFile:    cfg.Server.TLS.CertFile,
		KeyFile:     cfg.Server.TLS.KeyFile,
		CAFile:      cfg.Server.TLS.CAFile,
		GRPCPort:    cfg.Server.GRPCListen,
		Registry:    reg,
		ConfigStore: configStore,
		Queries:     queries,
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

	// Start REST gateway.
	gwMux, err := api.NewGateway(ctx, cfg.Server.GRPCListen, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, cfg.Server.TLS.CAFile)
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
