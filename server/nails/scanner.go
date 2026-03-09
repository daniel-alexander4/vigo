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
)

// Entry represents a single custom nail executable with its name and checksum.
type Entry struct {
	Name   string // nail name derived from filename (extension stripped)
	SHA256 string // hex-encoded SHA256 checksum
}

// Scanner scans a directory for executable nail files.
type Scanner struct {
	dir string
	// nameToFile maps nail name -> actual filename on disk (cached from last ScanDir).
	nameToFile map[string]string
}

// NewScanner creates a Scanner that reads from the given directory.
func NewScanner(dir string) *Scanner {
	return &Scanner{dir: dir}
}

// ScanDir reads the custom nails directory and returns entries for each executable file.
// Non-executable files and subdirectories are silently skipped.
// If the directory does not exist, returns nil, nil (custom nails are optional).
// Results are sorted by name for deterministic ordering.
func (s *Scanner) ScanDir() ([]Entry, error) {
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.nameToFile = nil
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

	// Cache the name-to-filename mapping for ReadFile.
	s.nameToFile = nameToFile

	// Sort by name for deterministic ordering.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// ReadFile returns the raw bytes of a nail executable identified by its
// nail name (extension-stripped). ScanDir must be called first to build
// the name-to-filename mapping.
func (s *Scanner) ReadFile(name string) ([]byte, error) {
	if s.nameToFile == nil {
		return nil, fmt.Errorf("nail %q not found (no scan performed)", name)
	}

	filename, ok := s.nameToFile[name]
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
