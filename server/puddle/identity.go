// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package puddle

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeyDeriver is the dependency the puddle package needs from the
// secrets backend: a controlled HKDF derivation that never exposes
// the master key. *secrets.LocalProvider satisfies this; tests use a
// stub. Backends without master-key access (isopass, etc.) cannot
// host a service-account puddle in v1 — the caller checks the
// concrete provider type before calling EnsureIdentity.
type KeyDeriver interface {
	DeriveKey(purpose string, salt []byte, outLen int) ([]byte, error)
}

// Identity is an unlocked service-account puddle keypair. The signing
// key is held in memory only — wipe by dropping the reference. The
// text form mirrors the per-user puddle's encoding ("ed25519:<hex>")
// so swarm peers see one identity format regardless of origin.
type Identity struct {
	SigningKey    ed25519.PrivateKey
	PublicKey     ed25519.PublicKey
	PublicKeyText string
}

// EnsureIdentity loads the service-account puddle identity from dir,
// generating it on first call. The wrapped envelope is at
// <dir>/identity.wrapped, the per-host HKDF salt at <dir>/.salt, and
// the (public) pubkey text at <dir>/identity.pub.
//
// First-call semantics: the dir is created (0700), a fresh Ed25519
// keypair is generated, a fresh 32-byte salt is generated, the wrap
// key is derived from the secrets master key via HKDF, the seed is
// wrapped, and all three files are written atomically.
//
// Subsequent calls re-derive the wrap key from the same salt and the
// (presumed-unchanged) master key, unwrap the envelope, and return
// the identity. A master-key rotation invalidates the wrap and
// surfaces an unwrap error — recovery is operator-driven (rekey per
// ADR-018 or, in the degenerate single-vigosrv case, delete the
// directory and re-init).
func EnsureIdentity(deriver KeyDeriver, dir string) (*Identity, error) {
	return EnsureIdentityWithPurpose(deriver, dir, HKDFPurpose)
}

// EnsureIdentityWithPurpose is the parameterized core of EnsureIdentity:
// the HKDF purpose string binds the wrap-key derivation to a specific
// use. Service-account puddles call EnsureIdentity (purpose
// `vigo-puddle-wrap-v1`); sibling identity packages (server/bolt,
// future per-role identities) call this directly with their own
// purpose so a theoretical master-key leak that only exposes one
// derivation cannot cross the role boundary.
func EnsureIdentityWithPurpose(deriver KeyDeriver, dir, purpose string) (*Identity, error) {
	if deriver == nil {
		return nil, errors.New("puddle: nil key deriver")
	}
	if dir == "" {
		return nil, errors.New("puddle: empty dir")
	}
	if purpose == "" {
		return nil, errors.New("puddle: empty HKDF purpose")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("puddle: creating %s: %w", dir, err)
	}

	wrappedPath := WrappedPath(dir)
	saltPath := SaltPath(dir)
	pubkeyPath := PubkeyPath(dir)

	if _, err := os.Stat(wrappedPath); errors.Is(err, os.ErrNotExist) {
		return initIdentity(deriver, dir, purpose, wrappedPath, saltPath, pubkeyPath)
	} else if err != nil {
		return nil, fmt.Errorf("puddle: stat %s: %w", wrappedPath, err)
	}
	return loadIdentity(deriver, purpose, wrappedPath, saltPath, pubkeyPath)
}

// LoadIdentity unwraps an existing identity. Errors if the wrapped
// file is missing — callers wanting first-time generation use
// EnsureIdentity.
func LoadIdentity(deriver KeyDeriver, dir string) (*Identity, error) {
	return LoadIdentityWithPurpose(deriver, dir, HKDFPurpose)
}

// LoadIdentityWithPurpose is the parameterized core of LoadIdentity;
// see EnsureIdentityWithPurpose for the purpose-string contract.
func LoadIdentityWithPurpose(deriver KeyDeriver, dir, purpose string) (*Identity, error) {
	if deriver == nil {
		return nil, errors.New("puddle: nil key deriver")
	}
	if purpose == "" {
		return nil, errors.New("puddle: empty HKDF purpose")
	}
	return loadIdentity(deriver, purpose, WrappedPath(dir), SaltPath(dir), PubkeyPath(dir))
}

func initIdentity(deriver KeyDeriver, dir, purpose, wrappedPath, saltPath, pubkeyPath string) (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("puddle: generate keypair: %w", err)
	}
	seed := priv.Seed()

	salt, err := freshSalt()
	if err != nil {
		return nil, fmt.Errorf("puddle: %w", err)
	}
	wrapKey, err := deriver.DeriveKey(purpose, salt, wrapKeyLen)
	if err != nil {
		return nil, fmt.Errorf("puddle: derive wrap key: %w", err)
	}
	envelope, err := wrap(seed, wrapKey)
	if err != nil {
		return nil, fmt.Errorf("puddle: wrap: %w", err)
	}

	pubText := EncodePubkey(pub)
	if err := writePrivateFile(saltPath, salt); err != nil {
		return nil, fmt.Errorf("puddle: writing salt: %w", err)
	}
	if err := writePrivateFile(wrappedPath, envelope); err != nil {
		return nil, fmt.Errorf("puddle: writing wrapped identity: %w", err)
	}
	if err := writePublicFile(pubkeyPath, []byte(pubText+"\n")); err != nil {
		return nil, fmt.Errorf("puddle: writing pubkey: %w", err)
	}

	return &Identity{SigningKey: priv, PublicKey: pub, PublicKeyText: pubText}, nil
}

func loadIdentity(deriver KeyDeriver, purpose, wrappedPath, saltPath, pubkeyPath string) (*Identity, error) {
	envelope, err := os.ReadFile(wrappedPath)
	if err != nil {
		return nil, fmt.Errorf("puddle: reading %s: %w", wrappedPath, err)
	}
	salt, err := os.ReadFile(saltPath)
	if err != nil {
		return nil, fmt.Errorf("puddle: reading %s: %w", saltPath, err)
	}
	if len(salt) != SaltLen {
		return nil, fmt.Errorf("puddle: salt at %s is %d bytes, expected %d", saltPath, len(salt), SaltLen)
	}
	wrapKey, err := deriver.DeriveKey(purpose, salt, wrapKeyLen)
	if err != nil {
		return nil, fmt.Errorf("puddle: derive wrap key: %w", err)
	}
	seed, err := unwrap(envelope, wrapKey)
	if err != nil {
		return nil, fmt.Errorf("puddle: %w", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("puddle: derived public key has wrong type")
	}
	pubText := EncodePubkey(pub)

	// Pubkey file is convenience-only; the unwrapped key is authoritative.
	// Refresh it if missing or mismatched (e.g. an operator deleted it).
	if existing, err := os.ReadFile(pubkeyPath); err != nil || string(existing) != pubText+"\n" {
		_ = writePublicFile(pubkeyPath, []byte(pubText+"\n"))
	}

	return &Identity{SigningKey: priv, PublicKey: pub, PublicKeyText: pubText}, nil
}

// EncodePubkey formats an Ed25519 public key as "ed25519:<hex>",
// matching the per-user puddle's textual form (see
// agent/src/swarm/lockbox/crypt.rs).
func EncodePubkey(pk ed25519.PublicKey) string {
	return "ed25519:" + hex.EncodeToString(pk)
}

// writePrivateFile writes data to path atomically with mode 0600.
func writePrivateFile(path string, data []byte) error {
	return writeFile(path, data, 0o600)
}

// writePublicFile writes data to path atomically with mode 0644 (the
// pubkey is public-by-construction).
func writePublicFile(path string, data []byte) error {
	return writeFile(path, data, 0o644)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// best-effort: cleanup runs only after we've already errored higher up.
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
