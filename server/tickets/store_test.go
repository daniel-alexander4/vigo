// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package tickets

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// fakeDeriver mimics the slice of LocalProvider.DeriveKey we depend on
// without pulling the secrets package into the tickets test deps.
type fakeDeriver struct {
	master []byte
}

func (f *fakeDeriver) DeriveKey(purpose string, salt []byte, outLen int) ([]byte, error) {
	out := make([]byte, outLen)
	r := hkdf.New(sha256.New, f.master, salt, []byte(purpose))
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func newFakeDeriver(t *testing.T) *fakeDeriver {
	t.Helper()
	master := make([]byte, 32)
	for i := range master {
		master[i] = byte(i + 1) // deterministic but non-zero
	}
	return &fakeDeriver{master: master}
}

func TestEnsureStore_FirstBootGeneratesOneKey(t *testing.T) {
	dir := t.TempDir()
	store, err := EnsureStore(newFakeDeriver(t), dir)
	if err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}
	keys := store.Keys()
	if len(keys) != 1 {
		t.Fatalf("first-boot key count = %d, want 1", len(keys))
	}
	if isZero(keys[0][:]) {
		t.Fatal("active key is all zeros")
	}
}

func TestEnsureStore_RoundtripRecoversSameKey(t *testing.T) {
	dir := t.TempDir()
	deriver := newFakeDeriver(t)

	first, err := EnsureStore(deriver, dir)
	if err != nil {
		t.Fatalf("first EnsureStore: %v", err)
	}
	want := first.Keys()

	second, err := EnsureStore(deriver, dir)
	if err != nil {
		t.Fatalf("second EnsureStore: %v", err)
	}
	got := second.Keys()
	if len(got) != len(want) {
		t.Fatalf("key count after reload = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i][:], want[i][:]) {
			t.Fatalf("key %d differs after reload", i)
		}
	}
}

func TestRotate_DemotesActiveToGraceAndPersists(t *testing.T) {
	dir := t.TempDir()
	deriver := newFakeDeriver(t)

	store, err := EnsureStore(deriver, dir)
	if err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}
	before := store.Keys()[0]

	if err := store.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	after := store.Keys()
	if len(after) != 2 {
		t.Fatalf("post-rotate key count = %d, want 2", len(after))
	}
	if bytes.Equal(after[0][:], before[:]) {
		t.Fatal("active key did not change after rotate")
	}
	if !bytes.Equal(after[1][:], before[:]) {
		t.Fatal("previous active was not demoted to grace")
	}

	// Reload from disk: rotation must have persisted.
	reloaded, err := EnsureStore(deriver, dir)
	if err != nil {
		t.Fatalf("reload after rotate: %v", err)
	}
	got := reloaded.Keys()
	if len(got) != 2 {
		t.Fatalf("reloaded key count = %d, want 2", len(got))
	}
	for i := range got {
		if !bytes.Equal(got[i][:], after[i][:]) {
			t.Fatalf("reloaded key %d differs from in-memory", i)
		}
	}
}

func TestRotate_CapsAtTwoKeys(t *testing.T) {
	dir := t.TempDir()
	deriver := newFakeDeriver(t)
	store, err := EnsureStore(deriver, dir)
	if err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := store.Rotate(); err != nil {
			t.Fatalf("Rotate %d: %v", i, err)
		}
		if got := len(store.Keys()); got > 2 {
			t.Fatalf("after %d rotations: keys = %d, want ≤ 2", i+1, got)
		}
	}
}

func TestApply_InstallsGetConfigForClientThatReturnsCurrentKeys(t *testing.T) {
	dir := t.TempDir()
	store, err := EnsureStore(newFakeDeriver(t), dir)
	if err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	store.Apply(cfg)
	if cfg.GetConfigForClient == nil {
		t.Fatal("Apply did not install GetConfigForClient")
	}

	// First invocation should preserve caller's MinVersion and yield
	// a non-nil config (we can't read SessionTicketKeys directly).
	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if got == nil {
		t.Fatal("GetConfigForClient returned nil cfg")
	}
	if got.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion not preserved across callback: got %x", got.MinVersion)
	}

	// After Rotate the callback must keep returning a non-nil config —
	// the next handshake picks up new keys automatically.
	if err := store.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("post-rotate GetConfigForClient: %v", err)
	}
}

func TestWrappedFileIsMode0600(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureStore(newFakeDeriver(t), dir); err != nil {
		t.Fatalf("EnsureStore: %v", err)
	}
	for _, name := range []string{WrappedFile, SaltFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("%s mode = %#o, want 0600", name, mode)
		}
	}
}

func TestWrongMasterKeyRegeneratesStore(t *testing.T) {
	dir := t.TempDir()
	d1 := newFakeDeriver(t)
	first, err := EnsureStore(d1, dir)
	if err != nil {
		t.Fatalf("first EnsureStore: %v", err)
	}

	// Different master key, same salt path → unwrap fails; the store
	// must regenerate rather than fail the boot or return zero keys.
	d2 := &fakeDeriver{master: bytes.Repeat([]byte{0xFF}, 32)}
	second, err := EnsureStore(d2, dir)
	if err != nil {
		t.Fatalf("EnsureStore with wrong master key: %v", err)
	}
	k1, k2 := first.Keys(), second.Keys()
	if len(k2) != 1 || isZero(k2[0][:]) || k2[0] == k1[0] {
		t.Fatalf("regenerated store keys = %x, want one fresh non-zero key distinct from %x", k2, k1)
	}
	// The regenerated envelope is persisted under the new master key.
	third, err := EnsureStore(d2, dir)
	if err != nil {
		t.Fatalf("reload after regenerate: %v", err)
	}
	if third.Keys()[0] != k2[0] {
		t.Fatal("regenerated key did not persist")
	}
}

// legacyEnvelope builds an envelope the pre-AAD way: version byte not
// bound as associated data.
func legacyEnvelope(t *testing.T, payload, wrapKey []byte) []byte {
	t.Helper()
	aead, err := chacha20poly1305.NewX(wrapKey)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, nonceLen)
	out := append([]byte{WrapVersion}, nonce...)
	return aead.Seal(out, nonce, payload, nil)
}

func TestUnwrap_VersionByteIsAuthenticated(t *testing.T) {
	key := bytes.Repeat([]byte{7}, wrapKeyLen)
	payload := bytes.Repeat([]byte{9}, KeyLen)
	if _, err := unwrap(legacyEnvelope(t, payload, key), key); err == nil {
		t.Fatal("envelope sealed without the version byte as AAD unwrapped; version byte is not authenticated")
	}
	env, err := wrap(payload, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := unwrap(env, key)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip: %v", err)
	}
}

func TestEnsureStore_LegacyEnvelopeOnDiskRegenerates(t *testing.T) {
	dir := t.TempDir()
	d := newFakeDeriver(t)
	if _, err := EnsureStore(d, dir); err != nil {
		t.Fatal(err)
	}
	salt, err := os.ReadFile(SaltPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	wk, err := d.DeriveKey(HKDFPurpose, salt, wrapKeyLen)
	if err != nil {
		t.Fatal(err)
	}
	legacyKey := bytes.Repeat([]byte{3}, KeyLen)
	if err := os.WriteFile(WrappedPath(dir), legacyEnvelope(t, legacyKey, wk), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := EnsureStore(d, dir)
	if err != nil {
		t.Fatalf("EnsureStore over legacy envelope: %v", err)
	}
	if bytes.Equal(st.Keys()[0][:], legacyKey) {
		t.Fatal("legacy envelope was accepted instead of regenerated")
	}
}

func TestApply_ChainsPriorCallback(t *testing.T) {
	store, err := EnsureStore(newFakeDeriver(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		called++
		return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "from-prior"}, nil
	}
	store.Apply(cfg)
	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("prior GetConfigForClient called %d times, want 1", called)
	}
	if got == nil || got.ServerName != "from-prior" {
		t.Fatalf("prior callback's config was discarded: %+v", got)
	}
}

func TestApply_CachesCloneUntilRotate(t *testing.T) {
	store, err := EnsureStore(newFakeDeriver(t), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	store.Apply(cfg)
	a, _ := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	b, _ := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if a != b {
		t.Fatal("config cloned on every handshake; want a cached clone between rotations")
	}
	if err := store.Rotate(); err != nil {
		t.Fatal(err)
	}
	c, _ := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if c == a {
		t.Fatal("cached config not rebuilt after Rotate; new keys would never reach handshakes")
	}
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
