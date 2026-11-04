// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package traits

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestScanDir_CollidingNamesSkipped: foo.sh and foo.py both map to trait
// "foo"; neither is advertised, so no agent is told one hash and served
// the other file.
func TestScanDir_CollidingNamesSkipped(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, dir, "foo.sh", "#!/bin/sh\necho sh")
	writeExec(t, dir, "foo.py", "#!/usr/bin/env python3\nprint('py')")
	writeExec(t, dir, "bar", "#!/bin/sh\necho bar")
	s := NewScanner(dir, 0)
	entries, err := s.ScanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "bar" {
		t.Fatalf("entries = %+v, want only bar", entries)
	}
	if _, err := s.ReadFile("foo"); err == nil {
		t.Fatal("ReadFile(foo) served a colliding trait")
	}
}

// TestScanDir_SymlinksSkipped: an executable-looking symlink is neither
// advertised nor served.
func TestScanDir_SymlinksSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("not a trait"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	s := NewScanner(dir, 0)
	entries, err := s.ScanDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none (symlink must be skipped)", entries)
	}

	// A file swapped for a symlink after the scan is refused at read time.
	writeExec(t, dir, "real", "#!/bin/sh\necho ok")
	if _, err := s.ScanDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "real")); err != nil {
		t.Fatal(err)
	}
	if data, err := s.ReadFile("real"); err == nil {
		t.Fatalf("ReadFile followed a swapped-in symlink and served %q", data)
	}
}

// TestScanDir_OneRescanOnExpiry: many callers hitting an expired cache
// trigger one rescan, not one each.
func TestScanDir_OneRescanOnExpiry(t *testing.T) {
	dir := t.TempDir()
	writeExec(t, dir, "a", "#!/bin/sh\necho a")
	var scans atomic.Int32
	testScanHook = func() {
		scans.Add(1)
		time.Sleep(50 * time.Millisecond) // hold the rescan open
	}
	defer func() { testScanHook = nil }()

	s := NewScanner(dir, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.ScanDir() }()
	}
	wg.Wait()
	if n := scans.Load(); n != 1 {
		t.Fatalf("%d rescans for 20 concurrent callers, want 1", n)
	}
}
