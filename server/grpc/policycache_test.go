// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"sync"
	"testing"

	"github.com/hobnob/hobnob/server/config"
)

func makeTestConfig() *config.ThrallConfig {
	nginx := config.ModuleConfig{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "server {}"}},
			{Name: "svc", Type: "service", Attributes: map[string]any{"service": "nginx", "state": "running"}},
		},
	}
	nginx.ContentHash = config.HashModuleContent(nginx)

	postgres := config.ModuleConfig{
		Name: "postgres",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "listen = *"}},
		},
	}
	postgres.ContentHash = config.HashModuleContent(postgres)

	return &config.ThrallConfig{
		MatchPattern: "*.web.example.com",
		Environment:  "production",
		Roles:        []string{"webserver"},
		Modules:      []config.ModuleConfig{nginx, postgres},
		Vars:         map[string]any{"port": 8080},
		VarOrder:     []string{"port"},
	}
}

func TestPolicyCache_GetOrBuild_CachesBundle(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()

	b1, err := pc.GetOrBuild("*.web.example.com", cfg, 1)
	if err != nil {
		t.Fatalf("GetOrBuild: %v", err)
	}
	if b1 == nil {
		t.Fatal("expected non-nil bundle")
	}
	if len(b1.Modules) != 2 {
		t.Fatalf("modules = %d, want 2", len(b1.Modules))
	}

	// Second call should return same pointer (cached).
	b2, err := pc.GetOrBuild("*.web.example.com", cfg, 1)
	if err != nil {
		t.Fatalf("GetOrBuild second: %v", err)
	}
	if b1 != b2 {
		t.Error("expected same pointer on cache hit")
	}
}

func TestPolicyCache_VersionChange_Invalidates(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()

	b1, _ := pc.GetOrBuild("*.web.example.com", cfg, 1)

	// Bump version — should invalidate.
	b2, _ := pc.GetOrBuild("*.web.example.com", cfg, 2)
	if b1 == b2 {
		t.Error("version change should produce new bundle")
	}
}

func TestPolicyCache_DifferentPatterns(t *testing.T) {
	pc := newPolicyCache()
	cfg1 := makeTestConfig()
	cfg2 := &config.ThrallConfig{
		MatchPattern: "*.db.example.com",
		Modules: []config.ModuleConfig{
			{Name: "pg", Resources: []config.ResourceConfig{{Name: "conf", Type: "file"}}},
		},
	}

	b1, _ := pc.GetOrBuild("*.web.example.com", cfg1, 1)
	b2, _ := pc.GetOrBuild("*.db.example.com", cfg2, 1)

	if b1 == b2 {
		t.Error("different patterns should produce different bundles")
	}
}

func TestCustomizeStubs_NoHashes_SharesModules(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()

	base, _ := pc.GetOrBuild("*.web.example.com", cfg, 1)
	result := customizeStubs(base, nil)

	// Should share the modules slice.
	if len(result.Modules) != 2 {
		t.Fatalf("modules = %d, want 2", len(result.Modules))
	}
	for i := range result.Modules {
		if result.Modules[i].Stub {
			t.Errorf("module %d should not be a stub", i)
		}
	}
	// New bundle, safe for mutation.
	if result == base {
		t.Error("should return a new bundle even with no hashes")
	}
}

func TestCustomizeStubs_MatchingHash_CreatesStub(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()

	base, _ := pc.GetOrBuild("*.web.example.com", cfg, 1)

	// Agent has nginx cached.
	hashes := map[string]string{
		"nginx": base.Modules[0].ContentHash,
	}
	result := customizeStubs(base, hashes)

	// nginx should be a stub.
	if !result.Modules[0].Stub {
		t.Error("nginx should be a stub")
	}
	if result.Modules[0].Resources != nil {
		t.Error("stub should have nil resources")
	}
	if result.Modules[0].ContentHash == "" {
		t.Error("stub should preserve content hash")
	}

	// postgres should be full.
	if result.Modules[1].Stub {
		t.Error("postgres should not be a stub")
	}
	if len(result.Modules[1].Resources) == 0 {
		t.Error("postgres should have resources")
	}
}

func TestCustomizeStubs_MismatchedHash_SendsFull(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()

	base, _ := pc.GetOrBuild("*.web.example.com", cfg, 1)

	hashes := map[string]string{
		"nginx": "old-stale-hash",
	}
	result := customizeStubs(base, hashes)

	if result.Modules[0].Stub {
		t.Error("mismatched hash should not produce a stub")
	}
}

func TestCustomizeStubs_DoesNotMutateBase(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()

	base, _ := pc.GetOrBuild("*.web.example.com", cfg, 1)
	originalHash := base.Modules[0].ContentHash
	originalResCount := len(base.Modules[0].Resources)

	// Customize with stubs.
	hashes := map[string]string{"nginx": base.Modules[0].ContentHash}
	_ = customizeStubs(base, hashes)

	// Base should be unchanged.
	if base.Modules[0].ContentHash != originalHash {
		t.Error("base module hash was mutated")
	}
	if len(base.Modules[0].Resources) != originalResCount {
		t.Error("base module resources were mutated")
	}
	if base.Modules[0].Stub {
		t.Error("base module stub flag was mutated")
	}
}

func BenchmarkPolicyCache_Hit(b *testing.B) {
	pc := newPolicyCache()
	cfg := makeTestConfig()
	pc.GetOrBuild("*.web.example.com", cfg, 1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc.GetOrBuild("*.web.example.com", cfg, 1)
	}
}

func BenchmarkCustomizeStubs(b *testing.B) {
	pc := newPolicyCache()
	cfg := makeTestConfig()
	base, _ := pc.GetOrBuild("*.web.example.com", cfg, 1)
	hashes := map[string]string{"nginx": base.Modules[0].ContentHash}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		customizeStubs(base, hashes)
	}
}

func TestPolicyCache_ConcurrentVersionChange(t *testing.T) {
	pc := newPolicyCache()
	cfg := makeTestConfig()
	const goroutines = 50

	var wg sync.WaitGroup

	// Phase 1: Concurrent hits on version 1.
	pc.GetOrBuild("*.web.example.com", cfg, 1)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := pc.GetOrBuild("*.web.example.com", cfg, 1)
			if err != nil {
				t.Errorf("GetOrBuild v1: %v", err)
			}
			if b == nil {
				t.Error("nil bundle from v1")
			}
		}()
	}
	wg.Wait()

	// Phase 2: Concurrent version bump.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(v uint64) {
			defer wg.Done()
			b, err := pc.GetOrBuild("*.web.example.com", cfg, v)
			if err != nil {
				t.Errorf("GetOrBuild v%d: %v", v, err)
			}
			if b == nil {
				t.Errorf("nil bundle from v%d", v)
			}
		}(uint64(2 + i%2)) // alternates between v2 and v3
	}
	wg.Wait()
}
