package checkin

import (
	"testing"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

func TestModuleCache_EmptyHashes(t *testing.T) {
	cache := NewModuleCache()
	hashes := cache.Hashes()
	if hashes != nil {
		t.Errorf("empty cache hashes = %v, want nil", hashes)
	}
}

func TestModuleCache_UpdateAndGet(t *testing.T) {
	cache := NewModuleCache()

	modules := []*pb.PolicyModule{
		{
			Name:        "nginx",
			ContentHash: "abc123",
			Resources: []*pb.PolicyResource{
				{Name: "conf", Type: "file"},
			},
		},
		{
			Name:        "postgres",
			ContentHash: "def456",
			Resources: []*pb.PolicyResource{
				{Name: "pg-conf", Type: "file"},
			},
		},
	}

	cache.Update(modules)

	// Hashes should be populated.
	hashes := cache.Hashes()
	if len(hashes) != 2 {
		t.Fatalf("hashes count = %d, want 2", len(hashes))
	}
	if hashes["nginx"] != "abc123" {
		t.Errorf("nginx hash = %q, want %q", hashes["nginx"], "abc123")
	}
	if hashes["postgres"] != "def456" {
		t.Errorf("postgres hash = %q, want %q", hashes["postgres"], "def456")
	}

	// Get should return cached modules.
	got := cache.Get("nginx", "abc123")
	if got == nil {
		t.Fatal("Get(nginx, abc123) returned nil")
	}
	if got.Name != "nginx" {
		t.Errorf("got.Name = %q, want %q", got.Name, "nginx")
	}
	if len(got.Resources) != 1 {
		t.Errorf("got.Resources = %d, want 1", len(got.Resources))
	}
}

func TestModuleCache_GetWrongHash(t *testing.T) {
	cache := NewModuleCache()
	cache.Update([]*pb.PolicyModule{
		{Name: "nginx", ContentHash: "abc123"},
	})

	got := cache.Get("nginx", "wrong-hash")
	if got != nil {
		t.Error("Get with wrong hash should return nil")
	}
}

func TestModuleCache_GetMissing(t *testing.T) {
	cache := NewModuleCache()
	got := cache.Get("nonexistent", "any")
	if got != nil {
		t.Error("Get for missing module should return nil")
	}
}

func TestModuleCache_EvictsRemovedModules(t *testing.T) {
	cache := NewModuleCache()

	// Initially two modules.
	cache.Update([]*pb.PolicyModule{
		{Name: "nginx", ContentHash: "abc"},
		{Name: "postgres", ContentHash: "def"},
	})

	if len(cache.Hashes()) != 2 {
		t.Fatalf("expected 2 cached modules")
	}

	// Thrall's role changed — only nginx now.
	cache.Update([]*pb.PolicyModule{
		{Name: "nginx", ContentHash: "abc"},
	})

	hashes := cache.Hashes()
	if len(hashes) != 1 {
		t.Fatalf("expected 1 cached module after eviction, got %d", len(hashes))
	}
	if _, ok := hashes["postgres"]; ok {
		t.Error("postgres should have been evicted")
	}
}

func TestModuleCache_SkipsEmptyHash(t *testing.T) {
	cache := NewModuleCache()
	cache.Update([]*pb.PolicyModule{
		{Name: "no-hash", ContentHash: ""},
	})

	hashes := cache.Hashes()
	if hashes != nil {
		t.Errorf("module with empty hash should not be cached, got %v", hashes)
	}
}

func TestMergeStubs_NoStubs(t *testing.T) {
	modules := []*pb.PolicyModule{
		{Name: "nginx", ContentHash: "abc", Resources: []*pb.PolicyResource{{Name: "r1"}}},
	}

	merged := MergeStubs(modules, nil)
	if len(merged) != 1 {
		t.Fatalf("merged count = %d, want 1", len(merged))
	}
	if merged[0].Name != "nginx" {
		t.Errorf("name = %q, want %q", merged[0].Name, "nginx")
	}
}

func TestMergeStubs_FillsFromCache(t *testing.T) {
	cache := NewModuleCache()
	cache.Update([]*pb.PolicyModule{
		{
			Name:        "nginx",
			ContentHash: "abc123",
			Resources: []*pb.PolicyResource{
				{Name: "conf", Type: "file", Attributes: map[string]string{"content": "cached"}},
			},
		},
	})

	// Server sends stub for nginx, full for postgres.
	modules := []*pb.PolicyModule{
		{Name: "nginx", ContentHash: "abc123", Stub: true},
		{
			Name:        "postgres",
			ContentHash: "def456",
			Resources: []*pb.PolicyResource{
				{Name: "pg-conf", Type: "file"},
			},
		},
	}

	merged := MergeStubs(modules, cache)
	if len(merged) != 2 {
		t.Fatalf("merged count = %d, want 2", len(merged))
	}

	// nginx should be filled from cache.
	nginx := merged[0]
	if nginx.Stub {
		t.Error("merged nginx should not be a stub")
	}
	if len(nginx.Resources) != 1 {
		t.Fatalf("nginx resources = %d, want 1", len(nginx.Resources))
	}
	if nginx.Resources[0].Attributes["content"] != "cached" {
		t.Errorf("nginx content = %q, want %q", nginx.Resources[0].Attributes["content"], "cached")
	}

	// postgres passed through as-is.
	pg := merged[1]
	if pg.Name != "postgres" {
		t.Errorf("postgres name = %q", pg.Name)
	}
}

func TestMergeStubs_CacheMiss(t *testing.T) {
	cache := NewModuleCache()
	// Cache is empty — no nginx cached.

	modules := []*pb.PolicyModule{
		{Name: "nginx", ContentHash: "abc123", Stub: true},
	}

	merged := MergeStubs(modules, cache)
	if len(merged) != 1 {
		t.Fatalf("merged count = %d, want 1", len(merged))
	}

	// Stub passes through (no resources, harmless).
	if !merged[0].Stub {
		t.Error("unresolved stub should remain a stub")
	}
}

func TestDeltaTransfer_EndToEnd(t *testing.T) {
	// Simulate a full delta transfer cycle.
	cache := NewModuleCache()

	// Cycle 1: Full transfer (no cached hashes).
	fullModules := []*pb.PolicyModule{
		{
			Name:        "nginx",
			ContentHash: "hash-nginx-v1",
			Resources:   []*pb.PolicyResource{{Name: "conf", Type: "file"}},
		},
		{
			Name:        "postgres",
			ContentHash: "hash-pg-v1",
			Resources:   []*pb.PolicyResource{{Name: "pg-conf", Type: "file"}},
		},
	}
	merged := MergeStubs(fullModules, cache)
	cache.Update(merged)

	// Verify cache is populated.
	hashes := cache.Hashes()
	if len(hashes) != 2 {
		t.Fatalf("after cycle 1: cache size = %d, want 2", len(hashes))
	}

	// Cycle 2: Only postgres changed. Server sends nginx as stub.
	deltaModules := []*pb.PolicyModule{
		{Name: "nginx", ContentHash: "hash-nginx-v1", Stub: true},
		{
			Name:        "postgres",
			ContentHash: "hash-pg-v2",
			Resources:   []*pb.PolicyResource{{Name: "pg-conf-v2", Type: "file"}},
		},
	}
	merged = MergeStubs(deltaModules, cache)
	cache.Update(merged)

	// Verify merged result.
	if len(merged) != 2 {
		t.Fatalf("after cycle 2: merged count = %d, want 2", len(merged))
	}

	// nginx filled from cache with v1 content.
	if merged[0].Name != "nginx" || len(merged[0].Resources) != 1 {
		t.Errorf("nginx not restored from cache: resources=%d", len(merged[0].Resources))
	}

	// postgres has v2 content.
	if merged[1].Name != "postgres" || merged[1].Resources[0].Name != "pg-conf-v2" {
		t.Error("postgres should have v2 content")
	}

	// Cache updated with new postgres hash.
	hashes = cache.Hashes()
	if hashes["postgres"] != "hash-pg-v2" {
		t.Errorf("postgres hash = %q, want %q", hashes["postgres"], "hash-pg-v2")
	}
}
