// Package update implements the agent self-update mechanism: version checking
// against the server, binary download with SHA256 verification, atomic
// replacement, and process restart via exec.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// ExecFn is a function that replaces the current process with a new binary.
// It matches the signature of syscall.Exec. Tests can inject a no-op.
type ExecFn func(binary string, args []string, env []string) error

// VersionInfo holds the server's response about the latest agent version.
type VersionInfo struct {
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	DownloadURL string `json:"download_url"`
}

// CheckForUpdate queries the server's /api/v1/agent/latest endpoint to
// determine if a newer agent version is available. Returns nil if the current
// version matches or the server reports no binaries available.
func CheckForUpdate(ctx context.Context, serverAddr, currentVersion, goos, goarch string) (*VersionInfo, error) {
	url := fmt.Sprintf("%s/api/v1/agent/latest?os=%s&arch=%s", serverAddr, goos, goarch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("version check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("version check returned status %d", resp.StatusCode)
	}

	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode version response: %w", err)
	}

	// No update if version is empty (no binaries available) or matches current.
	if info.Version == "" || info.Version == currentVersion {
		return nil, nil
	}

	// Build full download URL if relative.
	if len(info.DownloadURL) > 0 && info.DownloadURL[0] == '/' {
		info.DownloadURL = serverAddr + info.DownloadURL
	}

	return &info, nil
}

// Apply downloads the new agent binary, verifies its SHA256 checksum, atomically
// replaces the current binary, and calls execFn to restart the process.
//
// If execFn is nil, the function returns after the atomic rename (useful for
// environments where exec is not appropriate). The caller should provide
// syscall.Exec in production.
//
// On any error before exec, the original binary is left untouched and any
// temporary files are cleaned up.
func Apply(info *VersionInfo, currentBinary string, httpClient *http.Client, execFn ExecFn) error {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// Download to temp file in same directory (same filesystem for atomic rename).
	dir := filepath.Dir(currentBinary)
	tmpFile, err := os.CreateTemp(dir, ".hob-update-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Ensure cleanup on any error path.
	defer func() {
		// Only remove if file still exists (not renamed).
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			os.Remove(tmpPath)
		}
	}()

	// Download the binary.
	resp, err := httpClient.Get(info.DownloadURL)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download binary: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	// Stream download while computing SHA256.
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, h), resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("download binary: %w", err)
	}
	tmpFile.Close()

	// Verify checksum.
	computed := fmt.Sprintf("%x", h.Sum(nil))
	if computed != info.SHA256 {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", info.SHA256, computed)
	}

	// Set executable permissions.
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, currentBinary); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// Exec replaces the current process. If execFn is nil, skip (test mode).
	if execFn != nil {
		return execFn(currentBinary, os.Args, os.Environ())
	}

	return nil
}
