// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// withTempPaths points the verifier and sentinel at a per-test tmpdir so
// concurrent tests don't fight over /srv/vigo state. Returns the dir.
func withTempPaths(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("VIGO_VERIFIER_PATH", filepath.Join(dir, "verifier.json"))
	t.Setenv("VIGO_SENTINEL_PATH", filepath.Join(dir, "sentinel"))
	return dir
}

func TestVerifierRoundTrip_CorrectPassphrase(t *testing.T) {
	withTempPaths(t)

	if err := writeVerifier("CorrectHorseBattery$1!"); err != nil {
		t.Fatalf("writeVerifier: %v", err)
	}
	if !verifierExists() {
		t.Fatal("verifierExists should be true after writeVerifier")
	}
	if err := verifyPassphrase("CorrectHorseBattery$1!"); err != nil {
		t.Fatalf("verifyPassphrase should succeed on the matching passphrase: %v", err)
	}
}

func TestVerifierRejectsWrongPassphrase(t *testing.T) {
	withTempPaths(t)

	if err := writeVerifier("CorrectHorseBattery$1!"); err != nil {
		t.Fatalf("writeVerifier: %v", err)
	}
	err := verifyPassphrase("WrongHorseBattery$1!")
	if err == nil {
		t.Fatal("verifyPassphrase should reject a mismatched passphrase")
	}
	if !strings.Contains(err.Error(), "incorrect") {
		t.Fatalf("expected 'incorrect passphrase' error, got: %v", err)
	}
}

func TestVerifierDifferentSaltsProduceDifferentHashes(t *testing.T) {
	// Same passphrase, two writes. The salt is random per write, so the
	// stored hash must differ — defence against on-disk inspection
	// distinguishing identical passphrases across rotations.
	withTempPaths(t)

	require := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	require(writeVerifier("SamePass$1!"), "first write")
	v1, err := readVerifier()
	require(err, "first read")

	require(writeVerifier("SamePass$1!"), "second write")
	v2, err := readVerifier()
	require(err, "second read")

	if v1.Salt == v2.Salt {
		t.Fatal("expected different salts across writes")
	}
	if v1.Hash == v2.Hash {
		t.Fatal("expected different hashes across writes (different salts ⇒ different hashes)")
	}
}

func TestSentinelFreshIsUnlocked(t *testing.T) {
	withTempPaths(t)

	if isUnlocked() {
		t.Fatal("isUnlocked should be false with no sentinel present")
	}
	if err := writeSentinel(time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("writeSentinel: %v", err)
	}
	if !isUnlocked() {
		t.Fatal("isUnlocked should be true within the TTL window")
	}
}

func TestSentinelExpiredIsLocked(t *testing.T) {
	withTempPaths(t)

	if err := writeSentinel(time.Now().Add(-1 * time.Second)); err != nil {
		t.Fatalf("writeSentinel: %v", err)
	}
	if isUnlocked() {
		t.Fatal("isUnlocked should be false past the expiry timestamp")
	}
}

func TestSentinelCorruptionIsLocked(t *testing.T) {
	dir := withTempPaths(t)

	// Write a garbage sentinel — should be treated as locked, not crash.
	if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte("not-a-timestamp"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if isUnlocked() {
		t.Fatal("isUnlocked should be false for an unparseable sentinel")
	}
}

func TestRequireUnlockedBumpsTTL(t *testing.T) {
	withTempPaths(t)

	if err := writeVerifier("Pass$1tap1e!"); err != nil {
		t.Fatalf("writeVerifier: %v", err)
	}

	// Set sentinel with very short expiry (10s from now) to detect bump.
	short := time.Now().Add(10 * time.Second)
	if err := writeSentinel(short); err != nil {
		t.Fatalf("writeSentinel: %v", err)
	}

	cmd := &cobra.Command{Use: "list"}
	if err := requireUnlocked(cmd, nil); err != nil {
		t.Fatalf("requireUnlocked should pass for a non-exempt verb when unlocked: %v", err)
	}

	got, err := readSentinelExpiry()
	if err != nil {
		t.Fatalf("readSentinelExpiry: %v", err)
	}
	// After bump, the expiry should be ~idleTTL away (much further than 10s).
	if !got.After(short.Add(time.Minute)) {
		t.Fatalf("expected sliding-window bump to push expiry beyond %s, got %s", short, got)
	}
}

func TestRequireUnlockedRefusesWhenLocked(t *testing.T) {
	withTempPaths(t)

	if err := writeVerifier("Pass$1tap1e!"); err != nil {
		t.Fatalf("writeVerifier: %v", err)
	}
	// No sentinel — vault locked.

	cmd := &cobra.Command{Use: "list"}
	err := requireUnlocked(cmd, nil)
	if err == nil {
		t.Fatal("requireUnlocked should refuse when locked")
	}
	if !strings.Contains(err.Error(), "vigocli secrets unlock") {
		t.Fatalf("error should point operator at `vigocli secrets unlock`, got: %v", err)
	}
}

func TestRequireUnlockedRefusesWhenVerifierMissing(t *testing.T) {
	withTempPaths(t)
	// Neither verifier nor sentinel exists.

	cmd := &cobra.Command{Use: "list"}
	err := requireUnlocked(cmd, nil)
	if err == nil {
		t.Fatal("requireUnlocked should refuse when vault is not initialized")
	}
	if !strings.Contains(err.Error(), "vigocli secrets init") {
		t.Fatalf("error should point operator at `vigocli secrets init`, got: %v", err)
	}
}

func TestRequireUnlockedAllowsExemptVerbs(t *testing.T) {
	withTempPaths(t)
	// Vault not initialized — even so, exempt verbs must work.

	for verb := range gateExemptVerbs {
		cmd := &cobra.Command{Use: verb}
		if err := requireUnlocked(cmd, nil); err != nil {
			t.Fatalf("requireUnlocked should let %q through pre-init: %v", verb, err)
		}
	}
}

func TestSentinelExpiryFormatRoundTrip(t *testing.T) {
	withTempPaths(t)

	want := time.Now().Add(15 * time.Minute).Truncate(time.Millisecond)
	if err := writeSentinel(want); err != nil {
		t.Fatalf("writeSentinel: %v", err)
	}
	got, err := readSentinelExpiry()
	if err != nil {
		t.Fatalf("readSentinelExpiry: %v", err)
	}
	if got.UnixMilli() != want.UnixMilli() {
		t.Fatalf("sentinel ms drift: want %d, got %d", want.UnixMilli(), got.UnixMilli())
	}
}

func TestSentinelEnvOverrideHonored(t *testing.T) {
	dir := withTempPaths(t)

	want := filepath.Join(dir, "sentinel")
	if got := sentinelPath(); got != want {
		t.Fatalf("sentinelPath should follow VIGO_SENTINEL_PATH: want %s got %s", want, got)
	}
}

func TestVerifierEnvOverrideHonored(t *testing.T) {
	dir := withTempPaths(t)

	want := filepath.Join(dir, "verifier.json")
	if got := verifierPath(); got != want {
		t.Fatalf("verifierPath should follow VIGO_VERIFIER_PATH: want %s got %s", want, got)
	}
}

// TestBumpSentinelExtends checks that a fresh bump from very-near-expiry
// pushes the expiry forward.
func TestBumpSentinelExtends(t *testing.T) {
	withTempPaths(t)

	near := time.Now().Add(2 * time.Second)
	if err := writeSentinel(near); err != nil {
		t.Fatalf("writeSentinel: %v", err)
	}
	bumpSentinel()
	got, err := readSentinelExpiry()
	if err != nil {
		t.Fatalf("readSentinelExpiry: %v", err)
	}
	if !got.After(near.Add(time.Hour)) {
		t.Fatalf("expected bumped expiry to be >1h past the original, got %s vs %s", got, near)
	}
}

// TestSentinelMillisecondsBoundary checks numeric parse robustness on a
// realistic value.
func TestSentinelMillisecondsBoundary(t *testing.T) {
	t.Parallel()
	// Just an isolated test that confirms strconv.ParseInt and UnixMilli
	// agree on epoch math at the boundary used by the sentinel format.
	s := "1737038400000"
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if got := time.UnixMilli(ms).UnixMilli(); got != ms {
		t.Fatalf("UnixMilli round-trip drift: %d vs %d", got, ms)
	}
}
