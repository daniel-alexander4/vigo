// Package bootstrap provides HTTP handlers for agent bootstrap scripts,
// binary distribution, and version information.
package bootstrap

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// checksumCache caches SHA256 checksums for agent binaries keyed by path.
type checksumCache struct {
	mu     sync.RWMutex
	sums   map[string]string
	modMap map[string]int64 // file mod time to detect changes
}

func newChecksumCache() *checksumCache {
	return &checksumCache{
		sums:   make(map[string]string),
		modMap: make(map[string]int64),
	}
}

// Get returns the cached checksum for the given path, recomputing if the file
// has been modified or is not yet cached.
func (c *checksumCache) Get(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	modTime := info.ModTime().UnixNano()

	c.mu.RLock()
	if sum, ok := c.sums[path]; ok {
		if c.modMap[path] == modTime {
			c.mu.RUnlock()
			return sum, nil
		}
	}
	c.mu.RUnlock()

	// Compute checksum.
	sum, err := ComputeChecksum(path)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.sums[path] = sum
	c.modMap[path] = modTime
	c.mu.Unlock()

	return sum, nil
}

// ComputeChecksum reads a file and returns its hex-encoded SHA256 digest.
func ComputeChecksum(binaryPath string) (string, error) {
	f, err := os.Open(binaryPath)
	if err != nil {
		return "", fmt.Errorf("open binary: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash binary: %w", err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// BinaryPath returns the expected filesystem path for an agent binary.
func BinaryPath(agentDir, goos, goarch string) string {
	return filepath.Join(agentDir, goos, goarch, "hob")
}
