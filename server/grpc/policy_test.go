// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package grpc

import (
	"testing"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/config"
)

func TestHashPolicyModule_Deterministic(t *testing.T) {
	mod := &pb.PolicyModule{
		Name:      "nginx",
		DependsOn: []string{"base"},
		When:      "is_web",
		Resources: []*pb.PolicyResource{
			{
				Name: "nginx-conf",
				Type: "file",
				Attributes: map[string]string{
					"targetpath": "/etc/nginx/nginx.conf",
					"content":    "server { listen 80; }",
				},
				Notify: []string{"nginx-svc"},
			},
			{
				Name: "nginx-svc",
				Type: "service",
				Attributes: map[string]string{
					"service": "nginx",
					"state":   "running",
				},
			},
		},
	}

	hash1 := hashPolicyModule(mod)
	hash2 := hashPolicyModule(mod)

	if hash1 != hash2 {
		t.Errorf("hash not deterministic: %q != %q", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA256 hex)", len(hash1))
	}
}

func TestHashPolicyModule_DifferentContent(t *testing.T) {
	mod1 := &pb.PolicyModule{
		Name: "nginx",
		Resources: []*pb.PolicyResource{
			{Name: "conf", Type: "file", Attributes: map[string]string{"content": "v1"}},
		},
	}
	mod2 := &pb.PolicyModule{
		Name: "nginx",
		Resources: []*pb.PolicyResource{
			{Name: "conf", Type: "file", Attributes: map[string]string{"content": "v2"}},
		},
	}

	hash1 := hashPolicyModule(mod1)
	hash2 := hashPolicyModule(mod2)

	if hash1 == hash2 {
		t.Error("different module content should produce different hashes")
	}
}

func TestHashPolicyModule_AttributeOrderIrrelevant(t *testing.T) {
	// Map iteration order is random in Go, but our hash sorts keys.
	mod := &pb.PolicyModule{
		Name: "test",
		Resources: []*pb.PolicyResource{
			{
				Name: "r1",
				Type: "file",
				Attributes: map[string]string{
					"zzz":  "last",
					"aaa":  "first",
					"mmm":  "middle",
				},
			},
		},
	}

	// Run many times to verify determinism despite map iteration.
	first := hashPolicyModule(mod)
	for i := 0; i < 50; i++ {
		if h := hashPolicyModule(mod); h != first {
			t.Fatalf("hash changed on iteration %d: %q != %q", i, h, first)
		}
	}
}

func TestBuildPolicyBundle_NoCachedHashes_FullModules(t *testing.T) {
	mod := config.ModuleConfig{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "test"}},
		},
	}
	mod.ContentHash = config.HashModuleContent(mod)

	cfg := &config.ThrallConfig{
		MatchPattern: "*.web.example.com",
		Modules:      []config.ModuleConfig{mod},
	}

	bundle, err := buildPolicyBundle(cfg, 1, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	if len(bundle.Modules) != 1 {
		t.Fatalf("modules count = %d, want 1", len(bundle.Modules))
	}

	pbMod := bundle.Modules[0]
	if pbMod.ContentHash == "" {
		t.Error("content_hash should be set")
	}
	if pbMod.Stub {
		t.Error("stub should be false when no cached hashes")
	}
	if len(pbMod.Resources) != 1 {
		t.Errorf("resources count = %d, want 1", len(pbMod.Resources))
	}
}

func TestBuildPolicyBundle_CachedHashMatch_SendsStub(t *testing.T) {
	nginxMod := config.ModuleConfig{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "test"}},
		},
	}
	nginxMod.ContentHash = config.HashModuleContent(nginxMod)

	pgMod := config.ModuleConfig{
		Name: "postgres",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "pg"}},
		},
	}
	pgMod.ContentHash = config.HashModuleContent(pgMod)

	cfg := &config.ThrallConfig{
		Modules: []config.ModuleConfig{nginxMod, pgMod},
	}

	// First build to get the hash.
	full, err := buildPolicyBundle(cfg, 1, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	// Agent sends cached hash for nginx but not postgres.
	cachedHashes := map[string]string{
		"nginx": full.Modules[0].ContentHash,
	}

	// Second build with cached hashes.
	delta, err := buildPolicyBundle(cfg, 2, cachedHashes)
	if err != nil {
		t.Fatalf("buildPolicyBundle delta: %v", err)
	}

	if len(delta.Modules) != 2 {
		t.Fatalf("modules count = %d, want 2", len(delta.Modules))
	}

	// nginx should be a stub.
	nginx := delta.Modules[0]
	if !nginx.Stub {
		t.Error("nginx should be a stub (hash matched)")
	}
	if len(nginx.Resources) != 0 {
		t.Errorf("stub nginx should have 0 resources, got %d", len(nginx.Resources))
	}
	if nginx.ContentHash == "" {
		t.Error("stub should still have content_hash")
	}

	// postgres should be full.
	pg := delta.Modules[1]
	if pg.Stub {
		t.Error("postgres should not be a stub (not in cached hashes)")
	}
	if len(pg.Resources) != 1 {
		t.Errorf("postgres resources = %d, want 1", len(pg.Resources))
	}
}

func TestInjectSecretTriggers(t *testing.T) {
	bundle := &pb.PolicyBundle{
		Modules: []*pb.PolicyModule{
			{
				Name: "grafana",
				Resources: []*pb.PolicyResource{
					{
						Name:       "grafana-ini",
						Type:       "file",
						Attributes: map[string]string{"content": "test"},
					},
					{
						Name:             "grafana-set-password",
						Type:             "exec",
						Attributes:       map[string]string{"command": "grafana-cli reset"},
						SubscribesSecret: []string{"hobnob/grafana/admin-password"},
					},
					{
						Name:             "grafana-tls",
						Type:             "exec",
						Attributes:       map[string]string{"command": "update-tls"},
						SubscribesSecret: []string{"hobnob/grafana/tls-cert"},
					},
				},
			},
		},
	}

	injectSecretTriggers(bundle, []string{"hobnob/grafana/admin-password"})

	res := bundle.Modules[0].Resources

	// grafana-ini should be untouched.
	if res[0].Attributes["_secret_triggered"] != "" {
		t.Error("grafana-ini should not have _secret_triggered")
	}

	// grafana-set-password should be triggered.
	if res[1].Attributes["_secret_triggered"] != "true" {
		t.Error("grafana-set-password should have _secret_triggered=true")
	}

	// grafana-tls should NOT be triggered (different secret path).
	if res[2].Attributes["_secret_triggered"] != "" {
		t.Error("grafana-tls should not have _secret_triggered")
	}
}

func TestInjectSecretTriggers_StubModuleSkipped(t *testing.T) {
	bundle := &pb.PolicyBundle{
		Modules: []*pb.PolicyModule{
			{
				Name: "stubbed",
				Stub: true,
				Resources: []*pb.PolicyResource{
					{
						Name:             "res",
						Type:             "exec",
						SubscribesSecret: []string{"hobnob/secret"},
					},
				},
			},
		},
	}

	injectSecretTriggers(bundle, []string{"hobnob/secret"})

	// Stub module resources should not be modified.
	if bundle.Modules[0].Resources[0].Attributes != nil {
		t.Error("stub module resource should not have attributes modified")
	}
}

func TestBuildPolicyBundle_SubscribesSecretSerialized(t *testing.T) {
	mod := config.ModuleConfig{
		Name: "grafana",
		Resources: []config.ResourceConfig{
			{
				Name:             "set-pw",
				Type:             "exec",
				Attributes:       map[string]any{"command": "reset-pw"},
				SubscribesSecret: []string{"hobnob/grafana/admin-password"},
			},
		},
	}
	mod.ContentHash = config.HashModuleContent(mod)

	cfg := &config.ThrallConfig{Modules: []config.ModuleConfig{mod}}

	bundle, err := buildPolicyBundle(cfg, 1, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	res := bundle.Modules[0].Resources[0]
	if len(res.SubscribesSecret) != 1 || res.SubscribesSecret[0] != "hobnob/grafana/admin-password" {
		t.Errorf("subscribes_secret = %v, want [hobnob/grafana/admin-password]", res.SubscribesSecret)
	}
}

func TestBuildPolicyModule_SliceAttributeSerializedAsJSON(t *testing.T) {
	mod := config.ModuleConfig{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{
				Name: "conf",
				Type: "file",
				Attributes: map[string]any{
					"content":     "server {}",
					"stream_edit": []interface{}{"/usr/local/bin/strip-comments", "/usr/local/bin/envsubst"},
				},
			},
		},
	}

	pbMod := buildPolicyModule(mod)
	res := pbMod.Resources[0]

	got := res.Attributes["stream_edit"]
	want := `["/usr/local/bin/strip-comments","/usr/local/bin/envsubst"]`
	if got != want {
		t.Errorf("stream_edit attribute = %q, want %q", got, want)
	}

	// Scalar attributes should still work as before.
	if res.Attributes["content"] != "server {}" {
		t.Errorf("content = %q, want %q", res.Attributes["content"], "server {}")
	}
}

func TestBuildPolicyBundle_CachedHashMismatch_SendsFull(t *testing.T) {
	mod := config.ModuleConfig{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "updated"}},
		},
	}
	mod.ContentHash = config.HashModuleContent(mod)

	cfg := &config.ThrallConfig{
		Modules: []config.ModuleConfig{mod},
	}

	// Agent has an old hash.
	cachedHashes := map[string]string{
		"nginx": "stale-hash-from-previous-version",
	}

	bundle, err := buildPolicyBundle(cfg, 2, cachedHashes)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	pbMod := bundle.Modules[0]
	if pbMod.Stub {
		t.Error("module with mismatched hash should not be a stub")
	}
	if len(pbMod.Resources) != 1 {
		t.Errorf("resources = %d, want 1", len(pbMod.Resources))
	}
}
