// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LocalProvider is the default secrets backend. It stores secrets as
// AES-256-GCM encrypted files under a directory, one file per secret path.
//
// Secret path "hobnob/db/dsn" maps to file "<secrets_dir>/hobnob/db/dsn".
// Each file contains: 12-byte nonce || ciphertext || GCM tag (16 bytes).
//
// If no key_file is specified, a master key is auto-generated at
// <secrets_dir>/.master.key on first use. Secrets are never stored as plaintext.
//
// For core keys not present in the secrets dir, falls back to built-in defaults.
type LocalProvider struct {
	secretsDir string
	gcm        cipher.AEAD
	mu         sync.RWMutex
}

// NewLocalProvider creates a provider that reads encrypted secrets from
// secretsDir using the master key in keyFile. The key file must be exactly
// 32 bytes (raw) or 64 hex characters, or any length (hashed with SHA-256).
//
// If keyFile is empty, a 32-byte master key is auto-generated at
// <secretsDir>/.master.key. Secrets are always encrypted at rest.
func NewLocalProvider(keyFile, secretsDir string) (*LocalProvider, error) {
	// Ensure secrets directory exists.
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating secrets directory %q: %w", secretsDir, err)
	}

	// No key file = auto-generate one inside the secrets directory.
	if keyFile == "" {
		keyFile = filepath.Join(secretsDir, ".master.key")
		if _, err := os.Stat(keyFile); os.IsNotExist(err) {
			key := make([]byte, 32)
			if _, err := io.ReadFull(rand.Reader, key); err != nil {
				return nil, fmt.Errorf("generating master key: %w", err)
			}
			encoded := []byte(hex.EncodeToString(key) + "\n")
			if err := os.WriteFile(keyFile, encoded, 0o600); err != nil {
				return nil, fmt.Errorf("writing auto-generated master key %q: %w", keyFile, err)
			}
		}
	}

	keyData, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("reading master key %q: %w", keyFile, err)
	}

	key, err := deriveKey(keyData)
	if err != nil {
		return nil, fmt.Errorf("deriving encryption key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	return &LocalProvider{
		secretsDir: secretsDir,
		gcm:        gcm,
	}, nil
}

// Get retrieves and decrypts a secret by its path.
// Falls back to built-in defaults for core keys if not found on disk.
func (p *LocalProvider) Get(_ context.Context, key string) (string, error) {
	data, err := p.readAndDecrypt(key)
	if err != nil {
		if os.IsNotExist(err) {
			// Fall back to built-in defaults for core keys.
			envKey := pathToEnvVar(key)
			if val, ok := builtinDefaults[envKey]; ok {
				return val, nil
			}
			return "", fmt.Errorf("secret %q not found in %s", key, p.secretsDir)
		}
		return "", err
	}
	return string(data), nil
}

// GetBytes retrieves and decrypts a binary secret.
func (p *LocalProvider) GetBytes(ctx context.Context, key string) ([]byte, error) {
	val, err := p.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	return []byte(val), nil
}

// GetVersion returns the file modification time as a version number (unix seconds).
// This allows the rotation watcher to detect when a secret has been re-encrypted.
func (p *LocalProvider) GetVersion(_ context.Context, key string) (int, error) {
	path := p.secretPath(key)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("stat secret %q: %w", key, err)
	}
	return int(info.ModTime().Unix()), nil
}

// Set encrypts and writes a secret value. Creates parent directories as needed.
func (p *LocalProvider) Set(key, value string) error {
	return p.encryptAndWrite(key, []byte(value))
}

// Delete removes an encrypted secret file.
func (p *LocalProvider) Delete(key string) error {
	path := p.secretPath(key)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting secret %q: %w", key, err)
	}
	return nil
}

// List returns all secret key paths in the secrets directory.
func (p *LocalProvider) List() ([]string, error) {
	var keys []string
	err := filepath.WalkDir(p.secretsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip the auto-generated master key file.
		if d.Name() == ".master.key" {
			return nil
		}
		rel, err := filepath.Rel(p.secretsDir, path)
		if err != nil {
			return err
		}
		keys = append(keys, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	return keys, nil
}

// Close is a no-op for LocalProvider.
func (p *LocalProvider) Close() error {
	return nil
}

// secretPath returns the filesystem path for a secret key.
func (p *LocalProvider) secretPath(key string) string {
	return filepath.Join(p.secretsDir, filepath.FromSlash(key))
}

// readAndDecrypt reads an encrypted file and decrypts it.
func (p *LocalProvider) readAndDecrypt(key string) ([]byte, error) {
	path := p.secretPath(key)

	// Prevent path traversal.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving path for %q: %w", key, err)
	}
	absDir, err := filepath.Abs(p.secretsDir)
	if err != nil {
		return nil, fmt.Errorf("resolving secrets dir: %w", err)
	}
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("secret key %q escapes secrets directory", key)
	}

	// Resolve symlinks and re-check containment.
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, err
	}
	resolvedDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return nil, fmt.Errorf("resolving secrets dir: %w", err)
	}
	if !strings.HasPrefix(resolved, resolvedDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("secret key %q resolves outside secrets directory", key)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}

	nonceSize := p.gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("secret %q: ciphertext too short", key)
	}

	nonce := data[:nonceSize]
	encrypted := data[nonceSize:]

	plaintext, err := p.gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting secret %q: %w (wrong master key?)", key, err)
	}

	return plaintext, nil
}

// encryptAndWrite encrypts data and writes it to the secret file atomically.
func (p *LocalProvider) encryptAndWrite(key string, plaintext []byte) error {
	path := p.secretPath(key)

	// Prevent path traversal.
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path for %q: %w", key, err)
	}
	absDir, err := filepath.Abs(p.secretsDir)
	if err != nil {
		return fmt.Errorf("resolving secrets dir: %w", err)
	}
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return fmt.Errorf("secret key %q escapes secrets directory", key)
	}

	// Resolve symlinks on the parent directory to prevent symlink-based escapes.
	// The target file may not exist yet, so resolve the parent.
	parentDir := filepath.Dir(absPath)
	if _, err := os.Stat(parentDir); err == nil {
		resolvedParent, err := filepath.EvalSymlinks(parentDir)
		if err != nil {
			return fmt.Errorf("resolving parent directory for %q: %w", key, err)
		}
		resolvedDir, err := filepath.EvalSymlinks(absDir)
		if err != nil {
			return fmt.Errorf("resolving secrets dir: %w", err)
		}
		if !strings.HasPrefix(resolvedParent+string(filepath.Separator), resolvedDir+string(filepath.Separator)) && resolvedParent != resolvedDir {
			return fmt.Errorf("secret key %q resolves outside secrets directory", key)
		}
	}

	// Create parent directories.
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return fmt.Errorf("creating directory for %q: %w", key, err)
	}

	nonce := make([]byte, p.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}
	fileData := p.gcm.Seal(nonce, nonce, plaintext, nil)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Write atomically via temp file + rename.
	tmp := absPath + ".tmp"
	if err := os.WriteFile(tmp, fileData, 0o600); err != nil {
		return fmt.Errorf("writing secret %q: %w", key, err)
	}
	if err := os.Rename(tmp, absPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming secret %q: %w", key, err)
	}

	return nil
}

// deriveKey normalizes key material to a 32-byte AES-256 key.
// Accepts: 32 raw bytes, 64 hex chars, or arbitrary bytes (SHA-256 hashed).
func deriveKey(raw []byte) ([]byte, error) {
	trimmed := strings.TrimSpace(string(raw))

	// 64 hex characters = 32-byte key.
	if len(trimmed) == 64 {
		key, err := hex.DecodeString(trimmed)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}

	// Raw 32 bytes (binary key file).
	if len(raw) == 32 {
		return raw, nil
	}

	// Anything else: SHA-256 hash to get 32 bytes.
	hash := sha256.Sum256(raw)
	return hash[:], nil
}
