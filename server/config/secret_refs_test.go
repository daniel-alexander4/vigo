// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestKnownSecretPlaintextNotInError: a known-secret var set to plaintext
// is refused, and the plaintext never appears in the error text or in the
// diagnostic block Reload logs.
func TestKnownSecretPlaintextNotInError(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "bad.example.com"
    vars:
      dsn: "postgres://admin:hunter2@db/app"
      nested:
        webhook_secret: "s3kr1t-nested"
`)
	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true)
	if err == nil {
		t.Fatal("expected a known-secret error, got nil")
	}
	msg := err.Error()
	for _, plain := range []string{"hunter2", "s3kr1t-nested"} {
		if strings.Contains(msg, plain) {
			t.Fatalf("error leaks plaintext %q:\n%s", plain, msg)
		}
	}
	if !strings.Contains(msg, `"dsn"`) || !strings.Contains(msg, `"webhook_secret"`) {
		t.Fatalf("error should name both known-secret fields (top level and nested):\n%s", msg)
	}
}

// TestNestedSecretRefsResolved: a secret: ref inside a list or map var or
// resource attribute is validated and replaced with a placeholder, and the
// detection side (SecretPaths) reports it.
func TestNestedSecretRefsResolved(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "configcrates", "app.vgo"), `
name: app
vars:
  creds:
    - "secret:vigo/app/one"
resources:
  - name: svc
    type: container
    container: svc
    image: nginx
    state: running
    env:
      - "PW=secret:vigo/app/two"
`)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web1"
    configcrates: [app]
    vars:
      db:
        conn: "secret:vigo/app/three"
`)
	store, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{
		"vigo/app/one": "v1", "vigo/app/two": "v2", "vigo/app/three": "v3",
	}}, true)
	if err != nil {
		t.Fatalf("NewConfigStore: %v", err)
	}
	_, cfg := store.Match("web1")
	if cfg == nil {
		t.Fatal("no config for web1")
	}
	db, _ := cfg.Vars["db"].(map[string]any)
	if got, _ := db["conn"].(string); got != formatSecretPlaceholder("vigo/app/three") {
		t.Errorf("nested map var = %q, want placeholder", got)
	}
	env, _ := cfg.Configcrates[0].Resources[0].Attributes["env"].([]any)
	if len(env) != 1 || env[0] != "PW="+formatSecretPlaceholder("vigo/app/two") {
		t.Errorf("list attribute = %#v, want placeholder", env)
	}
	paths := strings.Join(store.SecretPaths(), ",")
	for _, want := range []string{"vigo/app/one", "vigo/app/two"} {
		if !strings.Contains(paths, want) {
			t.Errorf("SecretPaths() = %s, missing nested ref %s", paths, want)
		}
	}
}

// TestNestedSecretRefUnresolvableFails: a missing secret nested in a list
// is a load error, not a literal shipped to the agent.
func TestNestedSecretRefUnresolvableFails(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "web1"
    vars:
      list: ["secret:vigo/missing"]
`)
	if _, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{}}, true); err == nil {
		t.Fatal("expected a load error for an unresolvable nested secret ref")
	}
}

// TestSecretKeyTraversalRejected: a key the provider would happily look up
// is still refused when it has a `..` or hidden component — the grammar is
// enforced before the provider is asked.
func TestSecretKeyTraversalRejected(t *testing.T) {
	for _, ref := range []string{"secret:vigo/../x", "secret:.master.key", "secret:Vigo/X"} {
		dir := setupDir(t)
		writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), "envoys:\n  - match: \"web1\"\n    vars:\n      v: \""+ref+"\"\n")
		key := strings.TrimPrefix(ref, "secret:")
		_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{key: "x"}}, true)
		if err == nil {
			t.Errorf("%s: expected a secret-key grammar error, got nil", ref)
		}
	}
}

// TestDeviceCredentialKeyValidated: the network-device credential goes
// through the same scanner and grammar as every other ref.
func TestDeviceCredentialKeyValidated(t *testing.T) {
	dir := setupDir(t)
	writeFile(t, filepath.Join(dir, "default", "envoys.vgo"), `
envoys:
  - match: "gw1"
  - match: "sw1"
    device_type: cisco_ios
    gateway: gw1
    credential: "secret:net/../sw1"
`)
	_, err := NewConfigStore(dir, &mockProvider{secrets: map[string]string{"net/../sw1": "x"}}, true)
	if err == nil {
		t.Fatal("expected a secret-key grammar error for the device credential")
	}
}
