// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReloadSerializesOverlappingLoads holds one reload open after it has
// read the tree, publishes a newer tree, and starts a second reload. The
// newer tree must be what ends up live: an older read finishing last may
// not overwrite it.
func TestReloadSerializesOverlappingLoads(t *testing.T) {
	dir := setupDir(t)
	envoys := filepath.Join(dir, "default", "envoys.vgo")
	writeFile(t, envoys, "envoys:\n  - match: \"web1\"\n    vars:\n      v: old\n")

	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	testAfterReadHook = func() {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	defer func() { testAfterReadHook = nil }()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = store.Reload() }()
	<-entered

	writeFile(t, envoys, "envoys:\n  - match: \"web1\"\n    vars:\n      v: new\n")
	bDone := make(chan struct{})
	go func() { _ = store.Reload(); close(bDone) }()
	select {
	case <-bDone:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	<-bDone

	cfg := store.Get("web1")
	if cfg == nil {
		t.Fatal("web1 has no config")
	}
	if got := cfg.Vars["v"]; got != "new" {
		t.Fatalf("live var v = %v, want new (an older reload overwrote a newer tree)", got)
	}
}
