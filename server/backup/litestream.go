// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package backup manages continuous SQLite WAL replication via Litestream.
// It generates a litestream config file and runs `litestream replicate` as a
// managed subprocess.
package backup

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Config holds backup replication settings from server.yaml.
type Config struct {
	URL             string // s3://bucket/path, gcs://bucket/path, file:///path, sftp://host/path
	DBPath          string // SQLite database file path
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	Endpoint        string
	ForcePathStyle  bool
	Retention       string // e.g. "720h" (default 30 days)
	SyncInterval    string // e.g. "1s"
}

// Replicator manages a litestream subprocess for continuous WAL replication.
type Replicator struct {
	cfg        Config
	configPath string // generated litestream config file
	cmd        *exec.Cmd
}

// NewReplicator creates a replicator. Call Start() to begin replication.
func NewReplicator(cfg Config) *Replicator {
	return &Replicator{cfg: cfg}
}

// Start generates the litestream config and starts the subprocess.
// Blocks until the context is cancelled, then stops litestream gracefully.
func (r *Replicator) Start(ctx context.Context) error {
	if r.cfg.URL == "" {
		return fmt.Errorf("backup URL is empty")
	}

	// Verify litestream binary exists.
	path, err := exec.LookPath("litestream")
	if err != nil {
		return fmt.Errorf("litestream binary not found in PATH: %w", err)
	}
	slog.Info("litestream found", "path", path)

	// Generate config file.
	configPath, err := r.writeConfig()
	if err != nil {
		return fmt.Errorf("generating litestream config: %w", err)
	}
	r.configPath = configPath
	defer os.Remove(configPath)

	// Start litestream replicate subprocess.
	r.cmd = exec.CommandContext(ctx, "litestream", "replicate", "-config", configPath)
	r.cmd.Stdout = os.Stdout
	r.cmd.Stderr = os.Stderr

	// Pass S3/GCS credentials via environment.
	r.cmd.Env = os.Environ()
	if r.cfg.AccessKeyID != "" {
		r.cmd.Env = append(r.cmd.Env,
			"AWS_ACCESS_KEY_ID="+r.cfg.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY="+r.cfg.SecretAccessKey,
		)
	}

	slog.Info("starting litestream replication",
		"db", r.cfg.DBPath,
		"url", r.cfg.URL,
	)

	if err := r.cmd.Start(); err != nil {
		return fmt.Errorf("starting litestream: %w", err)
	}

	// Wait for process to exit (normally via context cancellation).
	err = r.cmd.Wait()
	if ctx.Err() != nil {
		// Context cancelled — normal shutdown.
		slog.Info("litestream replication stopped")
		return nil
	}
	if err != nil {
		return fmt.Errorf("litestream exited: %w", err)
	}
	return nil
}

// Restore downloads the latest snapshot and WAL files from the replica,
// rebuilding the database at the configured path. Call before opening the DB.
func (r *Replicator) Restore(ctx context.Context) error {
	if r.cfg.URL == "" {
		return fmt.Errorf("backup URL is empty")
	}

	configPath, err := r.writeConfig()
	if err != nil {
		return fmt.Errorf("generating litestream config: %w", err)
	}
	defer os.Remove(configPath)

	cmd := exec.CommandContext(ctx, "litestream", "restore", "-config", configPath, "-o", r.cfg.DBPath, r.cfg.DBPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if r.cfg.AccessKeyID != "" {
		cmd.Env = append(cmd.Env,
			"AWS_ACCESS_KEY_ID="+r.cfg.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY="+r.cfg.SecretAccessKey,
		)
	}

	slog.Info("restoring database from litestream backup", "url", r.cfg.URL)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("litestream restore: %w", err)
	}
	slog.Info("database restored from backup")
	return nil
}

// writeConfig generates a litestream YAML config file in a temp directory.
func (r *Replicator) writeConfig() (string, error) {
	replicaType, bucket, path, err := parseReplicaURL(r.cfg.URL)
	if err != nil {
		return "", err
	}

	retention := r.cfg.Retention
	if retention == "" {
		retention = "720h"
	}
	syncInterval := r.cfg.SyncInterval
	if syncInterval == "" {
		syncInterval = "1s"
	}

	var b strings.Builder
	b.WriteString("dbs:\n")
	b.WriteString("  - path: " + r.cfg.DBPath + "\n")
	b.WriteString("    replicas:\n")
	b.WriteString("      - type: " + replicaType + "\n")

	switch replicaType {
	case "s3":
		b.WriteString("        bucket: " + bucket + "\n")
		if path != "" {
			b.WriteString("        path: " + path + "\n")
		}
		if r.cfg.Region != "" {
			b.WriteString("        region: " + r.cfg.Region + "\n")
		}
		if r.cfg.Endpoint != "" {
			b.WriteString("        endpoint: " + r.cfg.Endpoint + "\n")
		}
		if r.cfg.ForcePathStyle {
			b.WriteString("        force-path-style: true\n")
		}
	case "gcs":
		b.WriteString("        bucket: " + bucket + "\n")
		if path != "" {
			b.WriteString("        path: " + path + "\n")
		}
	case "sftp":
		b.WriteString("        host: " + bucket + "\n")
		if path != "" {
			b.WriteString("        path: " + path + "\n")
		}
	case "file":
		b.WriteString("        path: " + bucket + path + "\n")
	}

	b.WriteString("        retention: " + retention + "\n")
	b.WriteString("        sync-interval: " + syncInterval + "\n")

	tmpDir := os.TempDir()
	configPath := filepath.Join(tmpDir, fmt.Sprintf("litestream-%d.yml", time.Now().UnixNano()))
	if err := os.WriteFile(configPath, []byte(b.String()), 0600); err != nil {
		return "", fmt.Errorf("writing config: %w", err)
	}
	return configPath, nil
}

// parseReplicaURL extracts the replica type and components from a URL.
// Supported schemes: s3://, gcs://, sftp://, file://
func parseReplicaURL(rawURL string) (replicaType, bucket, path string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid backup URL: %w", err)
	}

	switch u.Scheme {
	case "s3":
		return "s3", u.Host, strings.TrimPrefix(u.Path, "/"), nil
	case "gcs":
		return "gcs", u.Host, strings.TrimPrefix(u.Path, "/"), nil
	case "sftp":
		return "sftp", u.Host, strings.TrimPrefix(u.Path, "/"), nil
	case "file":
		return "file", u.Path, "", nil
	case "":
		return "", "", "", fmt.Errorf("backup URL missing scheme (expected s3://, gcs://, sftp://, or file://)")
	default:
		return "", "", "", fmt.Errorf("unsupported backup URL scheme: %s (expected s3, gcs, sftp, or file)", u.Scheme)
	}
}

// GenerateConfig returns the litestream YAML config content for the given settings.
// Exported for testing.
func GenerateConfig(cfg Config) (string, error) {
	r := &Replicator{cfg: cfg}
	path, err := r.writeConfig()
	if err != nil {
		return "", err
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
