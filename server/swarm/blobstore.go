// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package swarm provides server-side blob storage and peer tracking for
// peer-to-peer content distribution. Blobs are content-addressed files
// stored by their SHA256 hash. Agents download chunks from peers or
// the server and report completion via the swarm.blobs trait.
package swarm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ChunkSize is the standard chunk size for swarm blob distribution (1 MiB).
const ChunkSize = 1 << 20 // 1048576

// BlobInfo describes a stored blob.
type BlobInfo struct {
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Label     string    `json:"label"`
	ChunkSize int       `json:"chunk_size"`
	Chunks    int       `json:"chunk_count"`
	CreatedAt time.Time `json:"created_at"`
}

// BlobStore is a filesystem-backed content-addressed blob store.
// Blobs are stored as flat files named by their SHA256 hash.
type BlobStore struct {
	dir    string
	mu     sync.RWMutex
	labels map[string]string // sha256 → label
}

// NewBlobStore creates a blob store at the given directory, creating it
// if it does not exist.
func NewBlobStore(dir string) (*BlobStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating blob directory: %w", err)
	}
	return &BlobStore{
		dir:    dir,
		labels: make(map[string]string),
	}, nil
}

// Store streams the content from r into the blob store, returning the
// SHA256 hash and size. If a blob with the same hash already exists,
// the write is a no-op and the existing blob's info is returned.
func (s *BlobStore) Store(r io.Reader, label string) (string, int64, error) {
	// Write to a temp file while computing the hash.
	tmp, err := os.CreateTemp(s.dir, ".blob-upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath) // no-op if renamed
	}()

	h := sha256.New()
	w := io.MultiWriter(tmp, h)
	size, err := io.Copy(w, r)
	if err != nil {
		return "", 0, fmt.Errorf("writing blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("closing temp file: %w", err)
	}

	hash := hex.EncodeToString(h.Sum(nil))
	dest := filepath.Join(s.dir, hash)

	// If the blob already exists, discard the temp file.
	if _, err := os.Stat(dest); err == nil {
		s.mu.Lock()
		if label != "" {
			s.labels[hash] = label
		}
		s.mu.Unlock()
		return hash, size, nil
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		return "", 0, fmt.Errorf("renaming blob: %w", err)
	}

	s.mu.Lock()
	if label != "" {
		s.labels[hash] = label
	}
	s.mu.Unlock()

	return hash, size, nil
}

// Open returns a file handle and size for the blob with the given hash.
func (s *BlobStore) Open(hash string) (*os.File, int64, error) {
	if !IsValidHash(hash) {
		return nil, 0, fmt.Errorf("invalid hash: %s", hash)
	}
	path := filepath.Join(s.dir, hash)
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// Has returns true if a blob with the given hash exists.
func (s *BlobStore) Has(hash string) bool {
	if !IsValidHash(hash) {
		return false
	}
	_, err := os.Stat(filepath.Join(s.dir, hash))
	return err == nil
}

// Delete removes a blob by hash.
func (s *BlobStore) Delete(hash string) error {
	if !IsValidHash(hash) {
		return fmt.Errorf("invalid hash: %s", hash)
	}
	s.mu.Lock()
	delete(s.labels, hash)
	s.mu.Unlock()
	return os.Remove(filepath.Join(s.dir, hash))
}

// List returns info for all stored blobs, sorted by creation time (newest first).
func (s *BlobStore) List() ([]BlobInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var blobs []BlobInfo
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !IsValidHash(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		blobs = append(blobs, BlobInfo{
			SHA256:    e.Name(),
			Size:      size,
			Label:     s.labels[e.Name()],
			ChunkSize: ChunkSize,
			Chunks:    chunksForSize(size),
			CreatedAt: info.ModTime(),
		})
	}

	sort.Slice(blobs, func(i, j int) bool {
		return blobs[i].CreatedAt.After(blobs[j].CreatedAt)
	})
	return blobs, nil
}

// SetLabel sets or updates the human-readable label for a blob.
func (s *BlobStore) SetLabel(hash, label string) {
	s.mu.Lock()
	s.labels[hash] = label
	s.mu.Unlock()
}

// Label returns the label for a blob, or empty string.
func (s *BlobStore) Label(hash string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.labels[hash]
}

// Dir returns the blob store directory path.
func (s *BlobStore) Dir() string {
	return s.dir
}

// chunksForSize returns the number of 1 MiB chunks needed for a blob of the given size.
func chunksForSize(size int64) int {
	if size <= 0 {
		return 0
	}
	n := int(size / int64(ChunkSize))
	if size%int64(ChunkSize) != 0 {
		n++
	}
	return n
}

// IsValidHash checks that a string looks like a hex-encoded SHA256 hash.
func IsValidHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	return isHexString(s)
}

// IsValidHashPrefix checks that a string is a valid hex prefix (at least 6 chars).
func IsValidHashPrefix(s string) bool {
	return len(s) >= 6 && isHexString(s)
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// Resolve expands a full hash or unique prefix to the full SHA256.
// Returns the full hash and true if exactly one blob matches, or empty and false
// if zero or multiple blobs match.
func (s *BlobStore) Resolve(hashOrPrefix string) (string, bool) {
	if IsValidHash(hashOrPrefix) {
		if s.Has(hashOrPrefix) {
			return hashOrPrefix, true
		}
		return "", false
	}
	if !IsValidHashPrefix(hashOrPrefix) {
		return "", false
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", false
	}
	var match string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, hashOrPrefix) && IsValidHash(name) {
			if match != "" {
				return "", false // ambiguous
			}
			match = name
		}
	}
	return match, match != ""
}
