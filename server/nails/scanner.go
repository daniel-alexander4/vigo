// Package nails provides a scanner for custom nail executables.
// The server distributes these executables to agents, which run them
// and merge their JSON output into the nails map under the custom.* namespace.
package nails

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry represents a single custom nail executable with its name and checksum.
type Entry struct {
	Name   string // nail name derived from filename (extension stripped)
	SHA256 string // hex-encoded SHA256 checksum
}

// Scanner scans a directory for executable nail files. Results are cached
// with a configurable TTL so that frequent CheckIn RPCs don't re-read and
// re-hash the entire directory on every call.
type Scanner struct {
	dir string
	ttl time.Duration

	mu         sync.RWMutex
	cached     []Entry
	cachedAt   time.Time
	nameToFile map[string]string // nail name -> actual filename on disk
}

// NewScanner creates a Scanner that reads from the given directory.
// Results are cached for ttl duration (0 means no caching).
func NewScanner(dir string, ttl time.Duration) *Scanner {
	return &Scanner{dir: dir, ttl: ttl}
}

// ScanDir reads the custom nails directory and returns entries for each executable file.
// Non-executable files and subdirectories are silently skipped.
// If the directory does not exist, returns nil, nil (custom nails are optional).
// Results are sorted by name for deterministic ordering.
// When a TTL is set, returns cached results if the cache is fresh.
func (s *Scanner) ScanDir() ([]Entry, error) {
	// Fast path: return cached results if fresh.
	if s.ttl > 0 {
		s.mu.RLock()
		if s.cached != nil && time.Since(s.cachedAt) < s.ttl {
			entries := s.cached
			s.mu.RUnlock()
			return entries, nil
		}
		s.mu.RUnlock()
	}

	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.mu.Lock()
			s.nameToFile = nil
			s.cached = nil
			s.mu.Unlock()
			return nil, nil
		}
		return nil, fmt.Errorf("reading nails directory %s: %w", s.dir, err)
	}

	var entries []Entry
	nameToFile := make(map[string]string)

	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}

		info, err := de.Info()
		if err != nil {
			continue
		}

		// Only include files with at least one executable bit set.
		if info.Mode()&0111 == 0 {
			continue
		}

		filename := de.Name()
		path := filepath.Join(s.dir, filename)

		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		hash := sha256.Sum256(data)
		name := stripExtension(filename)

		entries = append(entries, Entry{
			Name:   name,
			SHA256: fmt.Sprintf("%x", hash),
		})
		nameToFile[name] = filename
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

// ReadFile returns the raw bytes of a nail executable identified by its
// nail name (extension-stripped). ScanDir must be called first to build
// the name-to-filename mapping.
func (s *Scanner) ReadFile(name string) ([]byte, error) {
	s.mu.RLock()
	nameToFile := s.nameToFile
	s.mu.RUnlock()

	if nameToFile == nil {
		return nil, fmt.Errorf("nail %q not found (no scan performed)", name)
	}

	filename, ok := nameToFile[name]
	if !ok {
		return nil, fmt.Errorf("nail %q not found", name)
	}

	return os.ReadFile(filepath.Join(s.dir, filename))
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
