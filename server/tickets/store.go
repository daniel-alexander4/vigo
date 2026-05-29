// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package tickets

import (
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// KeyDeriver mirrors the puddle/bolt dependency on the secrets backend:
// a controlled HKDF derivation that never exposes the master key.
// *secrets.LocalProvider satisfies this. Backends without master-key
// access (isopass etc.) cannot host a persistent ticket-key store in
// v1 — the caller checks the concrete provider type before calling
// EnsureStore.
type KeyDeriver interface {
	DeriveKey(purpose string, salt []byte, outLen int) ([]byte, error)
}

// Store holds the active and grace TLS session-ticket encryption keys.
// Rotation updates the in-memory slice; live tls.Configs pick up the
// new keys on the next handshake via the GetConfigForClient callback
// installed by Apply (per-handshake invocation survives the clones that
// credentials.NewTLS and http.Server.ServeTLS perform on the original
// tls.Config).
//
// First key in Keys() is active (used for issuing new tickets); the
// remaining are accepted for decrypting tickets handed back by clients
// across a rotation boundary.
type Store struct {
	dir     string
	deriver KeyDeriver
	salt    []byte

	mu   sync.RWMutex
	keys [][KeyLen]byte // [0] = active, [1] = grace (if present)
}

// EnsureStore loads the ticket-key store from dir, generating it on
// first call. On first call: dir is created (0700), a fresh 32-byte
// HKDF salt is generated, a fresh active ticket key is generated, the
// envelope is wrapped under the master key, and both files are written
// atomically.
//
// On subsequent calls: the salt is read, the wrap key re-derived, the
// envelope unwrapped, and the recovered key list returned.
//
// A master-key rotation invalidates the wrap and surfaces an unwrap
// error; recovery is operator-driven (delete the directory and re-init
// — every envoy will full-handshake on its next CheckIn, the same
// behaviour as a fresh deploy without persistence).
func EnsureStore(deriver KeyDeriver, dir string) (*Store, error) {
	if deriver == nil {
		return nil, errors.New("tickets: nil key deriver")
	}
	if dir == "" {
		return nil, errors.New("tickets: empty dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("tickets: creating %s: %w", dir, err)
	}

	wrappedPath := WrappedPath(dir)
	saltPath := SaltPath(dir)

	if _, err := os.Stat(wrappedPath); errors.Is(err, os.ErrNotExist) {
		return initStore(deriver, dir, wrappedPath, saltPath)
	} else if err != nil {
		return nil, fmt.Errorf("tickets: stat %s: %w", wrappedPath, err)
	}
	return loadStore(deriver, dir, wrappedPath, saltPath)
}

// Keys returns a copy of the current ticket-encryption keys, with the
// active key first. Safe for concurrent calls; the returned slice is
// owned by the caller.
func (s *Store) Keys() [][KeyLen]byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([][KeyLen]byte, len(s.keys))
	copy(out, s.keys)
	return out
}

// Apply installs the store as the session-ticket-keys source on cfg
// via cfg.GetConfigForClient. The callback closes over cfg so every
// per-handshake clone preserves the caller's other settings
// (ClientAuth, ClientCAs, GetCertificate, MinVersion, etc.) while
// always getting the current key slice. Idempotent across rotations —
// the store needs no config registry because the callback reads
// store.keys at handshake time.
func (s *Store) Apply(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	base := cfg // capture the caller's config; clone per handshake
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		out := base.Clone()
		s.mu.RLock()
		keys := make([][KeyLen]byte, len(s.keys))
		copy(keys, s.keys)
		s.mu.RUnlock()
		out.SetSessionTicketKeys(keys)
		return out, nil
	}
}

// Rotate generates a new active key, demotes the current active to
// grace, and persists both. The next handshake picks up the new keys
// automatically via the Apply'd GetConfigForClient callback. The
// previous grace key is discarded — clients that present a ticket from
// two rotations ago full-handshake.
func (s *Store) Rotate() error {
	var fresh [KeyLen]byte
	if _, err := io.ReadFull(rand.Reader, fresh[:]); err != nil {
		return fmt.Errorf("tickets: generate fresh key: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// New active first, previous active becomes grace. Cap at 2 — Go's
	// tls.SetSessionTicketKeys accepts up to 64 keys but a 1-rotation
	// grace is the right tradeoff between forward secrecy and avoiding
	// full handshakes around a rotation boundary.
	prev := s.keys
	s.keys = append([][KeyLen]byte{fresh}, prev[:min(len(prev), 1)]...)

	if err := persistKeys(s.deriver, s.dir, s.salt, s.keys); err != nil {
		// Roll back in-memory state so a failed persist doesn't leave
		// future handshakes holding keys that won't survive a restart.
		// Caller (rotation loop) logs the error.
		s.keys = prev
		return fmt.Errorf("tickets: persist after rotate: %w", err)
	}
	return nil
}

func initStore(deriver KeyDeriver, dir, wrappedPath, saltPath string) (*Store, error) {
	salt, err := freshSalt()
	if err != nil {
		return nil, fmt.Errorf("tickets: %w", err)
	}
	var fresh [KeyLen]byte
	if _, err := io.ReadFull(rand.Reader, fresh[:]); err != nil {
		return nil, fmt.Errorf("tickets: generate initial key: %w", err)
	}
	keys := [][KeyLen]byte{fresh}

	if err := writePrivateFile(saltPath, salt); err != nil {
		return nil, fmt.Errorf("tickets: writing salt: %w", err)
	}
	if err := persistKeys(deriver, dir, salt, keys); err != nil {
		return nil, fmt.Errorf("tickets: %w", err)
	}
	return &Store{dir: dir, deriver: deriver, salt: salt, keys: keys}, nil
}

func loadStore(deriver KeyDeriver, dir, wrappedPath, saltPath string) (*Store, error) {
	envelope, err := os.ReadFile(wrappedPath)
	if err != nil {
		return nil, fmt.Errorf("tickets: reading %s: %w", wrappedPath, err)
	}
	salt, err := os.ReadFile(saltPath)
	if err != nil {
		return nil, fmt.Errorf("tickets: reading %s: %w", saltPath, err)
	}
	if len(salt) != SaltLen {
		return nil, fmt.Errorf("tickets: salt at %s is %d bytes, expected %d", saltPath, len(salt), SaltLen)
	}
	wrapKey, err := deriver.DeriveKey(HKDFPurpose, salt, wrapKeyLen)
	if err != nil {
		return nil, fmt.Errorf("tickets: derive wrap key: %w", err)
	}
	payload, err := unwrap(envelope, wrapKey)
	if err != nil {
		return nil, fmt.Errorf("tickets: %w", err)
	}
	keys := make([][KeyLen]byte, len(payload)/KeyLen)
	for i := range keys {
		copy(keys[i][:], payload[i*KeyLen:(i+1)*KeyLen])
	}
	return &Store{dir: dir, deriver: deriver, salt: salt, keys: keys}, nil
}

// persistKeys wraps the concatenated key list under the per-host salt
// and writes the envelope atomically.
func persistKeys(deriver KeyDeriver, dir string, salt []byte, keys [][KeyLen]byte) error {
	wrapKey, err := deriver.DeriveKey(HKDFPurpose, salt, wrapKeyLen)
	if err != nil {
		return fmt.Errorf("derive wrap key: %w", err)
	}
	payload := make([]byte, 0, len(keys)*KeyLen)
	for i := range keys {
		payload = append(payload, keys[i][:]...)
	}
	envelope, err := wrap(payload, wrapKey)
	if err != nil {
		return fmt.Errorf("wrap: %w", err)
	}
	return writePrivateFile(WrappedPath(dir), envelope)
}

// writePrivateFile writes data to path atomically with mode 0600.
// Mirrors server/puddle/identity.go's helper; not extracted because
// neither caller wins more than a one-line dedup from sharing.
func writePrivateFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) } // best-effort temp cleanup
	if err := tmp.Chmod(0o600); err != nil {
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
