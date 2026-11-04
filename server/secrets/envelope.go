// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Envelope format v1 (everything the LocalProvider encrypts):
//
//	magic "VGE\x01" (4 bytes) || nonce (12) || AES-256-GCM ciphertext || tag (16)
//
// The AES key is a per-purpose HKDF-SHA256 subkey of the master key (the
// master key itself never keys a cipher), and the GCM additional data binds
// the ciphertext to its purpose and to the object it belongs to (the secret's
// path, the compliance doc's id). A ciphertext moved to another path, or fed
// to another purpose, fails to open.
//
// The pre-v1 format (v0: nonce || ciphertext under the raw master key, no
// AAD) is readable ONLY by MigrateV0Envelope, which re-seals it as v1. The
// secrets directory is migrated in NewLocalProvider and the compliance-doc
// directory in compliancedocs.New; every other read refuses v0.
const envelopeMagic = "VGE\x01"

// Purpose names an encryption domain. Each purpose has its own subkey.
type Purpose string

const (
	// purposeSecret encrypts secret files under the secrets directory.
	purposeSecret Purpose = "vigo-secret-v1"
	// PurposeComplianceDoc encrypts compliance-doc uploads; AAD is the doc id.
	PurposeComplianceDoc Purpose = "vigo-compliancedoc-v1"
)

// ErrLegacyEnvelope is returned when a ciphertext is in the pre-v1 format.
// It is never decrypted outside the one-shot migration.
var ErrLegacyEnvelope = errors.New("ciphertext is in the legacy v0 format (not migrated)")

// aeadFor returns the AES-256-GCM AEAD keyed by purpose's subkey.
func (p *LocalProvider) aeadFor(purpose Purpose) (cipher.AEAD, error) {
	p.mu.RLock()
	a, ok := p.aeads[purpose]
	p.mu.RUnlock()
	if ok {
		return a, nil
	}
	key, err := p.DeriveKey(string(purpose), nil, 32)
	if err != nil {
		return nil, fmt.Errorf("deriving %s subkey: %w", purpose, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	a, err = cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.aeads == nil {
		p.aeads = make(map[Purpose]cipher.AEAD)
	}
	p.aeads[purpose] = a
	p.mu.Unlock()
	return a, nil
}

// envelopeAAD binds a ciphertext to its format version, purpose and object.
func envelopeAAD(purpose Purpose, aad []byte) []byte {
	var b bytes.Buffer
	b.WriteString(envelopeMagic)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(purpose)))
	b.Write(n[:])
	b.WriteString(string(purpose))
	b.Write(aad)
	return b.Bytes()
}

// Encrypt seals plaintext for purpose, bound to aad (the object identity,
// e.g. a compliance doc id). Output is a v1 envelope.
func (p *LocalProvider) Encrypt(purpose Purpose, aad, plaintext []byte) ([]byte, error) {
	a, err := p.aeadFor(purpose)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}
	out := make([]byte, 0, len(envelopeMagic)+len(nonce)+len(plaintext)+a.Overhead())
	out = append(out, envelopeMagic...)
	out = append(out, nonce...)
	return a.Seal(out, nonce, plaintext, envelopeAAD(purpose, aad)), nil
}

// Decrypt opens a v1 envelope sealed by Encrypt with the same purpose and
// aad. A legacy v0 envelope is refused with ErrLegacyEnvelope.
func (p *LocalProvider) Decrypt(purpose Purpose, aad, envelope []byte) ([]byte, error) {
	a, err := p.aeadFor(purpose)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(envelope, []byte(envelopeMagic)) {
		return nil, ErrLegacyEnvelope
	}
	body := envelope[len(envelopeMagic):]
	if len(body) < a.NonceSize()+a.Overhead() {
		return nil, fmt.Errorf("envelope too short for nonce + GCM tag")
	}
	nonce, ct := body[:a.NonceSize()], body[a.NonceSize():]
	pt, err := a.Open(nil, nonce, ct, envelopeAAD(purpose, aad))
	if err != nil {
		return nil, fmt.Errorf("decrypting envelope: %w (wrong master key, purpose or object?)", err)
	}
	return pt, nil
}

// MigrateV0Envelope is the only reader of the legacy v0 format. If data
// already opens as v1 for (purpose, aad) it is returned unchanged with
// migrated=false. Otherwise it is decrypted as v0 under the raw master key
// and re-sealed as v1 (migrated=true). Data that is neither fails.
func (p *LocalProvider) MigrateV0Envelope(purpose Purpose, aad, data []byte) (out []byte, migrated bool, err error) {
	if _, err := p.Decrypt(purpose, aad, data); err == nil {
		return data, false, nil
	}
	ns := p.legacyV0.NonceSize()
	if len(data) < ns+p.legacyV0.Overhead() {
		return nil, false, errors.New("not a v0 or v1 envelope")
	}
	pt, err := p.legacyV0.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return nil, false, fmt.Errorf("not a v0 or v1 envelope: %w", err)
	}
	v1, err := p.Encrypt(purpose, aad, pt)
	if err != nil {
		return nil, false, err
	}
	return v1, true, nil
}

// secretAAD is the canonical object identity of a secret: its slash-form
// path relative to the secrets directory.
func secretAAD(key string) []byte {
	return []byte(filepath.ToSlash(filepath.Clean(filepath.FromSlash(key))))
}

// migrateSecretsDir re-seals every v0 secret file as v1 in place (atomic
// rename, 0600). Files that are neither v0 nor v1 secrets (the master key,
// other tools' files) are left alone. A file changed while it was being
// migrated is left for the next run rather than overwritten.
func (p *LocalProvider) migrateSecretsDir() {
	var migrated, failed int
	_ = filepath.WalkDir(p.secretsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == MasterKeyFile || strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		rel, rerr := filepath.Rel(p.secretsDir, path)
		if rerr != nil {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		out, did, merr := p.MigrateV0Envelope(purposeSecret, secretAAD(rel), data)
		if merr != nil || !did {
			return nil // not a secret envelope, or already v1
		}
		if werr := replaceIfUnchanged(path, data, out); werr != nil {
			failed++
			slog.Error("secrets: migrating secret to the v1 envelope failed", "key", filepath.ToSlash(rel), "error", werr)
			return nil
		}
		migrated++
		return nil
	})
	if migrated > 0 || failed > 0 {
		slog.Info("secrets: migrated legacy v0 secrets to the v1 envelope", "migrated", migrated, "failed", failed)
	}
}

// replaceIfUnchanged atomically replaces path with newData, provided path
// still holds oldData (a concurrent Set wins).
func replaceIfUnchanged(path string, oldData, newData []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".migrate-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(newData); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, oldData) {
		return errors.New("file changed during migration; left for the next run")
	}
	return os.Rename(tmpName, path)
}
