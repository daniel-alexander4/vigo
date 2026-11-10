// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

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
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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

// Replicator supervision: litestream is restarted whenever it exits while the
// server is running, with exponential backoff between attempts. A run that
// stayed up for at least restartResetAfter resets the backoff.
const (
	restartMinBackoff = time.Second
	restartMaxBackoff = 5 * time.Minute
	restartResetAfter = time.Minute
)

// Metrics are the replicator's Prometheus series. Register them in the
// server's registry with NewMetrics; a nil *Metrics records nothing.
type Metrics struct {
	// Running is 1 while a litestream process is up, 0 otherwise.
	Running prometheus.Gauge
	// Exits counts litestream exits while the server was running (each is
	// followed by a restart after backoff).
	Exits prometheus.Counter
}

// NewMetrics creates and registers the replicator metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Running: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_backup_replicator_running",
			Help: "1 while the litestream WAL replicator process is running, 0 while it is down or waiting to restart.",
		}),
		Exits: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vigo_backup_replicator_exits_total",
			Help: "Times the litestream WAL replicator exited unexpectedly and was restarted.",
		}),
	}
	reg.MustRegister(m.Running, m.Exits)
	return m
}

func (m *Metrics) setRunning(up bool) {
	if m == nil {
		return
	}
	if up {
		m.Running.Set(1)
	} else {
		m.Running.Set(0)
	}
}

func (m *Metrics) exited() {
	if m != nil {
		m.Exits.Inc()
	}
}

// Replicator manages a litestream subprocess for continuous WAL replication.
type Replicator struct {
	cfg     Config
	metrics *Metrics

	// bin and the backoff bounds are fields so tests can supervise a stub.
	bin        string
	minBackoff time.Duration
	maxBackoff time.Duration
	resetAfter time.Duration
}

// NewReplicator creates a replicator. Call Start() to begin replication.
// m may be nil.
func NewReplicator(cfg Config, m *Metrics) *Replicator {
	return &Replicator{
		cfg:        cfg,
		metrics:    m,
		bin:        "litestream",
		minBackoff: restartMinBackoff,
		maxBackoff: restartMaxBackoff,
		resetAfter: restartResetAfter,
	}
}

// Start generates the litestream config and supervises `litestream
// replicate` until ctx is cancelled: every unexpected exit is logged,
// counted (vigo_backup_replicator_exits_total) and followed by a restart
// after exponential backoff. Returns nil on cancellation; an error only for a
// setup failure (no binary, bad URL) that a restart cannot fix.
func (r *Replicator) Start(ctx context.Context) error {
	if r.cfg.URL == "" {
		return fmt.Errorf("backup URL is empty")
	}
	path, err := exec.LookPath(r.bin)
	if err != nil {
		return fmt.Errorf("litestream binary not found in PATH: %w", err)
	}
	slog.Info("litestream found", "path", path)

	configPath, err := r.writeConfig()
	if err != nil {
		return fmt.Errorf("generating litestream config: %w", err)
	}
	defer os.Remove(configPath)

	slog.Info("starting litestream replication", "db", r.cfg.DBPath, "url", r.cfg.URL)
	backoff := r.minBackoff
	for {
		started := time.Now()
		runErr := r.runOnce(ctx, path, configPath)
		r.metrics.setRunning(false)
		if ctx.Err() != nil {
			slog.Info("litestream replication stopped")
			return nil
		}
		if time.Since(started) >= r.resetAfter {
			backoff = r.minBackoff
		}
		r.metrics.exited()
		slog.Error("litestream exited; backups are paused until it restarts",
			"error", runErr, "uptime", time.Since(started).Round(time.Second), "restart_in", backoff)
		select {
		case <-ctx.Done():
			slog.Info("litestream replication stopped")
			return nil
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > r.maxBackoff {
			backoff = r.maxBackoff
		}
	}
}

// runOnce runs one litestream replicate process to completion.
func (r *Replicator) runOnce(ctx context.Context, bin, configPath string) error {
	cmd := exec.CommandContext(ctx, bin, "replicate", "-config", configPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = r.credentialEnv()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting litestream: %w", err)
	}
	r.metrics.setRunning(true)
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("litestream exited: %w", err)
	}
	return fmt.Errorf("litestream exited with status 0")
}

// credentialEnv passes the S3/GCS credentials to litestream through its
// environment: never argv (world-readable via /proc/<pid>/cmdline) and never
// the generated config file on disk. The environment is readable only by the
// same uid and root. In server.yaml the keys should be secret: references so
// the plaintext lives only in the secrets backend.
func (r *Replicator) credentialEnv() []string {
	env := os.Environ()
	if r.cfg.AccessKeyID != "" {
		env = append(env,
			"AWS_ACCESS_KEY_ID="+r.cfg.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY="+r.cfg.SecretAccessKey,
		)
	}
	return env
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

	cmd := exec.CommandContext(ctx, r.bin, "restore", "-config", configPath, "-o", r.cfg.DBPath, r.cfg.DBPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = r.credentialEnv()

	slog.Info("restoring database from litestream backup", "url", r.cfg.URL)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("litestream restore: %w", err)
	}
	slog.Info("database restored from backup")
	return nil
}

// writeConfig generates the litestream YAML config in a fresh 0600 temp file
// (os.CreateTemp: unpredictable name, O_EXCL), so another local user cannot
// pre-create or swap it. The caller removes it.
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

	f, err := os.CreateTemp("", "litestream-*.yml")
	if err != nil {
		return "", fmt.Errorf("creating config file: %w", err)
	}
	configPath := f.Name()
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(configPath)
		return "", fmt.Errorf("writing config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(configPath)
		return "", fmt.Errorf("writing config: %w", err)
	}
	return configPath, nil
}

// SQLitePathFromDSN returns the database file path named by a SQLite DSN:
// a "file:" URI prefix and any "?pragma=..." query are stripped. Litestream
// needs the file path, not the driver DSN.
func SQLitePathFromDSN(dsn string) string {
	p := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return p
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
	r := NewReplicator(cfg, nil)
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
