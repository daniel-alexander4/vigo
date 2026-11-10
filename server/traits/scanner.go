// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package traits provides a scanner for custom trait executables.
// The server distributes these executables to agents, which run them
// and merge their JSON output into the traits map under the custom.* namespace.
package traits

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Entry represents a single custom trait executable with its name and checksum.
type Entry struct {
	Name   string // trait name derived from filename (extension stripped)
	SHA256 string // hex-encoded SHA256 checksum
}

// Scanner scans a directory for executable trait files. Results are cached
// with a configurable TTL so that frequent CheckIn RPCs don't re-read and
// re-hash the entire directory on every call.
type Scanner struct {
	dir string
	ttl time.Duration

	// scanMu serializes rescans: when the TTL expires under load, one
	// caller rescans and the rest wait for and reuse its result instead of
	// each re-reading and re-hashing the directory.
	scanMu sync.Mutex

	mu         sync.RWMutex
	cached     []Entry
	cachedAt   time.Time
	nameToFile map[string]string // trait name -> actual filename on disk
}

// testScanHook, when non-nil, runs at the start of every directory rescan.
var testScanHook func()

// NewScanner creates a Scanner that reads from the given directory.
// Results are cached for ttl duration (0 means no caching).
func NewScanner(dir string, ttl time.Duration) *Scanner {
	return &Scanner{dir: dir, ttl: ttl}
}

// ScanDir reads the custom traits directory and returns entries for each executable file.
// Non-executable files and subdirectories are silently skipped.
// If the directory does not exist, returns nil, nil (custom traits are optional).
// Results are sorted by name for deterministic ordering.
// When a TTL is set, returns cached results if the cache is fresh.
//
// Cache-validity sentinel is `cachedAt` being non-zero (i.e. ever scanned),
// NOT `cached != nil`. An empty-or-missing custom-traits directory produces
// `cached == nil` legitimately; without this distinction the cache would
// miss on every CheckIn and the slow-path Lock would queue every caller —
// surfaced at ≥ 8K req/s in the 2026-05-25 mixed-driver loadtest as 93 % of
// total mutex wait. The slow-path now also stamps cachedAt on the
// `ErrNotExist` branch so that empty-cache state is itself cached for TTL.
func (s *Scanner) ScanDir() ([]Entry, error) {
	// Fast path: return cached results if fresh.
	if s.ttl > 0 {
		s.mu.RLock()
		if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < s.ttl {
			entries := s.cached
			s.mu.RUnlock()
			return entries, nil
		}
		s.mu.RUnlock()

		s.scanMu.Lock()
		defer s.scanMu.Unlock()
		// Another caller may have rescanned while this one waited.
		s.mu.RLock()
		if !s.cachedAt.IsZero() && time.Since(s.cachedAt) < s.ttl {
			entries := s.cached
			s.mu.RUnlock()
			return entries, nil
		}
		s.mu.RUnlock()
	}

	if testScanHook != nil {
		testScanHook()
	}
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.mu.Lock()
			s.nameToFile = nil
			s.cached = nil
			s.cachedAt = time.Now()
			s.mu.Unlock()
			return nil, nil
		}
		return nil, fmt.Errorf("reading traits directory %s: %w", s.dir, err)
	}

	type found struct {
		filename string
		sum      string
	}
	byName := make(map[string][]found)

	for _, de := range dirEntries {
		info, err := de.Info() // lstat: a symlink reports as a symlink
		if err != nil {
			continue
		}
		// Regular files only: a symlink would be followed to whatever it
		// points at, and that is what agents would execute.
		if !info.Mode().IsRegular() {
			continue
		}
		// Only include files with at least one executable bit set.
		if info.Mode()&0111 == 0 {
			continue
		}

		filename := de.Name()
		data, err := readRegularFile(filepath.Join(s.dir, filename))
		if err != nil {
			continue
		}
		hash := sha256.Sum256(data)
		name := stripExtension(filename)
		byName[name] = append(byName[name], found{filename: filename, sum: fmt.Sprintf("%x", hash)})
	}

	var entries []Entry
	nameToFile := make(map[string]string, len(byName))
	for name, hits := range byName {
		if len(hits) > 1 {
			// foo.sh and foo.py both become trait "foo"; serving either
			// would mismatch the hash advertised for the other. Skip the
			// name entirely until the operator renames one.
			files := make([]string, len(hits))
			for i, f := range hits {
				files[i] = f.filename
			}
			slog.Warn("custom traits: files collide on one trait name; skipping it", "trait", name, "files", files, "dir", s.dir)
			continue
		}
		entries = append(entries, Entry{Name: name, SHA256: hits[0].sum})
		nameToFile[name] = hits[0].filename
	}

	// Sort by name for deterministic ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	// Cache results.
	s.mu.Lock()
	s.nameToFile = nameToFile
	s.cached = entries
	s.cachedAt = time.Now()
	s.mu.Unlock()

	return entries, nil
}

// ReadFile returns the raw bytes of a trait executable identified by its
// trait name (extension-stripped). ScanDir must be called first to build
// the name-to-filename mapping.
func (s *Scanner) ReadFile(name string) ([]byte, error) {
	s.mu.RLock()
	nameToFile := s.nameToFile
	s.mu.RUnlock()

	if nameToFile == nil {
		return nil, fmt.Errorf("trait %q not found (no scan performed)", name)
	}

	filename, ok := nameToFile[name]
	if !ok {
		return nil, fmt.Errorf("trait %q not found", name)
	}

	return readRegularFile(filepath.Join(s.dir, filename))
}

// readRegularFile reads path without following a final symlink, so a file
// swapped for a symlink after the scan is refused rather than served.
func readRegularFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return io.ReadAll(f)
}

// stripExtension removes the last file extension from a filename.
// "app-version.sh" -> "app-version", "disk-check" -> "disk-check",
// "cpu.check.sh" -> "cpu.check"
func stripExtension(filename string) string {
	ext := filepath.Ext(filename)
	if ext == "" {
		return filename
	}
	return strings.TrimSuffix(filename, ext)
}
