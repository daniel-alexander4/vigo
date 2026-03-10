// Command seed inserts development test data into the Hobnob database.
// It is idempotent -- safe to run multiple times.
//
// Usage: go run ./cmd/seed/main.go
// Or: make seed
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	dsn := os.Getenv("HOBNOB_DB_DSN")
	if dsn == "" {
		dsn = "postgres://hobnob:secret@localhost:5432/hobnob?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging database: %w", err)
	}

	slog.Info("connected to database, seeding data...")

	// Insert approved patterns (ON CONFLICT to be idempotent).
	patterns := []struct {
		pattern   string
		tokenHash string // Pre-hashed bcrypt for dev convenience. Token: "dev-token-123"
	}{
		{
			pattern:   "*.web.example.com",
			tokenHash: "$2a$10$KJYqz0/I.J3v7XJ4/V2v.e4mNg6y5xkKl7vC8G1Rj0uB6NqZ8w3zW",
		},
		{
			pattern:   "*.db.example.com",
			tokenHash: "$2a$10$KJYqz0/I.J3v7XJ4/V2v.e4mNg6y5xkKl7vC8G1Rj0uB6NqZ8w3zW",
		},
		{
			pattern:   "10.0.1.*",
			tokenHash: "$2a$10$KJYqz0/I.J3v7XJ4/V2v.e4mNg6y5xkKl7vC8G1Rj0uB6NqZ8w3zW",
		},
	}

	for _, p := range patterns {
		_, err := pool.Exec(ctx, `
			INSERT INTO approved_patterns (pattern, token_hash)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, p.pattern, p.tokenHash)
		if err != nil {
			return fmt.Errorf("inserting pattern %q: %w", p.pattern, err)
		}
		slog.Info("seeded approved pattern", "pattern", p.pattern)
	}

	// Insert sample nodes (ON CONFLICT DO NOTHING on hostname).
	nodes := []struct {
		hostname  string
		ipAddress string
		pubkeyPEM string
	}{
		{
			hostname:  "web01.web.example.com",
			ipAddress: "10.0.1.10",
			pubkeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA+dev+key+web01+placeholder+000000000000=\n-----END PUBLIC KEY-----\n",
		},
		{
			hostname:  "db01.db.example.com",
			ipAddress: "10.0.1.20",
			pubkeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEA+dev+key+db01+placeholder+0000000000000=\n-----END PUBLIC KEY-----\n",
		},
	}

	for _, n := range nodes {
		_, err := pool.Exec(ctx, `
			INSERT INTO nodes (hostname, ip_address, pubkey_pem)
			VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING
		`, n.hostname, n.ipAddress, n.pubkeyPEM)
		if err != nil {
			return fmt.Errorf("inserting node %q: %w", n.hostname, err)
		}
		slog.Info("seeded node", "hostname", n.hostname)
	}

	// Create sample node config files if they don't exist.
	if err := writeNodeConfig("config/nodes/web01.web.example.com.yaml", webNodeConfig); err != nil {
		return err
	}
	if err := writeNodeConfig("config/nodes/db01.db.example.com.yaml", dbNodeConfig); err != nil {
		return err
	}

	slog.Info("seed complete")
	return nil
}

func writeNodeConfig(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		slog.Info("node config already exists, skipping", "path", path)
		return nil
	}
	if err := os.MkdirAll("config/nodes", 0755); err != nil {
		return fmt.Errorf("creating config/nodes dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	slog.Info("created node config", "path", path)
	return nil
}

const webNodeConfig = `# This is a legacy seed file — not used by the config loader.
# Real configs live in /etc/hobnob-server/modules/ and /etc/hobnob-server/nodes/.
`

const dbNodeConfig = `# This is a legacy seed file — not used by the config loader.
# Real configs live in /etc/hobnob-server/modules/ and /etc/hobnob-server/nodes/.
`
