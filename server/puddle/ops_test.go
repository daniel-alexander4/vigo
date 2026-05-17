// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package puddle

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const founderPubkeyExample = "ed25519:000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestEnsureOpsPuddle_FirstFounding(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	ops, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample)
	if err != nil {
		t.Fatalf("EnsureOpsPuddle: %v", err)
	}
	if ops.FriendlyName != "alexander4-ops" {
		t.Errorf("FriendlyName = %q", ops.FriendlyName)
	}
	if len(ops.Members) != 1 || ops.Members[0].Pubkey != founderPubkeyExample {
		t.Errorf("Members = %+v, expected single founder", ops.Members)
	}
	if ops.Members[0].JoinedAt.IsZero() {
		t.Error("JoinedAt should be set")
	}
	if !strings.HasPrefix(ops.PublicKeyText, "ed25519:") {
		t.Errorf("PublicKeyText %q missing prefix", ops.PublicKeyText)
	}

	// Sign-then-verify on the ops-puddle key proves the wrap/unwrap chain.
	msg := []byte("ops-puddle slice 2a round-trip")
	sig := ed25519.Sign(ops.SigningKey, msg)
	if !ed25519.Verify(ops.PublicKey, msg, sig) {
		t.Fatal("ops-puddle signature did not verify against its pubkey")
	}
}

func TestEnsureOpsPuddle_SecondCallLoadsSame(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	first, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := EnsureOpsPuddle(d, dir, "alexander4-ops", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !bytes.Equal(first.PublicKey, second.PublicKey) {
		t.Fatal("ops-puddle pubkey changed across loads — identity is not stable")
	}
	if !bytes.Equal(first.SigningKey, second.SigningKey) {
		t.Fatal("ops-puddle signing key changed across loads")
	}
}

func TestEnsureOpsPuddle_NameMismatchRefused(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)

	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("initial: %v", err)
	}
	_, err := EnsureOpsPuddle(d, dir, "different-name", "")
	if err == nil {
		t.Fatal("renaming an existing ops-puddle should refuse")
	}
	if !strings.Contains(err.Error(), "friendly name mismatch") {
		t.Errorf("error %q should mention friendly name mismatch", err.Error())
	}
}

func TestEnsureOpsPuddle_FirstFoundingRequiresFounderPubkey(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	_, err := EnsureOpsPuddle(d, dir, "alexander4-ops", "")
	if err == nil {
		t.Fatal("first founding without founderPubkey should fail")
	}
}

func TestEnsureOpsPuddle_FounderPubkeyFormat(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	_, err := EnsureOpsPuddle(d, dir, "alexander4-ops", "raw-hex-not-prefixed")
	if err == nil {
		t.Fatal("non-ed25519: pubkey should fail")
	}
}

func TestEnsureOpsPuddle_DistinctWrapPurposeBindsToOpsHKDF(t *testing.T) {
	// Founding an ops-puddle and re-loading it must use OpsHKDFPurpose,
	// not HKDFPurpose. If the loader confused the two, unwrap would fail
	// because the derived wrap key would differ. The pass = the test
	// reaches loadOpsPuddle and succeeds.
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := LoadOpsPuddle(d, dir); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestEnsureOpsPuddle_FilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}

	wantModes := map[string]os.FileMode{
		WrappedPath(dir):       0o600,
		SaltPath(dir):          0o600,
		PubkeyPath(dir):        0o644,
		friendlyNamePath(dir):  0o600,
		membersPath(dir):       0o600,
	}
	for path, want := range wantModes {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %v, want %v", path, got, want)
		}
	}

	// Friendly name file is a single line.
	b, _ := os.ReadFile(friendlyNamePath(dir))
	if string(b) != "alexander4-ops\n" {
		t.Errorf("friendly name file = %q, want %q", b, "alexander4-ops\n")
	}
}

func TestValidateFriendlyName(t *testing.T) {
	// 64-char non-hex name to exercise the upper-length-bound path without
	// tripping the "looks like a project_id" guard.
	max := strings.Repeat("z", 64)
	good := []string{"alexander4-ops", "ops", "a", "ops.staging", "ops_v2", max}
	for _, n := range good {
		if err := ValidateFriendlyName(n); err != nil {
			t.Errorf("ValidateFriendlyName(%q) rejected: %v", n, err)
		}
	}
	bad := []string{
		"",                       // empty
		strings.Repeat("a", 65),  // too long
		".hidden",                // leading dot
		"has space",              // space
		"has/slash",              // slash
		"has:colon",              // colon
		strings.Repeat("a", 32) + strings.Repeat("b", 32), // 64 hex-like chars
	}
	for _, n := range bad {
		if err := ValidateFriendlyName(n); err == nil {
			t.Errorf("ValidateFriendlyName(%q) accepted, should fail", n)
		}
	}
}

// ---------- Slice 2b: pair flow ----------

const joiningPubkeyExample = "ed25519:202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"

func TestGeneratePairCode_UniqueAndDecodable(t *testing.T) {
	a, err := GeneratePairCode()
	if err != nil {
		t.Fatalf("GeneratePairCode: %v", err)
	}
	b, err := GeneratePairCode()
	if err != nil {
		t.Fatalf("GeneratePairCode: %v", err)
	}
	if a == b {
		t.Error("two pair codes were identical — RNG is broken")
	}
	if a == "" || b == "" {
		t.Error("pair code must be non-empty")
	}
	// HashPairCode is a deterministic function of the plaintext.
	if HashPairCode(a) == HashPairCode(b) {
		t.Error("two different pair codes hashed to the same value")
	}
}

func TestWriteLoadPendingPair_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &PendingPair{
		CodeHash:  HashPairCode("test-code"),
		ExpiresAt: time.Now().UTC().Add(PairCodeTTL).Truncate(time.Second),
		Used:      false,
	}
	if err := WritePendingPair(dir, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadPendingPair(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil after write")
	}
	if got.CodeHash != want.CodeHash {
		t.Errorf("CodeHash mismatch: got %q, want %q", got.CodeHash, want.CodeHash)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt mismatch: got %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
	if got.Used != want.Used {
		t.Errorf("Used mismatch: got %v, want %v", got.Used, want.Used)
	}
}

func TestLoadPendingPair_MissingReturnsNil(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadPendingPair(dir)
	if err != nil {
		t.Fatalf("expected nil err for missing file, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil pending pair, got %+v", got)
	}
}

func TestMarkPendingPairUsed_Idempotent(t *testing.T) {
	dir := t.TempDir()
	p := &PendingPair{
		CodeHash:  HashPairCode("x"),
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := WritePendingPair(dir, p); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := MarkPendingPairUsed(dir); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	if err := MarkPendingPairUsed(dir); err != nil {
		t.Fatalf("second mark (should be idempotent): %v", err)
	}
	got, _ := LoadPendingPair(dir)
	if got == nil || !got.Used {
		t.Errorf("Used should be true after mark, got %+v", got)
	}
}

func TestAppendMember_AppendsAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}

	got, err := AppendMember(dir, joiningPubkeyExample)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("after first append, members = %d, want 2", len(got))
	}
	if got[1].Pubkey != joiningPubkeyExample {
		t.Errorf("appended pubkey = %q, want %q", got[1].Pubkey, joiningPubkeyExample)
	}

	// Idempotent: same pubkey again returns same list.
	got2, err := AppendMember(dir, joiningPubkeyExample)
	if err != nil {
		t.Fatalf("idempotent append: %v", err)
	}
	if len(got2) != 2 {
		t.Errorf("idempotent append changed count to %d", len(got2))
	}
}

func TestAppendMember_RefusesMalformedPubkey(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := AppendMember(dir, "not-an-ed25519-pubkey"); err == nil {
		t.Fatal("malformed pubkey should be refused")
	}
}

func TestIngestOpsPuddle_RoundTrip(t *testing.T) {
	// Simulate the existing-member side: found an ops-puddle, then
	// extract the seed and ingest it into a SECOND dir under a
	// DIFFERENT deriver — exactly what happens between two
	// vigosrvs during pair. After ingest both should hold the same
	// signing key, friendly name, and member set.
	existingDir := t.TempDir()
	joiningDir := t.TempDir()
	dExisting := newDeriver(t)
	dJoining := newDeriver(t) // independent salt → independent wrap key

	existing, err := EnsureOpsPuddle(dExisting, existingDir, "alexander4-ops", founderPubkeyExample)
	if err != nil {
		t.Fatalf("init existing: %v", err)
	}

	// Existing-member appends joining pubkey to its own list (what
	// the pair-claim handler does before responding).
	members, err := AppendMember(existingDir, joiningPubkeyExample)
	if err != nil {
		t.Fatalf("append on existing: %v", err)
	}

	seed := existing.SigningKey.Seed()
	joining, err := IngestOpsPuddle(dJoining, joiningDir, existing.FriendlyName, seed, members)
	if err != nil {
		t.Fatalf("ingest on joining: %v", err)
	}

	if !bytes.Equal(joining.PublicKey, existing.PublicKey) {
		t.Fatal("pubkeys differ after ingest — pair flow broke key identity")
	}
	if !bytes.Equal(joining.SigningKey, existing.SigningKey) {
		t.Fatal("signing keys differ after ingest")
	}
	if joining.FriendlyName != existing.FriendlyName {
		t.Errorf("friendly name diverged: %q vs %q", joining.FriendlyName, existing.FriendlyName)
	}
	if len(joining.Members) != 2 {
		t.Errorf("members on joining = %d, want 2", len(joining.Members))
	}

	// Sanity: a re-load on joining (a fresh boot of the joining
	// vigosrv) gives the same identity back.
	reloaded, err := LoadOpsPuddle(dJoining, joiningDir)
	if err != nil {
		t.Fatalf("reload on joining: %v", err)
	}
	if !bytes.Equal(reloaded.PublicKey, existing.PublicKey) {
		t.Fatal("pubkey changed across reload — joining-side wrap is unstable")
	}
}

func TestIngestOpsPuddle_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	if _, err := EnsureOpsPuddle(d, dir, "alexander4-ops", founderPubkeyExample); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Try to ingest into the same dir — should refuse to clobber.
	seed := bytes.Repeat([]byte{0xab}, 32)
	members := []OpsMember{{Pubkey: founderPubkeyExample, JoinedAt: time.Now().UTC()}}
	_, err := IngestOpsPuddle(d, dir, "alexander4-ops", seed, members)
	if err == nil {
		t.Fatal("ingest should refuse to overwrite an existing ops-puddle")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("error %q should mention overwrite refusal", err.Error())
	}
}

func TestIngestOpsPuddle_RejectsBadSeedLen(t *testing.T) {
	dir := t.TempDir()
	d := newDeriver(t)
	short := []byte{0x01, 0x02, 0x03}
	_, err := IngestOpsPuddle(d, dir, "alexander4-ops", short, nil)
	if err == nil {
		t.Fatal("short seed should be refused")
	}
}

func TestPendingPairFile_PathConvention(t *testing.T) {
	// Defense: catch accidental rename of PendingPairFile that
	// would orphan the pair record on the existing-member.
	dir := t.TempDir()
	want := filepath.Join(dir, "pending-pair")
	if PendingPairFile != "pending-pair" {
		t.Errorf("PendingPairFile = %q, want pending-pair", PendingPairFile)
	}
	if err := WritePendingPair(dir, &PendingPair{CodeHash: HashPairCode("x"), ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected pending-pair file at %s: %v", want, err)
	}

	// Mode is 0600 (matches writePrivateFile contract).
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("pending-pair mode = %v, want 0600", got)
	}
}

func ed25519keyBytes(seed byte) ed25519.PublicKey {
	// Tiny helper to satisfy compiler if we use ed25519 indirectly;
	// kept to mirror existing test-helper style.
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	return priv.Public().(ed25519.PublicKey)
}

var _ = ed25519keyBytes // silence unused if not currently called
