// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// cleanupImmutableKeys removes the immutable flag set by generateAndSaveKeys
// so t.TempDir() can clean up.
func cleanupImmutableKeys(t *testing.T, dir string) {
	t.Helper()
	t.Cleanup(func() {
		removeImmutable(filepath.Join(dir, privateKeyFile))
		removeImmutable(filepath.Join(dir, publicKeyFile))
	})
}

func TestGenerateAndSaveKeys(t *testing.T) {
	dir := t.TempDir()
	cleanupImmutableKeys(t, dir)
	if err := generateAndSaveKeys(dir); err != nil {
		t.Fatalf("generateAndSaveKeys: %v", err)
	}

	// Check private key file.
	privPath := filepath.Join(dir, privateKeyFile)
	info, err := os.Stat(privPath)
	if err != nil {
		t.Fatalf("private key not created: %v", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		// We only check that group/other bits are clear; chattr may not work.
		t.Errorf("private key permissions = %o, want owner-only", info.Mode().Perm())
	}

	privHex, _ := os.ReadFile(privPath)
	if len(privHex) != 64 {
		t.Errorf("private key hex length = %d, want 64", len(privHex))
	}

	// Check public key file.
	pubPath := filepath.Join(dir, publicKeyFile)
	pubHex, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("public key not created: %v", err)
	}
	if len(pubHex) != 64 {
		t.Errorf("public key hex length = %d, want 64", len(pubHex))
	}
}

func TestGenerateKeysRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	cleanupImmutableKeys(t, dir)

	// First generation should succeed.
	if err := generateAndSaveKeys(dir); err != nil {
		t.Fatalf("first generate: %v", err)
	}

	// Save the original keyDir and swap in our temp dir.
	origKeyDir := keyDir
	keyDir = dir
	defer func() { keyDir = origKeyDir }()

	// Second generation via the command should fail.
	err := runKeysGenerate(nil, nil)
	if err == nil {
		t.Fatal("expected error on second generate, got nil")
	}
}

func TestContains(t *testing.T) {
	if !contains("foobar", "oob") {
		t.Error("contains(foobar, oob) should be true")
	}
	if contains("foo", "bar") {
		t.Error("contains(foo, bar) should be false")
	}
	if contains("", "a") {
		t.Error("contains(empty, a) should be false")
	}
	if !contains("a", "a") {
		t.Error("contains(a, a) should be true")
	}
}

func TestResolveRepoPath(t *testing.T) {
	origInject := injectRepoPath
	defer func() { injectRepoPath = origInject }()

	injectRepoPath = "/tmp/test-repo-path"
	got := resolveRepoPath()
	if got != "/tmp/test-repo-path" {
		t.Errorf("resolveRepoPath with inject = %q, want /tmp/test-repo-path", got)
	}
}
