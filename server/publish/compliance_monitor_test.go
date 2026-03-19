// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package publish

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daniel-alexander4/vigo/server/fleet"
)

func TestComplianceMonitorNoRollbackWhenHealthy(t *testing.T) {
	fi := fleet.NewFleetIndex()
	// Add 10 compliant envoys.
	for i := 0; i < 10; i++ {
		id := "id-" + string(rune('a'+i))
		fi.AddEnvoy(fleet.EnvoyState{
			ID:               id,
			Hostname:         "host-" + id,
			ComplianceStatus: "compliant",
		}, nil)
	}

	var rollbackCalled atomic.Int32
	m := NewComplianceMonitor(fi, func() error {
		rollbackCalled.Add(1)
		return nil
	})

	m.Start(50, 500*time.Millisecond)

	// Wait for the window to elapse.
	time.Sleep(800 * time.Millisecond)

	if rollbackCalled.Load() != 0 {
		t.Fatal("rollback should not have been called when fleet is healthy")
	}
}

func TestComplianceMonitorTriggersRollback(t *testing.T) {
	// Set up temp directories for config history.
	tmpDir := t.TempDir()
	liveDir := filepath.Join(tmpDir, ".live")
	histDir := filepath.Join(tmpDir, "history")

	// Override package-level dirs.
	origLive := LiveDir
	origHist := HistoryDir
	LiveDir = liveDir
	HistoryDir = histDir
	t.Cleanup(func() {
		LiveDir = origLive
		HistoryDir = origHist
	})

	// Create a live dir with some content and take two snapshots.
	// Snapshots use second-level timestamps so we must space them apart.
	os.MkdirAll(filepath.Join(liveDir, "modules"), 0o755)
	os.WriteFile(filepath.Join(liveDir, "modules", "good.vgo"), []byte("name: good"), 0o644)
	// Snapshot 1 (the "previous good" config).
	v1, err := SnapshotLive(liveDir, histDir, 20)
	if err != nil {
		t.Fatal(err)
	}

	// Wait so the second snapshot gets a different timestamp.
	time.Sleep(1100 * time.Millisecond)

	// Modify live dir and take another snapshot (the "bad" config that triggers rollback).
	os.WriteFile(filepath.Join(liveDir, "modules", "bad.vgo"), []byte("name: bad"), 0o644)
	_, err = SnapshotLive(liveDir, histDir, 20)
	if err != nil {
		t.Fatal(err)
	}

	fi := fleet.NewFleetIndex()
	// Add 10 envoys — 3 compliant, 7 failing (30% compliance).
	for i := 0; i < 3; i++ {
		id := "c-" + string(rune('a'+i))
		fi.AddEnvoy(fleet.EnvoyState{
			ID:               id,
			Hostname:         "compliant-" + id,
			ComplianceStatus: "compliant",
		}, nil)
	}
	for i := 0; i < 7; i++ {
		id := "f-" + string(rune('a'+i))
		fi.AddEnvoy(fleet.EnvoyState{
			ID:               id,
			Hostname:         "failing-" + id,
			ComplianceStatus: "failing",
		}, nil)
	}

	var rollbackCalled atomic.Int32
	m := NewComplianceMonitor(fi, func() error {
		rollbackCalled.Add(1)
		return nil
	})

	// Threshold 50% — fleet is at 30%, should trigger rollback.
	m.Start(50, 30*time.Second)

	// Wait for at least one poll cycle.
	time.Sleep(8 * time.Second)

	if rollbackCalled.Load() == 0 {
		t.Fatal("rollback should have been triggered when fleet compliance is below threshold")
	}

	// Verify rollback restored v1 content (good.vgo exists, bad.vgo does not).
	_ = v1
	if _, err := os.Stat(filepath.Join(liveDir, "modules", "good.vgo")); err != nil {
		t.Fatalf("good.vgo should exist after rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(liveDir, "modules", "bad.vgo")); err == nil {
		t.Fatal("bad.vgo should not exist after rollback (it was only in the bad snapshot)")
	}
}

func TestComplianceMonitorCancelsPrevious(t *testing.T) {
	fi := fleet.NewFleetIndex()
	fi.AddEnvoy(fleet.EnvoyState{
		ID:               "a",
		Hostname:         "a.example.com",
		ComplianceStatus: "compliant",
	}, nil)

	var rollbackCalled atomic.Int32
	m := NewComplianceMonitor(fi, func() error {
		rollbackCalled.Add(1)
		return nil
	})

	// Start first monitor with long window.
	m.Start(50, 10*time.Minute)

	// Start second monitor — should cancel the first.
	m.Start(50, 300*time.Millisecond)

	// Wait for second window to elapse.
	time.Sleep(600 * time.Millisecond)

	// Neither should trigger rollback (fleet is 100% compliant).
	if rollbackCalled.Load() != 0 {
		t.Fatal("rollback should not be called for healthy fleet")
	}
}

func TestComplianceMonitorEmptyFleet(t *testing.T) {
	fi := fleet.NewFleetIndex()

	var rollbackCalled atomic.Int32
	m := NewComplianceMonitor(fi, func() error {
		rollbackCalled.Add(1)
		return nil
	})

	// Empty fleet — should not trigger rollback.
	m.Start(90, 500*time.Millisecond)

	time.Sleep(800 * time.Millisecond)

	if rollbackCalled.Load() != 0 {
		t.Fatal("rollback should not be called for empty fleet")
	}
}
