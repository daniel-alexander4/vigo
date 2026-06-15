// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"testing"

	pb "git.alexander4.io/vigo/generated.code/proto/vigo"
	"git.alexander4.io/vigo/server/config"
)

func TestHashPolicyConfigcrate_Deterministic(t *testing.T) {
	mod := &pb.PolicyConfigcrate{
		Name:      "nginx",
		DependsOn: []string{"base"},
		When:      "is_web",
		Resources: []*pb.PolicyResource{
			{
				Name: "nginx-conf",
				Type: "file",
				Attributes: map[string]string{
					"target_path": "/etc/nginx/nginx.conf",
					"content":     "server { listen 80; }",
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

	hash1 := hashPolicyConfigcrate(mod)
	hash2 := hashPolicyConfigcrate(mod)

	if hash1 != hash2 {
		t.Errorf("hash not deterministic: %q != %q", hash1, hash2)
	}
	if len(hash1) != 64 {
		t.Errorf("hash length = %d, want 64 (SHA256 hex)", len(hash1))
	}
}

func TestHashPolicyConfigcrate_DifferentContent(t *testing.T) {
	mod1 := &pb.PolicyConfigcrate{
		Name: "nginx",
		Resources: []*pb.PolicyResource{
			{Name: "conf", Type: "file", Attributes: map[string]string{"content": "v1"}},
		},
	}
	mod2 := &pb.PolicyConfigcrate{
		Name: "nginx",
		Resources: []*pb.PolicyResource{
			{Name: "conf", Type: "file", Attributes: map[string]string{"content": "v2"}},
		},
	}

	hash1 := hashPolicyConfigcrate(mod1)
	hash2 := hashPolicyConfigcrate(mod2)

	if hash1 == hash2 {
		t.Error("different module content should produce different hashes")
	}
}

func TestHashPolicyConfigcrate_AttributeOrderIrrelevant(t *testing.T) {
	// Map iteration order is random in Go, but our hash sorts keys.
	mod := &pb.PolicyConfigcrate{
		Name: "test",
		Resources: []*pb.PolicyResource{
			{
				Name: "r1",
				Type: "file",
				Attributes: map[string]string{
					"zzz": "last",
					"aaa": "first",
					"mmm": "middle",
				},
			},
		},
	}

	// Run many times to verify determinism despite map iteration.
	first := hashPolicyConfigcrate(mod)
	for i := 0; i < 50; i++ {
		if h := hashPolicyConfigcrate(mod); h != first {
			t.Fatalf("hash changed on iteration %d: %q != %q", i, h, first)
		}
	}
}

func TestBuildPolicyBundle_NoCachedHashes_FullConfigcrates(t *testing.T) {
	mod := config.Configcrate{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "test"}},
		},
	}
	mod.ContentHash = config.HashConfigcrateContent(mod)

	cfg := &config.EnvoyConfig{
		MatchPattern: "*.web.example.com",
		Configcrates: []config.Configcrate{mod},
	}

	bundle, err := buildPolicyBundle(cfg, 1, nil, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	if len(bundle.Configcrates) != 1 {
		t.Fatalf("modules count = %d, want 1", len(bundle.Configcrates))
	}

	pbMod := bundle.Configcrates[0]
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
	nginxMod := config.Configcrate{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "test"}},
		},
	}
	nginxMod.ContentHash = config.HashConfigcrateContent(nginxMod)

	pgMod := config.Configcrate{
		Name: "postgres",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "pg"}},
		},
	}
	pgMod.ContentHash = config.HashConfigcrateContent(pgMod)

	cfg := &config.EnvoyConfig{
		Configcrates: []config.Configcrate{nginxMod, pgMod},
	}

	// First build to get the hash.
	full, err := buildPolicyBundle(cfg, 1, nil, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	// Agent sends cached hash for nginx but not postgres.
	cachedHashes := map[string]string{
		"nginx": full.Configcrates[0].ContentHash,
	}

	// Second build with cached hashes.
	delta, err := buildPolicyBundle(cfg, 2, cachedHashes, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle delta: %v", err)
	}

	if len(delta.Configcrates) != 2 {
		t.Fatalf("modules count = %d, want 2", len(delta.Configcrates))
	}

	// nginx should be a stub.
	nginx := delta.Configcrates[0]
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
	pg := delta.Configcrates[1]
	if pg.Stub {
		t.Error("postgres should not be a stub (not in cached hashes)")
	}
	if len(pg.Resources) != 1 {
		t.Errorf("postgres resources = %d, want 1", len(pg.Resources))
	}
}

func TestInjectSecretTriggers(t *testing.T) {
	bundle := &pb.PolicyBundle{
		Configcrates: []*pb.PolicyConfigcrate{
			{
				Name: "grafana",
				Resources: []*pb.PolicyResource{
					{
						Name:       "grafana-ini",
						Type:       "file",
						Attributes: map[string]string{"content": "test"},
					},
					{
						Name:        "grafana-set-password",
						Type:        "exec",
						Attributes:  map[string]string{"command": "grafana-cli reset"},
						WatchSecret: []string{"vigo/grafana/admin-password"},
					},
					{
						Name:        "grafana-tls",
						Type:        "exec",
						Attributes:  map[string]string{"command": "update-tls"},
						WatchSecret: []string{"vigo/grafana/tls-cert"},
					},
				},
			},
		},
	}

	injectSecretTriggers(bundle, []string{"vigo/grafana/admin-password"})

	res := bundle.Configcrates[0].Resources

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

func TestInjectSecretTriggers_StubConfigcrateSkipped(t *testing.T) {
	bundle := &pb.PolicyBundle{
		Configcrates: []*pb.PolicyConfigcrate{
			{
				Name: "stubbed",
				Stub: true,
				Resources: []*pb.PolicyResource{
					{
						Name:        "res",
						Type:        "exec",
						WatchSecret: []string{"vigo/secret"},
					},
				},
			},
		},
	}

	injectSecretTriggers(bundle, []string{"vigo/secret"})

	// Stub module resources should not be modified.
	if bundle.Configcrates[0].Resources[0].Attributes != nil {
		t.Error("stub module resource should not have attributes modified")
	}
}

func TestBuildPolicyBundle_WatchSecretSerialized(t *testing.T) {
	mod := config.Configcrate{
		Name: "grafana",
		Resources: []config.ResourceConfig{
			{
				Name:        "set-pw",
				Type:        "exec",
				Attributes:  map[string]any{"command": "reset-pw"},
				WatchSecret: []string{"vigo/grafana/admin-password"},
			},
		},
	}
	mod.ContentHash = config.HashConfigcrateContent(mod)

	cfg := &config.EnvoyConfig{Configcrates: []config.Configcrate{mod}}

	bundle, err := buildPolicyBundle(cfg, 1, nil, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	res := bundle.Configcrates[0].Resources[0]
	if len(res.WatchSecret) != 1 || res.WatchSecret[0] != "vigo/grafana/admin-password" {
		t.Errorf("watch_secret = %v, want [vigo/grafana/admin-password]", res.WatchSecret)
	}
}

func TestBuildPolicyConfigcrate_SliceAttributeSerializedAsJSON(t *testing.T) {
	mod := config.Configcrate{
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

	pbMod := buildPolicyConfigcrate(mod, true, nil, nil)
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
	mod := config.Configcrate{
		Name: "nginx",
		Resources: []config.ResourceConfig{
			{Name: "conf", Type: "file", Attributes: map[string]any{"content": "updated"}},
		},
	}
	mod.ContentHash = config.HashConfigcrateContent(mod)

	cfg := &config.EnvoyConfig{
		Configcrates: []config.Configcrate{mod},
	}

	// Agent has an old hash.
	cachedHashes := map[string]string{
		"nginx": "stale-hash-from-previous-version",
	}

	bundle, err := buildPolicyBundle(cfg, 2, cachedHashes, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}

	pbMod := bundle.Configcrates[0]
	if pbMod.Stub {
		t.Error("module with mismatched hash should not be a stub")
	}
	if len(pbMod.Resources) != 1 {
		t.Errorf("resources = %d, want 1", len(pbMod.Resources))
	}
}
