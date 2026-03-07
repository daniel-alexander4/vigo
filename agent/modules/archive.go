package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArchiveExecutor downloads and installs binaries from URLs. It handles
// direct binaries, tarballs (.tar.gz, .tgz, .tar.bz2, .tar.xz), and zip
// archives, extracting the target binary into the destination path.
//
// Idempotency: skips download if the target path exists and either:
//   - the sha256 param matches the file's hash, or
//   - the unless command succeeds (e.g., version check)
//
// Supported params:
//   - url:      download URL (required)
//   - path:     destination file path for the binary (required)
//   - binary:   name of binary inside archive to extract (optional; if omitted,
//               treats download as a direct binary or extracts first executable)
//   - sha256:   expected hex SHA256 hash of the installed binary (optional)
//   - unless:   skip if this command exits 0 (optional, e.g., "glow --version")
//   - mode:     file permission mode as octal string (default "0755")
//   - owner:    file owner (placeholder, not enforced on non-root)
//   - state:    "present" (default) or "absent"
type ArchiveExecutor struct {
	Runner     CommandRunner
	HTTPClient *http.Client
}

func (a *ArchiveExecutor) runner() CommandRunner {
	if a.Runner != nil {
		return a.Runner
	}
	return &DefaultRunner{}
}

func (a *ArchiveExecutor) httpClient() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (a *ArchiveExecutor) Execute(ctx context.Context, params map[string]string, dryRun bool) ResourceResult {
	start := time.Now()
	path := params["path"]
	result := ResourceResult{
		ResourceType: "archive",
		ResourceName: path,
	}

	defer func() {
		result.DurationMs = int32(time.Since(start).Milliseconds())
	}()

	url := params["url"]
	if url == "" {
		result.Error = fmt.Errorf("archive executor: url param is required")
		return result
	}
	if path == "" {
		result.Error = fmt.Errorf("archive executor: path param is required")
		return result
	}

	state := params["state"]
	if state == "" {
		state = "present"
	}

	switch state {
	case "present":
		return a.handlePresent(ctx, params, dryRun, result)
	case "absent":
		return a.handleAbsent(path, dryRun, result)
	default:
		result.Error = fmt.Errorf("archive executor: unknown state %q", state)
		return result
	}
}

func (a *ArchiveExecutor) handlePresent(ctx context.Context, params map[string]string, dryRun bool, result ResourceResult) ResourceResult {
	path := params["path"]
	expectedHash := params["sha256"]
	unless := params["unless"]

	// Idempotency check 1: unless command.
	if unless != "" && !dryRun {
		parts := strings.Fields(unless)
		_, err := a.runner().Run(ctx, parts[0], parts[1:]...)
		if err == nil {
			// Command succeeded — binary is already in desired state.
			result.Action = ActionNone
			result.Changed = false
			return result
		}
	}

	// Idempotency check 2: file exists + sha256 matches.
	if _, err := os.Stat(path); err == nil {
		if expectedHash != "" {
			currentHash, hashErr := fileSHA256(path)
			if hashErr == nil && currentHash == expectedHash {
				result.Action = ActionNone
				result.Changed = false
				return result
			}
			// Hash mismatch — will re-download (update).
			result.Action = ActionUpdated
		} else if unless == "" {
			// File exists, no hash or unless to verify — assume OK.
			result.Action = ActionNone
			result.Changed = false
			return result
		} else {
			// unless failed but file exists — re-download (update).
			result.Action = ActionUpdated
		}
	} else {
		result.Action = ActionCreated
	}

	result.Changed = true
	if dryRun {
		return result
	}

	url := params["url"]
	binary := params["binary"]
	modeStr := params["mode"]
	if modeStr == "" {
		modeStr = "0755"
	}

	// Download to temp file.
	tmpDir, err := os.MkdirTemp("", "isogov-archive-*")
	if err != nil {
		result.Error = fmt.Errorf("create temp dir: %w", err)
		return result
	}
	defer os.RemoveAll(tmpDir)

	downloadPath := filepath.Join(tmpDir, "download")
	if err := a.download(ctx, url, downloadPath); err != nil {
		result.Error = fmt.Errorf("download %s: %w", url, err)
		return result
	}

	// Determine if this is an archive or direct binary.
	srcPath := downloadPath
	urlLower := strings.ToLower(url)

	if isArchive(urlLower) {
		extractDir := filepath.Join(tmpDir, "extracted")
		if err := os.MkdirAll(extractDir, 0755); err != nil {
			result.Error = fmt.Errorf("create extract dir: %w", err)
			return result
		}

		if err := a.extract(ctx, downloadPath, extractDir, urlLower); err != nil {
			result.Error = fmt.Errorf("extract archive: %w", err)
			return result
		}

		// Find the target binary in extracted files.
		found, err := findBinary(extractDir, binary, filepath.Base(path))
		if err != nil {
			result.Error = fmt.Errorf("find binary in archive: %w", err)
			return result
		}
		srcPath = found
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		result.Error = fmt.Errorf("create parent dir for %s: %w", path, err)
		return result
	}

	// Copy binary to destination (can't rename across filesystems).
	if err := copyFile(srcPath, path, modeStr); err != nil {
		result.Error = fmt.Errorf("install binary to %s: %w", path, err)
		return result
	}

	// Verify hash if specified.
	if expectedHash != "" {
		actualHash, err := fileSHA256(path)
		if err != nil {
			result.Error = fmt.Errorf("verify hash of %s: %w", path, err)
			return result
		}
		if actualHash != expectedHash {
			os.Remove(path)
			result.Error = fmt.Errorf("hash mismatch for %s: got %s, want %s", path, actualHash, expectedHash)
			return result
		}
	}

	return result
}

func (a *ArchiveExecutor) handleAbsent(path string, dryRun bool, result ResourceResult) ResourceResult {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		result.Action = ActionNone
		result.Changed = false
		return result
	}

	result.Action = ActionDeleted
	result.Changed = true

	if dryRun {
		return result
	}

	if err := os.Remove(path); err != nil {
		result.Error = fmt.Errorf("remove %s: %w", path, err)
	}
	return result
}

func (a *ArchiveExecutor) download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := a.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}

	return f.Close()
}

func (a *ArchiveExecutor) extract(ctx context.Context, archivePath, destDir, urlLower string) error {
	switch {
	case strings.HasSuffix(urlLower, ".tar.gz") || strings.HasSuffix(urlLower, ".tgz"):
		_, err := a.runner().Run(ctx, "tar", "-xzf", archivePath, "-C", destDir)
		return err
	case strings.HasSuffix(urlLower, ".tar.bz2"):
		_, err := a.runner().Run(ctx, "tar", "-xjf", archivePath, "-C", destDir)
		return err
	case strings.HasSuffix(urlLower, ".tar.xz"):
		_, err := a.runner().Run(ctx, "tar", "-xJf", archivePath, "-C", destDir)
		return err
	case strings.HasSuffix(urlLower, ".zip"):
		_, err := a.runner().Run(ctx, "unzip", "-o", archivePath, "-d", destDir)
		return err
	case strings.HasSuffix(urlLower, ".deb"):
		_, err := a.runner().Run(ctx, "dpkg", "-i", archivePath)
		return err
	case strings.HasSuffix(urlLower, ".rpm"):
		_, err := a.runner().Run(ctx, "rpm", "-i", archivePath)
		return err
	default:
		return fmt.Errorf("unsupported archive format: %s", urlLower)
	}
}

func isArchive(urlLower string) bool {
	for _, suffix := range []string{".tar.gz", ".tgz", ".tar.bz2", ".tar.xz", ".zip", ".deb", ".rpm"} {
		if strings.HasSuffix(urlLower, suffix) {
			return true
		}
	}
	return false
}

// findBinary locates a binary inside an extracted directory. If binaryName is
// specified, it looks for that exact name. Otherwise it looks for a file
// matching fallbackName, then falls back to the first executable file.
func findBinary(dir, binaryName, fallbackName string) (string, error) {
	candidates := []string{binaryName, fallbackName}

	// First pass: look for exact name matches at any depth.
	for _, name := range candidates {
		if name == "" {
			continue
		}
		var found string
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if info.Name() == name {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
		if found != "" {
			return found, nil
		}
	}

	// Second pass: find first executable file.
	var firstExec string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Mode()&0111 != 0 {
			firstExec = path
			return filepath.SkipAll
		}
		return nil
	})
	if firstExec != "" {
		return firstExec, nil
	}

	return "", fmt.Errorf("no binary found in archive (looked for %q)", binaryName)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst, modeStr string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	mode := os.FileMode(0755)
	if modeStr != "" {
		parsed, err := parseOctalMode(modeStr)
		if err == nil {
			mode = parsed
		}
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	return out.Close()
}

func parseOctalMode(s string) (os.FileMode, error) {
	var mode uint64
	_, err := fmt.Sscanf(s, "%o", &mode)
	if err != nil {
		return 0, err
	}
	return os.FileMode(mode), nil
}
