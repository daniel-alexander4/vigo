// Package traits implements custom trait executable sync, execution, and JSON
// parsing for the Hobnob agent.
package traits

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// Downloader abstracts the gRPC download call for testability.
type Downloader interface {
	DownloadCustomTrait(ctx context.Context, name string) ([]byte, error)
}

// Executor manages custom trait sync, caching, execution, and JSON parsing.
type Executor struct {
	cacheDir   string
	downloader Downloader
	timeout    time.Duration
}

// NewExecutor creates an Executor that caches executables in cacheDir and
// runs each with the given timeout.
func NewExecutor(cacheDir string, downloader Downloader, timeout time.Duration) *Executor {
	return &Executor{
		cacheDir:   cacheDir,
		downloader: downloader,
		timeout:    timeout,
	}
}

// SyncAndRun compares the manifest against locally cached executables, downloads
// new or changed ones, cleans up stale files, executes each trait with a timeout,
// and returns the merged results under a "custom" key.
func (e *Executor) SyncAndRun(ctx context.Context, manifest []*pb.CustomTraitEntry) (map[string]any, error) {
	result := make(map[string]any)
	if len(manifest) == 0 {
		return result, nil
	}

	logger := slog.Default()

	// Ensure cache directory exists.
	if err := os.MkdirAll(e.cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir %s: %w", e.cacheDir, err)
	}

	// Build set of manifest names for stale cleanup.
	manifestNames := make(map[string]bool, len(manifest))
	for _, entry := range manifest {
		manifestNames[entry.Name] = true
	}

	// Compute local checksums.
	local, err := localChecksums(e.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("read local checksums: %w", err)
	}

	// Download new or changed executables.
	for _, entry := range manifest {
		localHash, exists := local[entry.Name]
		if exists && localHash == entry.Sha256 {
			continue // up to date
		}

		data, dlErr := e.downloader.DownloadCustomTrait(ctx, entry.Name)
		if dlErr != nil {
			logger.Warn("failed to download custom trait", "name", entry.Name, "error", dlErr)
			continue
		}

		path := filepath.Join(e.cacheDir, entry.Name)
		if writeErr := os.WriteFile(path, data, 0755); writeErr != nil {
			logger.Warn("failed to write custom trait", "name", entry.Name, "error", writeErr)
			continue
		}
	}

	// Clean up stale files not in manifest.
	entries, err := os.ReadDir(e.cacheDir)
	if err == nil {
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			if !manifestNames[ent.Name()] {
				os.Remove(filepath.Join(e.cacheDir, ent.Name()))
			}
		}
	}

	// Execute each trait and collect results.
	customMap := make(map[string]any)
	for _, entry := range manifest {
		path := filepath.Join(e.cacheDir, entry.Name)
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			continue // download failed earlier
		}

		parsed, runErr := e.runOne(ctx, entry.Name, path)
		if runErr != nil {
			logger.Warn("custom trait execution failed", "name", entry.Name, "error", runErr)
			continue
		}
		customMap[entry.Name] = parsed
	}

	if len(customMap) > 0 {
		result["custom"] = customMap
	}

	return result, nil
}

// runOne executes a single trait executable with a timeout and parses
// its stdout as JSON. It uses a process group so the kill signal reaches
// child processes spawned by shell scripts.
func (e *Executor) runOne(ctx context.Context, name, path string) (map[string]any, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the entire process group so child processes are also killed.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 500 * time.Millisecond

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("execute %s: %w", name, err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse JSON from %s: %w", name, err)
	}

	return parsed, nil
}

// localChecksums reads all files in dir and returns a map of filename to SHA256
// hex digest.
func localChecksums(dir string) (map[string]string, error) {
	checksums := make(map[string]string)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return checksums, nil
		}
		return nil, err
	}

	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		checksums[ent.Name()] = computeSHA256(data)
	}

	return checksums, nil
}

// computeSHA256 returns the hex-encoded SHA256 digest of data.
func computeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
