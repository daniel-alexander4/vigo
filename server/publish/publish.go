// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package publish implements a staging pipeline for thrall configuration.
// Operators edit .hnb files in the proposed directory. The publish command
// validates them using the same NewConfigStore logic the server uses, then
// atomically syncs to the .live directory the server reads from.
package publish

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobnob/hobnob/server/config"
	"github.com/hobnob/hobnob/server/secrets"
	"gopkg.in/yaml.v3"
)

var (
	// ProposedDir is where operators edit .hnb files.
	ProposedDir = "/srv/hobnob/hnb/proposed"

	// LiveDir is the hidden directory the server reads from.
	// Only the publish command writes here.
	LiveDir = "/srv/hobnob/hnb/.live"
)

// setTestDirs overrides paths for testing. Not exported; used via _test.go.
func setTestDirs(proposed, live string) {
	ProposedDir = proposed
	LiveDir = live
}

// Subdirectories that make up a config tree.
var configDirs = []string{"modules", "roles", "thralls", "templates", "vars"}

// stubProvider accepts all secret refs so validation focuses on structure.
type stubProvider struct{}

func (s *stubProvider) Get(_ context.Context, _ string) (string, error)      { return "stub", nil }
func (s *stubProvider) GetBytes(_ context.Context, _ string) ([]byte, error) { return []byte("stub"), nil }
func (s *stubProvider) Close() error                                         { return nil }

// HistoryDir is the directory for config snapshots used by rollback.
var HistoryDir = "/srv/hobnob/config-history"

// Options controls publish/validation behavior.
type Options struct {
	CheckURLs  bool
	URLTimeout time.Duration // default 10s if zero
	Operator   string        // who performed this publish (defaults to $USER)
	Diff       bool          // compute and print line-level diffs
}

// Result holds summary info from a successful validation or publish.
type Result struct {
	Modules     int
	Roles       int
	Entries     int
	Files       int      // total files synced (0 for validate-only)
	AllFiles    []string // all files examined in proposed/
	Changed     []string // files that are new or changed vs .live
	Removed     []string // files in .live not present in proposed
	DiffSummary string   // unified diff of changed files
}

// ValidateSource runs full config validation against the proposed directory.
// Uses a permissive secrets stub so validation focuses on structure/idempotency.
// When opts.CheckURLs is true, http/https URLs in changed/new modules are
// checked for aliveness via HTTP HEAD.
func ValidateSource(opts Options) (*Result, error) {
	store, err := config.NewConfigStore(ProposedDir, &stubProvider{}, false)
	if err != nil {
		return nil, err
	}

	allFiles, changed, removed, err := diffTrees(ProposedDir, LiveDir)
	if err != nil {
		return nil, fmt.Errorf("computing diff: %w", err)
	}

	if opts.CheckURLs {
		changedModules := filterBySubdir(changed, "modules")
		if len(changedModules) > 0 {
			if err := checkURLs(ProposedDir, changedModules, opts.URLTimeout); err != nil {
				return nil, err
			}
		}
	}

	stats := store.Stats()
	return &Result{
		Modules:  stats.Modules,
		Roles:    len(store.ListRoleNames()),
		Entries:  stats.Entries,
		AllFiles: allFiles,
		Changed:  changed,
		Removed:  removed,
	}, nil
}

// FreezeUntil is set by the server when a global change freeze is active.
// If non-nil and in the future, Publish() will reject.
var FreezeUntil *time.Time

// Publish validates proposed configs then atomically syncs them to .live.
// After a successful sync, it snapshots .live for rollback support.
func Publish(opts Options) (*Result, error) {
	if FreezeUntil != nil && time.Now().Before(*FreezeUntil) {
		return nil, fmt.Errorf("config publish is frozen until %s", FreezeUntil.Format(time.RFC3339))
	}

	res, err := ValidateSource(opts)
	if err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	if len(res.Changed) == 0 && len(res.Removed) == 0 {
		return res, nil
	}

	// Compute diff before sync (comparing proposed vs current live).
	if opts.Diff || true { // always compute for DB attribution
		res.DiffSummary = ComputeAllDiffs(ProposedDir, LiveDir, res.Changed, res.Removed)
	}

	files, err := atomicSync(ProposedDir, LiveDir)
	if err != nil {
		return nil, fmt.Errorf("sync failed: %w", err)
	}
	res.Files = files

	// Lock down .live so only the publish command (running as root) can write.
	// The server reads via the hobnob group. Operators cannot write directly.
	lockdownLiveDir(LiveDir)

	// Snapshot .live for rollback support.
	_, _ = SnapshotLive(LiveDir, HistoryDir, DefaultMaxVersions)

	return res, nil
}

// --- Diff ---

// diffTrees compares proposed vs live and returns paths (relative to root)
// that are new/changed or removed.
func diffTrees(proposed, live string) (allFiles, changed, removed []string, err error) {
	proposedFiles, err := hashTree(proposed)
	if err != nil {
		return nil, nil, nil, err
	}

	liveFiles, err := hashTree(live)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, nil, err
	}

	for path := range proposedFiles {
		allFiles = append(allFiles, path)
	}
	sort.Strings(allFiles)

	for path, hash := range proposedFiles {
		if liveHash, ok := liveFiles[path]; !ok || liveHash != hash {
			changed = append(changed, path)
		}
	}

	for path := range liveFiles {
		if _, ok := proposedFiles[path]; !ok {
			removed = append(removed, path)
		}
	}

	return allFiles, changed, removed, nil
}

// hashTree walks a config directory and returns a map of relative path → SHA-256 hex.
func hashTree(root string) (map[string]string, error) {
	files := make(map[string]string)

	for _, sub := range configDirs {
		dir := filepath.Join(root, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			h := sha256.Sum256(data)
			files[rel] = fmt.Sprintf("%x", h)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	return files, nil
}

// filterBySubdir returns only paths whose first component matches subdir.
func filterBySubdir(paths []string, subdir string) []string {
	prefix := subdir + string(os.PathSeparator)
	var out []string
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// --- URL aliveness checking ---

// urlRef is a URL found in a config file with context for error messages.
type urlRef struct {
	url      string
	module   string
	resource string
	field    string
}

// checkURLs extracts http/https URLs from the specified changed module files
// and verifies they respond without 4xx/5xx errors.
func checkURLs(sourceDir string, changedModulePaths []string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	urls, err := extractURLsFromFiles(sourceDir, changedModulePaths)
	if err != nil {
		return fmt.Errorf("extracting URLs: %w", err)
	}
	if len(urls) == 0 {
		return nil
	}

	type result struct {
		ref urlRef
		err error
	}
	results := make([]result, len(urls))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	client := &http.Client{Timeout: timeout}

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, ref urlRef) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = result{ref: ref, err: probeURL(client, ref.url)}
		}(i, u)
	}
	wg.Wait()

	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("  %s/%s: %s %q: %v",
				r.ref.module, r.ref.resource, r.ref.field, r.ref.url, r.err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("unreachable URLs:\n%s", strings.Join(errs, "\n"))
	}
	return nil
}

// probeURL does an HTTP HEAD (falling back to GET on 405) and returns an error
// for 4xx/5xx or connection failures.
func probeURL(client *http.Client, rawURL string) error {
	resp, err := client.Head(rawURL)
	if err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", "bytes=0-0")
		resp, err = client.Do(req)
		if err != nil {
			return fmt.Errorf("connection failed: %w", err)
		}
		resp.Body.Close()
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// moduleFile is a minimal struct for parsing module YAML to extract URLs.
type moduleFile struct {
	Resources []struct {
		Name  string         `yaml:"name"`
		Type  string         `yaml:"type"`
		Attrs map[string]any `yaml:",inline"`
	} `yaml:"resources"`
	Name string `yaml:"name"`
}

// extractURLsFromFiles parses the given module files (relative paths like
// "modules/nginx.hnb") and returns all http/https URLs found.
func extractURLsFromFiles(sourceDir string, relPaths []string) ([]urlRef, error) {
	urlFields := map[string][]string{
		"archive":    {"url"},
		"repository": {"key_url"},
		"git":        {"repo"},
	}

	var refs []urlRef
	for _, rel := range relPaths {
		path := filepath.Join(sourceDir, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			continue // file might not be a module; skip
		}

		var mf moduleFile
		if err := yaml.Unmarshal(data, &mf); err != nil {
			continue // structural errors caught by NewConfigStore
		}

		for _, res := range mf.Resources {
			fields, ok := urlFields[res.Type]
			if !ok {
				continue
			}
			for _, field := range fields {
				v, ok := res.Attrs[field]
				if !ok {
					continue
				}
				urlStr, ok := v.(string)
				if !ok || urlStr == "" {
					continue
				}
				if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
					continue
				}
				refs = append(refs, urlRef{
					url:      urlStr,
					module:   mf.Name,
					resource: res.Name,
					field:    field,
				})
			}
		}
	}
	return refs, nil
}

// --- Atomic sync ---

// atomicSync copies each subdirectory from proposed to live using a
// tmp+rename strategy. Returns total files copied.
func atomicSync(proposed, live string) (int, error) {
	// Ensure live parent exists.
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return 0, fmt.Errorf("creating live parent: %w", err)
	}
	if err := os.MkdirAll(live, 0o750); err != nil {
		return 0, fmt.Errorf("creating live dir: %w", err)
	}

	totalFiles := 0

	for _, sub := range configDirs {
		src := filepath.Join(proposed, sub)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			// Proposed doesn't have this subdir — remove it from live if present.
			dst := filepath.Join(live, sub)
			if _, statErr := os.Stat(dst); statErr == nil {
				if err := os.RemoveAll(dst); err != nil {
					return totalFiles, fmt.Errorf("remove %s: %w", sub, err)
				}
			}
			continue
		}

		n, err := syncSubdir(proposed, live, sub)
		if err != nil {
			return totalFiles, err
		}
		totalFiles += n
	}

	return totalFiles, nil
}

// syncSubdir atomically replaces live/sub with contents from proposed/sub.
func syncSubdir(proposed, live, sub string) (int, error) {
	src := filepath.Join(proposed, sub)
	dst := filepath.Join(live, sub)

	tmpDir, err := os.MkdirTemp(live, ".publish-tmp-"+sub+"-")
	if err != nil {
		return 0, fmt.Errorf("create temp dir for %s: %w", sub, err)
	}

	files, err := copyDir(src, tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return 0, fmt.Errorf("copy %s: %w", sub, err)
	}

	var backupDir string
	if _, statErr := os.Stat(dst); statErr == nil {
		backupDir, err = os.MkdirTemp(live, ".publish-backup-"+sub+"-")
		if err != nil {
			os.RemoveAll(tmpDir)
			return 0, fmt.Errorf("create backup dir for %s: %w", sub, err)
		}
		os.Remove(backupDir)
		if err := os.Rename(dst, backupDir); err != nil {
			os.RemoveAll(tmpDir)
			return 0, fmt.Errorf("backup %s: %w", sub, err)
		}
	}

	if err := os.Rename(tmpDir, dst); err != nil {
		if backupDir != "" {
			os.Rename(backupDir, dst)
		}
		os.RemoveAll(tmpDir)
		return 0, fmt.Errorf("rename %s: %w", sub, err)
	}

	if backupDir != "" {
		os.RemoveAll(backupDir)
	}

	return files, nil
}

// copyDir recursively copies all files from src to dst.
func copyDir(src, dst string) (int, error) {
	count := 0
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		count++
		return os.WriteFile(target, data, 0o640)
	})
	return count, err
}

// lockdownLiveDir sets restrictive permissions on .live and all contents.
// Directories get 0750 (owner rwx, group rx), files get 0640 (owner rw, group r).
// This prevents operators from bypassing config publish by writing directly.
func lockdownLiveDir(live string) {
	_ = os.Chmod(live, 0o750)
	_ = filepath.WalkDir(live, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort
		}
		if d.IsDir() {
			_ = os.Chmod(path, 0o750)
		} else {
			_ = os.Chmod(path, 0o640)
		}
		return nil
	})
}

// CheckLivePermissions checks that .live is not world-writable. Returns a
// warning message if permissions are too open, or empty string if OK.
// Called at server startup for early detection of misconfigured permissions.
func CheckLivePermissions(live string) string {
	info, err := os.Stat(live)
	if err != nil {
		return "" // doesn't exist yet — will be created by first publish
	}
	mode := info.Mode().Perm()
	if mode&0o002 != 0 {
		return fmt.Sprintf(".live directory %s is world-writable (mode %04o) — run 'chmod 750 %s' to restrict access", live, mode, live)
	}
	if mode&0o020 != 0 && mode&0o007 != 0 {
		return fmt.Sprintf(".live directory %s is group and world accessible (mode %04o) — run 'chmod 750 %s'", live, mode, live)
	}
	return ""
}

var _ secrets.Provider = (*stubProvider)(nil)
