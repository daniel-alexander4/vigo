// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"sync"
	"testing"

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/swarm"
)

// TestSwarmConfig_NoRaceUnderReload exercises the swarmCfg reload race head-on:
// a config-reload writer (SetSwarmConfig) concurrent with the per-request gating
// readers (swarmConfig / buildSwarmRuntimeLimits). With swarmCfg as an
// atomic.Pointer this is race-clean; run under -race to prove it.
func TestSwarmConfig_NoRaceUnderReload(t *testing.T) {
	s := &AgentService{}
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() { // reload writer
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			var sc config.SwarmSection
			sc.Filecast.MaxSizeBytes = int64(i)
			s.SetSwarmConfig(sc)
		}
		close(stop)
	}()

	for r := 0; r < 4; r++ { // concurrent gating readers
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = s.swarmConfig()
					_ = s.buildSwarmRuntimeLimits()
				}
			}
		}()
	}
	wg.Wait()
}

// TestCollectManifestEntries_CachesByVersion verifies the Lever-D manifest cache:
// the built proto slice is reused (same proto pointers) while ManifestStore
// .Version() is unchanged, and rebuilt (fresh pointers) only when a mutation
// bumps the version.
func TestCollectManifestEntries_CachesByVersion(t *testing.T) {
	store := swarm.NewManifestStore()
	s := &AgentService{manifestStore: store}

	// Empty manifest → nil, cached at version 0.
	if got := s.collectManifestEntries(); got != nil {
		t.Fatalf("empty manifest: got %v, want nil", got)
	}

	// First real entry bumps the version → rebuild, one entry.
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.RecordSeed(sha, "boot.iso", "*", "7d", "server", 1024)
	r1 := s.collectManifestEntries()
	if len(r1) != 1 {
		t.Fatalf("after RecordSeed: got %d entries, want 1", len(r1))
	}

	// Same version → cache hit: identical proto pointers, not a rebuild.
	r2 := s.collectManifestEntries()
	if len(r2) != 1 || r2[0] != r1[0] {
		t.Fatalf("cache hit should return the same cached slice (same proto pointer)")
	}

	// A second seed bumps the version → rebuild: fresh proto pointers.
	sha2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store.RecordSeed(sha2, "img.tar", "*", "7d", "server", 2048)
	r3 := s.collectManifestEntries()
	if len(r3) != 2 {
		t.Fatalf("after second RecordSeed: got %d entries, want 2", len(r3))
	}
	for _, e := range r3 {
		if e == r1[0] {
			t.Fatalf("version bump should rebuild a fresh slice, not reuse the old proto pointer")
		}
	}
}

// TestBuildSwarmRuntimeLimits_CachedOnSetSwarmConfig verifies Lever-D's
// swarm-limits cache: the proto is built by SetSwarmConfig (not per call), is
// nil before any config / for an empty config, and reflects the latest config.
func TestBuildSwarmRuntimeLimits_CachedOnSetSwarmConfig(t *testing.T) {
	s := &AgentService{}

	// Nothing set yet → nil.
	if got := s.buildSwarmRuntimeLimits(); got != nil {
		t.Fatalf("before SetSwarmConfig: got %v, want nil", got)
	}

	// Empty config → nil (the all-zero guard).
	s.SetSwarmConfig(config.SwarmSection{})
	if got := s.buildSwarmRuntimeLimits(); got != nil {
		t.Fatalf("empty config: got %v, want nil", got)
	}

	// Non-empty config → populated proto, returned by the cached getter.
	var sc config.SwarmSection
	sc.Filecast.MaxSizeBytes = 4096
	s.SetSwarmConfig(sc)
	got := s.buildSwarmRuntimeLimits()
	if got == nil || got.FilecastMaxSizeBytes != 4096 {
		t.Fatalf("non-empty config: got %v, want FilecastMaxSizeBytes=4096", got)
	}

	// A later reload with different config is reflected (rebuilt on SetSwarmConfig).
	sc.Filecast.MaxSizeBytes = 8192
	s.SetSwarmConfig(sc)
	if got := s.buildSwarmRuntimeLimits(); got == nil || got.FilecastMaxSizeBytes != 8192 {
		t.Fatalf("after reload: got %v, want FilecastMaxSizeBytes=8192", got)
	}
}
