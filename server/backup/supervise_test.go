// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A litestream that dies is restarted (with backoff) and every exit is
// counted, instead of backups stopping silently after the first exit.
func TestReplicatorRestartsAfterExit(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	stub := filepath.Join(dir, "litestream-stub")
	script := "#!/bin/sh\necho x >> " + counter + "\nexit 1\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	r := NewReplicator(Config{URL: "file:///tmp/x", DBPath: filepath.Join(dir, "vigo.db")}, m)
	r.bin = stub
	r.minBackoff = 10 * time.Millisecond
	r.maxBackoff = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(counter)
		if strings.Count(string(b), "x") >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Start returned %v after cancel", err)
	}

	b, _ := os.ReadFile(counter)
	if runs := strings.Count(string(b), "x"); runs < 3 {
		t.Fatalf("litestream ran %d times, want it restarted after each exit", runs)
	}
	if exits := testutil.ToFloat64(m.Exits); exits < 2 {
		t.Fatalf("vigo_backup_replicator_exits_total = %v, want >= 2", exits)
	}
	if up := testutil.ToFloat64(m.Running); up != 0 {
		t.Fatalf("vigo_backup_replicator_running = %v after stop, want 0", up)
	}
}

// The generated config is a fresh 0600 file with an unpredictable name.
func TestWriteConfigUsesPrivateTempFile(t *testing.T) {
	r := NewReplicator(Config{URL: "s3://b/p", DBPath: "/db/vigo.db"}, nil)
	p1, err := r.writeConfig()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p1)
	p2, err := r.writeConfig()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p2)
	if p1 == p2 {
		t.Fatal("two configs got the same path")
	}
	fi, err := os.Stat(p1)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestSQLitePathFromDSN(t *testing.T) {
	for in, want := range map[string]string{
		"/srv/vigo/db/vigo.db":                      "/srv/vigo/db/vigo.db",
		"file:/srv/vigo/db/vigo.db?_busy_timeout=5": "/srv/vigo/db/vigo.db",
		"/srv/vigo/db/vigo.db?_journal_mode=WAL":    "/srv/vigo/db/vigo.db",
	} {
		if got := SQLitePathFromDSN(in); got != want {
			t.Errorf("SQLitePathFromDSN(%q) = %q, want %q", in, got, want)
		}
	}
}
