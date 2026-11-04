// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPublishHonoursPersistedFreeze: a freeze set through SetFreeze (the
// REST handler's path) blocks Publish and PublishFile, which is what
// vigocli calls in its own process.
func TestPublishHonoursPersistedFreeze(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)
	if err := SetFreeze(env.live, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(noURLOpts(env)); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("Publish under freeze = %v, want a freeze refusal", err)
	}
	if _, err := PublishFile("configcrates/nginx.vgo", noURLOpts(env)); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("PublishFile under freeze = %v, want a freeze refusal", err)
	}
	if err := ClearFreeze(env.live); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(noURLOpts(env)); err != nil {
		t.Fatalf("Publish after ClearFreeze: %v", err)
	}
}

// TestFreezeFileMalformedFailsClosed: a freeze file that cannot be parsed
// blocks publishes rather than silently lifting the freeze.
func TestFreezeFileMalformedFailsClosed(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)
	if err := os.WriteFile(stateFile(env.live, freezeFileName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(noURLOpts(env)); err == nil {
		t.Fatal("Publish with a malformed freeze file succeeded")
	}
}

// TestSnapshotNamesUniqueWithinASecond: back-to-back snapshots never share
// an archive.
func TestSnapshotNamesUniqueWithinASecond(t *testing.T) {
	src := t.TempDir()
	hist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "configcrates"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		v, err := SnapshotTree(src, hist, 20)
		if err != nil {
			t.Fatal(err)
		}
		if seen[v] {
			t.Fatalf("snapshot version %s reused", v)
		}
		seen[v] = true
	}
	versions, _ := ListVersions(hist)
	if len(versions) != 5 {
		t.Fatalf("got %d archives, want 5", len(versions))
	}
}

// TestPublishAbortsWhenSnapshotFails: a publish that cannot be snapshotted
// never reaches .live.
func TestPublishAbortsWhenSnapshotFails(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)
	opts := noURLOpts(env)
	// A history "dir" that is a regular file cannot hold snapshots.
	opts.HistoryDir = filepath.Join(t.TempDir(), "history-is-a-file")
	if err := os.WriteFile(opts.HistoryDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(opts); err == nil {
		t.Fatal("Publish with an unwritable history dir succeeded")
	}
	if _, err := os.Stat(filepath.Join(env.live, "configcrates", "nginx.vgo")); err == nil {
		t.Fatal(".live was written although the snapshot failed")
	}
}

// TestRollbackTargetIsPredecessorOfPublished: the monitor rolls back to the
// snapshot that preceded the version it was started for, not blindly to
// versions[1], and refuses when .live has moved on.
func TestRollbackTargetIsPredecessorOfPublished(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, ".live")
	hist := filepath.Join(root, "config-history")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	var vs []string
	for i := 0; i < 3; i++ {
		v, err := SnapshotTree(live, hist, 20)
		if err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	// .live holds the middle snapshot (e.g. after a manual rollback).
	if err := recordLiveVersion(live, vs[1]); err != nil {
		t.Fatal(err)
	}
	got, err := rollbackTarget(hist, live, vs[1])
	if err != nil || got != vs[0] {
		t.Fatalf("rollbackTarget = %q, %v; want %q", got, err, vs[0])
	}
	if _, err := rollbackTarget(hist, live, vs[2]); err == nil {
		t.Fatal("rollbackTarget for a version .live no longer holds should refuse")
	}
	if _, err := rollbackTarget(hist, live, ""); err == nil {
		t.Fatal("rollbackTarget with an unknown live version should refuse")
	}
}

// TestRollbackWaitsForPublishLock: a rollback cannot swap .live while a
// publish holds the lock.
func TestRollbackWaitsForPublishLock(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, ".live")
	hist := filepath.Join(root, "config-history")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	v, err := SnapshotTree(live, hist, 20)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockPublish(live)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- RollbackToVersion(hist, v, live) }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("RollbackToVersion finished (%v) while the publish lock was held", err)
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("RollbackToVersion after unlock: %v", err)
	}
	if LiveVersion(live) != v {
		t.Fatalf("LiveVersion = %q, want %q", LiveVersion(live), v)
	}
}

// TestPublishHonoursServerYAMLFreeze: maintenance.freeze_until in the
// server root's server.yaml blocks publishes.
func TestPublishHonoursServerYAMLFreeze(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if err := os.WriteFile(stateFile(env.live, "server.yaml"), []byte("maintenance:\n  freeze_until: \""+until+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish(noURLOpts(env)); err == nil || !strings.Contains(err.Error(), "maintenance.freeze_until") {
		t.Fatalf("Publish under server.yaml freeze = %v, want a freeze refusal", err)
	}
}
