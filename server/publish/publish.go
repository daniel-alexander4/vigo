// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package publish implements a publishing pipeline for envoy configuration.
// Operators edit .vgo files in the stockpile directory. The publish command
// validates them using the same NewConfigStore logic the server uses, then
// atomically syncs to the .live directory the server reads from.
package publish

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/modlint"
	"github.com/daniel-alexander4/vigo/server/secrets"
	"gopkg.in/yaml.v3"
)

var (
	// StockpileDir is where operators edit .vgo files.
	StockpileDir = "/srv/vigo/stockpile"

	// LiveDir is the hidden directory the server reads from.
	// Only the publish command writes here. Lives inside the stockpile so
	// the whole config tree is rooted under /srv/vigo/stockpile; the publish
	// walker skips dotfiles, so the nested layout does not cause double-load.
	LiveDir = "/srv/vigo/stockpile/.live"
)

// setTestDirs overrides paths for testing. Not exported; used via _test.go.
func setTestDirs(stockpile, live string) {
	StockpileDir = stockpile
	LiveDir = live
}

// skipDirs are directories that should not be synced from stockpile to .live.
var skipDirs = map[string]bool{
	".git": true, ".live": true, ".publish-tmp": true, ".publish-backup": true,
}

// stubProvider accepts all secret refs so validation focuses on structure.
type stubProvider struct{}

func (s *stubProvider) Get(_ context.Context, _ string) (string, error)      { return "stub", nil }
func (s *stubProvider) GetBytes(_ context.Context, _ string) ([]byte, error) { return []byte("stub"), nil }
func (s *stubProvider) Close() error                                         { return nil }

// HistoryDir is the directory for config snapshots used by rollback.
var HistoryDir = "/srv/vigo/config-history"

// Options controls publish/validation behavior.
type Options struct {
	CheckURLs  bool
	URLTimeout time.Duration // default 10s if zero
	Operator   string        // who performed this publish (defaults to $USER)
	Diff       bool          // compute and print line-level diffs
}

// Result holds summary info from a successful validation or publish.
// FileError associates a relative file path with its validation errors.
type FileError struct {
	Path     string
	Messages []string // one per validation finding
}

type Result struct {
	Modules     int
	Roles       int
	Entries     int
	Files       int      // total files synced (0 for validate-only)
	AllFiles    []string // all files examined in stockpile/
	Changed     []string // files that are new or changed vs .live
	Removed     []string // files in .live not present in stockpile
	DiffSummary string   // unified diff of changed files
	URLWarning  string   // non-fatal URL check warnings (empty if all OK)

	// stagingDir is the temp directory ValidateSource built (= stockpile +
	// linted overrides). Publish reuses it for the atomic sync to .live.
	// Empty when no staging was needed (rare error paths). Callers MUST
	// invoke Cleanup() once they're done with the result so the temp dir
	// is removed — defer is the usual pattern.
	stagingDir string

	// cleanup removes the staging dir. Always non-nil after a successful
	// ValidateSource — even when no staging dir was built it's a safe
	// no-op so callers can `defer res.Cleanup()` unconditionally.
	cleanup func()

	// ValidationErrors holds per-file validation errors from the config loader.
	// Non-nil when modules have schema/idempotency violations but the publish
	// can still proceed (the config store loads empty).
	ValidationErrors []FileError

	// RemovedModuleContent holds the YAML content of modules that were removed
	// during this publish (read from .live/ before sync). Keyed by relative path.
	// Used by the retraction feature to generate retract modules.
	RemovedModuleContent map[string][]byte `json:"-"`

	// ChangedModuleContent holds the YAML content of modules that were added
	// or modified during this publish (read from stockpile). Keyed by relative
	// path. Used by the retraction feature to pre-generate retract modules.
	ChangedModuleContent map[string][]byte `json:"-"`
}

// Cleanup removes the staging directory created during ValidateSource.
// Safe to call multiple times. Callers — including the dry-run CLI path —
// must defer this immediately after a successful ValidateSource or the
// staging tree leaks under /srv/vigo/.publish-stage-*.
func (r *Result) Cleanup() {
	if r != nil && r.cleanup != nil {
		r.cleanup()
		r.cleanup = nil
	}
}

// ValidateSource runs full config validation against the stockpile directory.
// Uses a permissive secrets stub so validation focuses on structure/idempotency.
// When opts.CheckURLs is true, http/https URLs in changed/new modules are
// checked for aliveness via HTTP HEAD.
//
// Stockpile is operator territory and is NOT modified by this function:
// modlint runs on stockpile bytes and produces canonical output as an
// in-memory override map, which is then written into a sibling staging
// directory alongside hardlinks to the unchanged files. Validation runs
// against the staging dir so the loader sees canonical bytes; Publish
// reuses the same staging dir for the atomic sync to .live. Callers MUST
// defer `res.Cleanup()` after a successful return — the staging tree
// otherwise leaks under stockpile's parent.
func ValidateSource(opts Options) (*Result, error) {
	// Normalize file permissions in stockpile/: ensure all config files are
	// owner-rw and group-readable, and directories are traversable.
	// This fixes files created by editors or cp that end up with
	// restrictive permissions (e.g., root-only 0600).
	if err := normalizeStockpilePermissions(StockpileDir); err != nil {
		return nil, fmt.Errorf("normalizing permissions: %w", err)
	}

	// Normalize whitespace in all .vgo/.yaml files: strip trailing whitespace
	// from every line. This prevents invisible formatting issues from editors
	// that add trailing spaces or mixed line endings. This DOES rewrite
	// files in stockpile, but only at the byte-stripping level — no
	// content/structure mangling.
	if err := normalizeWhitespace(StockpileDir); err != nil {
		return nil, fmt.Errorf("normalizing whitespace: %w", err)
	}

	// Initial diff (raw stockpile vs .live) identifies candidate files
	// for linting. The "true" diff (staging vs .live) is computed below
	// after staging is built, since the canonical form may match .live
	// even when the raw source differs.
	_, candidateChanged, removed, err := diffTrees(StockpileDir, LiveDir)
	if err != nil {
		return nil, fmt.Errorf("computing diff: %w", err)
	}

	// Run modlint on candidate-changed module files. Returns a map of
	// rel-path → linted bytes WITHOUT writing to stockpile.
	candidateModules := filterBySubdir(candidateChanged, "modules")
	var lintedOverrides map[string][]byte
	if len(candidateModules) > 0 {
		lintedOverrides, err = lintModules(StockpileDir, candidateModules)
		if err != nil {
			return nil, fmt.Errorf("linting modules: %w", err)
		}
	}

	// Build the staging dir (= stockpile + linted overrides via hardlinks
	// for unchanged files + fresh writes for overrides). Used for both
	// validation and the .live sync.
	stagingDir, cleanup, err := buildStagingDir(StockpileDir, lintedOverrides)
	if err != nil {
		return nil, err
	}
	// On any error from here, cleanup before returning. On success the
	// caller owns cleanup via res.Cleanup().
	cleanupOnError := func() {
		if cleanup != nil {
			cleanup()
		}
	}

	// Recompute the diff against the staging dir so .Changed reflects the
	// TRUE set of files whose canonical form differs from .live. Without
	// this, every modlint-touched file would perpetually appear in the
	// changed list (raw stockpile != linted .live).
	allFiles, changed, _, err := diffTrees(stagingDir, LiveDir)
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("computing staging diff: %w", err)
	}

	store, err := config.NewConfigStoreQuiet(stagingDir, &stubProvider{}, false)
	if err != nil {
		cleanupOnError()
		return nil, err
	}

	// Capture per-file validation errors from the config loader. Translate
	// staging-dir paths back to stockpile-relative paths so error messages
	// reference what the operator actually edits.
	var valErrors []FileError
	if reloadErr := store.LastReloadError(); reloadErr != nil {
		var ie *config.IdempotencyErrors
		if errors.As(reloadErr, &ie) {
			for _, m := range ie.Modules {
				fe := FileError{Path: rewriteStagingPath(m.Path, stagingDir, StockpileDir)}
				for _, f := range m.Findings {
					fe.Messages = append(fe.Messages, fmt.Sprintf("%s (%s): %s", f.ResourceName, f.ResourceType, f.Message))
				}
				valErrors = append(valErrors, fe)
			}
		}
	}

	var urlWarning string
	if opts.CheckURLs && len(candidateModules) > 0 {
		if err := checkURLs(StockpileDir, candidateModules, opts.URLTimeout); err != nil {
			urlWarning = err.Error()
		}
	}

	stats := store.Stats()
	return &Result{
		Modules:          stats.Modules,
		Roles:            len(store.ListRoleNames()),
		Entries:          stats.Entries,
		AllFiles:         allFiles,
		Changed:          changed,
		Removed:          removed,
		URLWarning:       urlWarning,
		ValidationErrors: valErrors,
		stagingDir:       stagingDir,
		cleanup:          cleanup,
	}, nil
}

// rewriteStagingPath translates an absolute path inside the staging dir
// to the equivalent path inside stockpile, so validation error messages
// reference files the operator can actually find. Falls through unchanged
// if the path doesn't have the staging prefix (defensive).
func rewriteStagingPath(p, stagingDir, stockpileDir string) string {
	if rel, err := filepath.Rel(stagingDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join(stockpileDir, rel)
	}
	return p
}

// FreezeUntil is set by the server when a global change freeze is active.
// If non-nil and in the future, Publish() will reject.
var FreezeUntil *time.Time

// Publish validates stockpiled configs then atomically syncs them to .live.
// After a successful sync, it snapshots .live for rollback support.
//
// Stockpile is never modified. The atomic sync runs against the staging
// directory ValidateSource produced (= stockpile + linted modlint
// overrides), so .live receives canonical bytes while the operator's
// source files stay byte-stable.
func Publish(opts Options) (*Result, error) {
	if FreezeUntil != nil && time.Now().Before(*FreezeUntil) {
		return nil, fmt.Errorf("config publish is frozen until %s", FreezeUntil.Format(time.RFC3339))
	}

	res, err := ValidateSource(opts)
	if err != nil {
		return nil, err
	}
	// Always clean up the staging dir before returning, regardless of
	// whether the sync runs (no-op early-return) or fails partway.
	defer res.Cleanup()

	if len(res.Changed) == 0 && len(res.Removed) == 0 {
		return res, nil
	}

	// Source for the publish — either the staging dir (= stockpile +
	// linted overrides) when staging was built, or stockpile itself for
	// the rare error path that bailed before staging existed.
	source := res.stagingDir
	if source == "" {
		source = StockpileDir
	}

	// Compute diff for DB attribution. Diff is staging-vs-live so paths
	// match what's actually being synced; operators see them as
	// stockpile-relative because staging mirrors stockpile structure.
	res.DiffSummary = ComputeAllDiffs(source, LiveDir, res.Changed, res.Removed)

	// Capture removed module content from .live/ before sync overwrites it.
	// The retraction feature uses this to generate retract modules.
	if removedMods := DetectRemovedModules(res.Removed); len(removedMods) > 0 {
		res.RemovedModuleContent = ReadRemovedModuleContent(LiveDir, removedMods)
	}

	// Capture added/changed module content for pre-generating retract
	// modules alongside the new modules. Reads from staging so the
	// captured bytes match what's about to land in .live.
	if changedMods := DetectChangedModules(res.Changed); len(changedMods) > 0 {
		res.ChangedModuleContent = ReadChangedModuleContent(source, changedMods)
	}

	unlockLiveDir(LiveDir)
	files, err := atomicSync(source, LiveDir)
	if err != nil {
		lockdownLiveDir(LiveDir) // re-lock even on failure
		return nil, fmt.Errorf("sync failed: %w", err)
	}
	res.Files = files

	// Lock down .live so only the publish command (running as root) can write.
	// The server reads via the vigo group. Operators cannot write directly.
	lockdownLiveDir(LiveDir)

	// Snapshot .live for rollback support.
	_, _ = SnapshotLive(LiveDir, HistoryDir, DefaultMaxVersions)

	return res, nil
}

// --- Diff ---

// diffTrees compares stockpile vs live and returns paths (relative to root)
// that are new/changed or removed.
func diffTrees(stockpile, live string) (allFiles, changed, removed []string, err error) {
	stockpileFiles, err := hashTree(stockpile)
	if err != nil {
		return nil, nil, nil, err
	}

	liveFiles, err := hashTree(live)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, nil, err
	}

	for path := range stockpileFiles {
		allFiles = append(allFiles, path)
	}
	sort.Strings(allFiles)

	for path, hash := range stockpileFiles {
		if liveHash, ok := liveFiles[path]; !ok || liveHash != hash {
			changed = append(changed, path)
		}
	}

	for path := range liveFiles {
		if _, ok := stockpileFiles[path]; !ok {
			removed = append(removed, path)
		}
	}

	return allFiles, changed, removed, nil
}

// hashTree walks a config directory and returns a map of relative path → SHA-256 hex.
// Walks the entire directory tree, skipping hidden dirs and known non-config dirs.
func hashTree(root string) (map[string]string, error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, nil
	}

	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Don't skip the root directory itself.
			if path == root {
				return nil
			}
			name := d.Name()
			// Skip hidden dirs and known non-config dirs.
			if strings.HasPrefix(name, ".") || skipDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip non-config files (binaries, temp files, etc.) but include
		// .vgo, .yaml, .yml, .tmpl, and .example files.
		if !isPublishableFile(d.Name()) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		h := sha256.Sum256(data)
		files[rel] = fmt.Sprintf("%x", h)
		return nil
	})
	return files, err
}

// isPublishableFile returns true if a file should be included in the
// publish diff and sync. Includes config files and templates.
func isPublishableFile(name string) bool {
	ext := filepath.Ext(name)
	switch ext {
	case ".vgo", ".yaml", ".yml", ".tmpl", ".conf", ".j2":
		return true
	}
	return false
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
// "modules/nginx.vgo") and returns all http/https URLs found.
func extractURLsFromFiles(sourceDir string, relPaths []string) ([]urlRef, error) {
	urlFields := map[string][]string{
		"source_package":  {"url"},
		"nonrepo_package": {"url"},
		"repository":      {"key_url"},
		"git":             {"repo"},
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
				// Skip templated URLs — they contain Go template syntax
				// that gets rendered at check-in time with real trait values.
				if strings.Contains(urlStr, "{{") {
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

// atomicSync copies the entire config tree from stockpile to live.
// Root-level files are copied directly. Subdirectories use a
// tmp+rename strategy for atomicity. Returns total files copied.
func atomicSync(stockpile, live string) (int, error) {
	// Ensure live parent exists.
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return 0, fmt.Errorf("creating live parent: %w", err)
	}
	if err := os.MkdirAll(live, 0o750); err != nil {
		return 0, fmt.Errorf("creating live dir: %w", err)
	}

	totalFiles := 0

	// Discover top-level entries in stockpile.
	stockpileEntries, err := os.ReadDir(stockpile)
	if err != nil {
		return 0, fmt.Errorf("reading stockpile dir: %w", err)
	}

	stockpileNames := make(map[string]bool)
	for _, entry := range stockpileEntries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		stockpileNames[name] = true

		if entry.IsDir() {
			n, syncErr := syncSubdir(stockpile, live, name)
			if syncErr != nil {
				return totalFiles, syncErr
			}
			totalFiles += n
		} else if isPublishableFile(name) {
			// Copy root-level files only if content changed.
			srcData, readErr := os.ReadFile(filepath.Join(stockpile, name))
			if readErr != nil {
				return totalFiles, fmt.Errorf("reading %s: %w", name, readErr)
			}
			dstPath := filepath.Join(live, name)
			dstData, _ := os.ReadFile(dstPath)
			if string(srcData) != string(dstData) {
				if writeErr := os.WriteFile(dstPath, srcData, 0o640); writeErr != nil {
					return totalFiles, fmt.Errorf("writing %s: %w", name, writeErr)
				}
				totalFiles++
			}
		}
	}

	// Remove entries in live that are not in stockpile.
	liveEntries, err := os.ReadDir(live)
	if err != nil {
		return totalFiles, nil // best-effort
	}
	for _, entry := range liveEntries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !stockpileNames[name] {
			os.RemoveAll(filepath.Join(live, name))
		}
	}

	return totalFiles, nil
}

// syncSubdir atomically replaces live/sub with contents from stockpile/sub.
func syncSubdir(stockpile, live, sub string) (int, error) {
	src := filepath.Join(stockpile, sub)
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

// lockdownLiveDir sets read-only permissions on .live and all contents.
// Directories get 0500 (owner rx), files get 0400 (owner r).
// This prevents any writes to .live outside of the publish command,
// which calls unlockLiveDir before syncing.
func lockdownLiveDir(live string) {
	_ = os.Chmod(live, 0o500)
	_ = filepath.WalkDir(live, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort
		}
		if d.IsDir() {
			_ = os.Chmod(path, 0o500)
		} else {
			_ = os.Chmod(path, 0o400)
		}
		return nil
	})
}

// unlockLiveDir temporarily sets writable permissions on .live so
// atomicSync can write. Called before sync, lockdownLiveDir after.
func unlockLiveDir(live string) {
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

// PublishFile copies a single .vgo file into stockpile/, validates the full
// config tree, and syncs only that file to .live/. The file path must be
// relative to the stockpile directory (e.g., "modules/nginx.vgo").
func PublishFile(srcPath string, opts Options) (*Result, error) {
	if FreezeUntil != nil && time.Now().Before(*FreezeUntil) {
		return nil, fmt.Errorf("config publish is frozen until %s", FreezeUntil.Format(time.RFC3339))
	}

	// Determine the relative path within the config tree.
	// Accept either an absolute path or a relative path like "modules/nginx.vgo".
	relPath := srcPath
	if filepath.IsAbs(srcPath) {
		// Check if the file is already inside stockpile/.
		if rel, err := filepath.Rel(StockpileDir, srcPath); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = rel
		} else {
			return nil, fmt.Errorf("file %s is not inside %s — copy it there first or use a relative path", srcPath, StockpileDir)
		}
	}

	// Validate the full config tree (cross-references require all files).
	store, err := config.NewConfigStore(StockpileDir, &stubProvider{}, false)
	if err != nil {
		return nil, err
	}

	// Unlock .live/ for writing, re-lock after.
	unlockLiveDir(LiveDir)
	defer lockdownLiveDir(LiveDir)

	// Ensure the target subdirectory exists in .live/.
	destPath := filepath.Join(LiveDir, relPath)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
		return nil, fmt.Errorf("creating target dir: %w", err)
	}

	// Read source and write to .live/.
	data, err := os.ReadFile(filepath.Join(StockpileDir, relPath))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", relPath, err)
	}
	if err := os.WriteFile(destPath, data, 0o640); err != nil {
		return nil, fmt.Errorf("writing %s: %w", relPath, err)
	}

	stats := store.Stats()
	return &Result{
		Modules:  stats.Modules,
		Roles:    len(store.ListRoleNames()),
		Entries:  stats.Entries,
		Files:    1,
		AllFiles: []string{relPath},
		Changed:  []string{relPath},
	}, nil
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

// normalizeStockpilePermissions walks the stockpile directory and ensures all
// config files are 0644 (owner rw, group/other r) and directories are
// 0755 (owner rwx, group/other rx). This fixes files created by editors,
// cp, or tar that end up with restrictive permissions, which would cause
// subsequent steps (whitespace normalization, config loading) to fail
// with "permission denied".
func normalizeStockpilePermissions(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != dir && (strings.HasPrefix(name, ".") || skipDirs[name]) {
				return filepath.SkipDir
			}
			info, statErr := d.Info()
			if statErr != nil {
				return statErr
			}
			if info.Mode().Perm() != 0o755 {
				return os.Chmod(path, 0o755)
			}
			return nil
		}
		if !isPublishableFile(d.Name()) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if info.Mode().Perm() != 0o644 {
			return os.Chmod(path, 0o644)
		}
		return nil
	})
}

// normalizeWhitespace walks the stockpile directory and strips trailing
// whitespace from every line of .vgo, .yaml, and .yml files. Files are
// rewritten in-place only if they changed. This prevents editor artifacts
// (trailing spaces, mixed line endings) from causing spurious diffs.
func normalizeWhitespace(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		ext := filepath.Ext(path)
		if ext != ".vgo" && ext != ".yaml" && ext != ".yml" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		text := string(data)
		changed := false

		// Strip UTF-8 BOM if present.
		if strings.HasPrefix(text, "\xef\xbb\xbf") {
			text = strings.TrimPrefix(text, "\xef\xbb\xbf")
			changed = true
		}

		// Strip leading non-ASCII bytes (e.g., ● from terminal copy-paste).
		if len(text) > 0 && text[0] > 127 {
			for i := 0; i < len(text); i++ {
				if text[i] <= 127 {
					text = text[i:]
					changed = true
					break
				}
			}
		}

		lines := strings.Split(text, "\n")
		for i, line := range lines {
			// Replace leading tabs with two spaces each.
			// YAML forbids tabs for indentation — editors and terminal
			// copy-paste often introduce them.
			if strings.ContainsRune(line, '\t') {
				// Count leading tabs and replace with spaces.
				orig := line
				for strings.HasPrefix(line, "\t") {
					line = strings.Replace(line, "\t", "  ", 1)
				}
				// Also replace any remaining tabs mid-line.
				line = strings.ReplaceAll(line, "\t", "  ")
				if line != orig {
					lines[i] = line
					changed = true
					relPath, _ := filepath.Rel(dir, path)
					slog.Warn("replaced tab characters with spaces",
						"file", relPath,
						"line", i+1,
					)
					continue
				}
			}

			trimmed := strings.TrimRight(line, " \t\r")
			if trimmed != line {
				lines[i] = trimmed
				changed = true
			}
		}

		if !changed {
			return nil
		}

		return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
	})
}

var _ secrets.Provider = (*stubProvider)(nil)

// lintModules runs the modlint pipeline on the given module files (relative
// paths like "modules/nginx.vgo") and returns the linted bytes for each file
// whose content differs from the source. It does NOT modify any file under
// baseDir — stockpile is operator territory and must stay byte-stable across
// publishes. Callers feed the returned overrides into a staging directory
// (see `buildStagingDir`) which becomes the source for both validation and
// the .live sync.
//
// Returns:
//
//   - overrides — map of relative-path → linted bytes for files that changed
//     under linting. Files whose linted output equals their source are
//     omitted (no work needed in the staging dir for those).
//   - err — non-nil if any file has validation errors modlint cannot
//     auto-fix; the operator must fix the source. Errors are accumulated
//     across all files so the caller can report them in one pass.
//
// compliance.vgo files under modules/ are directory-level compliance claim
// declarations, not module definitions — they are validated by the config
// loader instead and must be skipped here.
func lintModules(baseDir string, relPaths []string) (map[string][]byte, error) {
	overrides := make(map[string][]byte)
	var lintErrors []string

	for _, rel := range relPaths {
		if filepath.Base(rel) == "compliance.vgo" {
			continue
		}
		path := filepath.Join(baseDir, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		result := modlint.Lint(data, modlint.Options{})
		if !result.OK() {
			for _, e := range result.Errors {
				lintErrors = append(lintErrors, fmt.Sprintf("  %s: %s", rel, e))
			}
			continue // collect all errors before failing
		}

		// Only stash the override when linting actually changed bytes.
		// Files that are already in canonical form skip the staging-dir
		// write entirely (they're hardlinked from stockpile instead).
		if result.Output != string(data) {
			overrides[rel] = []byte(result.Output)
		}
	}

	if len(lintErrors) > 0 {
		return nil, fmt.Errorf("module validation failed:\n%s", strings.Join(lintErrors, "\n"))
	}
	return overrides, nil
}

// buildStagingDir produces a temp directory that mirrors stockpileDir but
// has linted bytes written in place of every entry in `overrides`. Used as
// the input to both config-store validation and the atomic sync to .live —
// keeping stockpile itself byte-stable across publishes.
//
// Files NOT in overrides are hardlinked from stockpile (cheap, O(1) per
// file, no data copy). Files in overrides are written as fresh content.
// Subdirectories are recreated with the same names so atomicSync's
// per-subdir rename strategy works against the staging dir without any
// changes.
//
// The temp dir is created as a sibling of stockpileDir so hardlinks stay
// on the same filesystem. The returned cleanup function is safe to call
// multiple times and removes the entire staging tree.
func buildStagingDir(stockpileDir string, overrides map[string][]byte) (string, func(), error) {
	parent := filepath.Dir(stockpileDir)
	staging, err := os.MkdirTemp(parent, ".publish-stage-")
	if err != nil {
		return "", nil, fmt.Errorf("creating staging dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(staging) }

	walkErr := filepath.Walk(stockpileDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(stockpileDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		// Skip dotfiles and known-non-config dirs at any depth.
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".") || skipDirs[base] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(staging, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		// File — hardlink unless this path has a linted override.
		if data, ok := overrides[rel]; ok {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return os.WriteFile(dst, data, info.Mode().Perm())
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		// Try hardlink first; fall back to copy if hardlink fails (cross-FS).
		if linkErr := os.Link(path, dst); linkErr == nil {
			return nil
		}
		// Fallback: read + write.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	})
	if walkErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("populating staging dir: %w", walkErr)
	}
	return staging, cleanup, nil
}
