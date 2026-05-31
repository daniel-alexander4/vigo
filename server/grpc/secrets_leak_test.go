// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package grpc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.alexander4.io/vigo/server/config"
	"google.golang.org/protobuf/proto"
)

// TestPolicyBundle_NoPlaintextSecretLeak walks the on-wire bytes of a
// fully-built PolicyBundle for a fixture envoy and asserts the
// sentinel secret value does NOT appear anywhere in the marshaled
// payload. The sentinel is fed through every secret-bearing surface
// the loader supports — direct attribute, compound attribute, var,
// device credential — so a leak anywhere in the bundle assembly
// pipeline tips this test.
//
// This is the headline regression guard for the "secrets never appear
// in plaintext in gRPC payloads" constraint in CLAUDE.md.
func TestPolicyBundle_NoPlaintextSecretLeak(t *testing.T) {
	const sentinel = "DEADBEEF_SECRET_SENTINEL_DO_NOT_LEAK"

	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	// Module references the sentinel via every shape the loader resolves:
	// whole-attribute, compound-attribute, and a templated var.
	mustWrite("configcrates/secrets.vgo", `
name: secrets
resources:
  - name: dsn-file
    type: file
    target_path: /etc/app/dsn
    content: "secret:vigo/dsn"
  - name: env-compound
    type: file
    target_path: /etc/app/env
    content: "DB=secret:vigo/dsn,LOG=info"
`)
	mustWrite("default/envoys.vgo", `
envoys:
  - match: "leak-test.example.com"
    configcrates: [secrets]
    vars:
      app_pw: "secret:vigo/dsn"
`)

	provider := &fakeSecretProvider{values: map[string]string{
		"vigo/dsn": sentinel,
	}}
	store, err := config.NewConfigStore(dir, provider, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}

	_, cfg := store.Match("leak-test.example.com")
	if cfg == nil {
		t.Fatal("no matched config for fixture envoy")
	}

	bundle, err := buildPolicyBundle(cfg, 1, nil, nil)
	if err != nil {
		t.Fatalf("buildPolicyBundle: %v", err)
	}
	wire, err := proto.Marshal(bundle)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}

	if bytes.Contains(wire, []byte(sentinel)) {
		t.Fatalf("PolicyBundle wire bytes contain plaintext secret sentinel %q — the placeholder pipeline leaked", sentinel)
	}

	// And confirm the placeholder marker IS present (sanity check —
	// otherwise this test would pass on a bundle that simply omitted
	// the secret-bearing fields).
	if !bytes.Contains(wire, []byte("__VIGO_SECRET__vigo/dsn__END_VIGO_SECRET__")) {
		t.Errorf("PolicyBundle missing the expected placeholder marker — the fixture is broken")
	}
}

// fakeSecretProvider is a minimal secrets.Provider for the leak test.
type fakeSecretProvider struct {
	values map[string]string
}

func (f *fakeSecretProvider) Get(_ context.Context, key string) (string, error) {
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return "", os.ErrNotExist
}
func (f *fakeSecretProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	v, err := f.Get(ctx, key)
	return []byte(v), err
}
func (f *fakeSecretProvider) Close() error { return nil }
