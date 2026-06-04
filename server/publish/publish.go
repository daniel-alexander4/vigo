// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package publish implements a publishing pipeline for envoy configuration.
// Operators edit .vgo files in the stacks directory. The publish command
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

	"git.alexander4.io/vigo/server/config"
	"git.alexander4.io/vigo/server/modlint"
	"git.alexander4.io/vigo/server/secrets"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultStacksDir is where operators edit .vgo files — the config tree
	// root (configcrates/, usercrates/, templates/, tasks/, workflows/,
	// roles.vgo, …). It is the gitback "stacks" project. Generated output
	// (.live/) and read-only reference templates (stacks-examples/) are
	// siblings under /srv/vigo, NOT children of this tree, so the publish
	// walker rooted here sees only operator content.
	DefaultStacksDir = "/srv/vigo/stacks"

	// DefaultLiveDir is the validated output directory the server reads.
	// Only the publish command writes here.
	DefaultLiveDir = "/srv/vigo/.live"

	// DefaultStacksExamplesDir holds read-only reference templates seeded
	// from the image. Operators copy from it into DefaultStacksDir.
	DefaultStacksExamplesDir = "/srv/vigo/stacks-examples"

	// DefaultHistoryDir is the directory for config snapshots used by rollback.
	DefaultHistoryDir = "/srv/vigo/config-history"
)

// skipDirs are directories that should not be synced from the stacks tree
// to .live/. The publish walker roots at StacksDir, and .live/ +
// stacks-examples/ live outside it under /srv/vigo, so the only entries
// needed here are git/publish scratch dirs.
//
// Kept entries:
//   - .git: operators may version-control stacks in place
//   - .publish-tmp / .publish-backup: produced by atomicSync's
//     temp-and-rename strategy; never sync these into a destination
var skipDirs = map[string]bool{
	".git": true, ".publish-tmp": true, ".publish-backup": true,
}

// stubProvider accepts all secret refs so validation focuses on structure.
type stubProvider struct{}

func (s *stubProvider) Get(_ context.Context, _ string) (string, error) { return "stub", nil }
func (s *stubProvider) GetBytes(_ context.Context, _ string) ([]byte, error) {
	return []byte("stub"), nil
}
func (s *stubProvider) Close() error { return nil }

// Options controls publish/validation behavior.
type Options struct {
	CheckURLs  bool
	URLTimeout time.Duration // default 10s if zero
	Operator   string        // who performed this publish (defaults to $USER)
	Diff       bool          // compute and print line-level diffs

	// NoPrune disables the post-validation pruning of unreachable modules,
	// usercrates, and templates from .live/. Default (false) means publish
	// only carries forward files statically reachable from the carrier
	// graph (roles/case/includes/match-blocks/common.vgo/environments.vgo
	// and source: references for templates). --no-prune reproduces the
	// pre-0.65.15 verbatim mirror behavior.
	NoPrune bool

	// Paths — zero values fall back to the Default* constants. Tests
	// pass explicit per-temp-dir values; production callers leave these
	// unset and let resolvePaths() fill them in from the defaults.
	StacksDir  string
	LiveDir    string
	HistoryDir string
}

// resolvePaths returns a copy of opts with empty path fields populated
// from the Default* constants. Called at the top of every entry point
// that reads a path so internal helpers see a fully-populated struct.
func (o Options) resolvePaths() Options {
	if o.StacksDir == "" {
		o.StacksDir = DefaultStacksDir
	}
	if o.LiveDir == "" {
		o.LiveDir = DefaultLiveDir
	}
	if o.HistoryDir == "" {
		o.HistoryDir = DefaultHistoryDir
	}
	return o
}

// Result holds summary info from a successful validation or publish.
// FileError associates a relative file path with its validation errors.
type FileError struct {
	Path     string
	Messages []string // one per validation finding
}

type Result struct {
	Configcrates int
	Roles        int
	Entries      int
	Files        int      // total files synced (0 for validate-only)
	AllFiles     []string // all files examined in stacks/
	Changed      []string // files that are new or changed vs .live
	Removed      []string // files in .live not present in stacks
	DiffSummary  string   // unified diff of changed files
	URLWarning   string   // non-fatal URL check warnings (empty if all OK)

	// stagingDir is the temp directory ValidateSource built (= stacks +
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
	// or modified during this publish (read from stacks). Keyed by relative
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

// ValidateSource runs full config validation against the stacks directory.
// Uses a permissive secrets stub so validation focuses on structure/idempotency.
// When opts.CheckURLs is true, http/https URLs in changed/new modules are
// checked for aliveness via HTTP HEAD.
//
// Stacks is operator territory and is NOT modified by this function:
// modlint runs on stacks bytes and produces canonical output as an
// in-memory override map, which is then written into a sibling staging
// directory alongside hardlinks to the unchanged files. Validation runs
// against the staging dir so the loader sees canonical bytes; Publish
// reuses the same staging dir for the atomic sync to .live. Callers MUST
// defer `res.Cleanup()` after a successful return — the staging tree
// otherwise leaks under stacks's parent.
func ValidateSource(opts Options) (*Result, error) {
	opts = opts.resolvePaths()

	// Enforce canonical-filename rules across the stacks tree.
	// Fatal — blocks publish on any violation with an actionable
	// remediation message.
	if err := validateStacksLayout(opts.StacksDir); err != nil {
		return nil, err
	}

	// Normalize file permissions in stacks/: ensure all config files are
	// owner-rw and group-readable, and directories are traversable.
	// This fixes files created by editors or cp that end up with
	// restrictive permissions (e.g., root-only 0600).
	if err := normalizeStacksPermissions(opts.StacksDir); err != nil {
		return nil, fmt.Errorf("normalizing permissions: %w", err)
	}

	// Normalize whitespace in .vgo files: strip trailing whitespace,
	// replace tabs with spaces in YAML indentation, drop UTF-8 BOM.
	// This rewrites files in stacks but only at the byte-stripping
	// level — no content/structure mangling. Templates (.tmpl) are
	// left alone; whitespace there can be intentional.
	if err := normalizeWhitespace(opts.StacksDir); err != nil {
		return nil, fmt.Errorf("normalizing whitespace: %w", err)
	}

	// Format the operator's source crates in place: ensure each carries
	// modlint's `#~` standard labels (module + per-resource), while
	// preserving every hand `#` comment — prose AND commented-out attributes
	// like `#owner: 0644` — plus all content. Like normalizeWhitespace, this
	// rewrites stacks/ idempotently (only when labels actually change). It
	// runs before the stacks-vs-.live diff so a freshly-relabeled crate is
	// correctly picked up as a candidate.
	if err := formatSourceLabels(opts.StacksDir); err != nil {
		return nil, fmt.Errorf("formatting source labels: %w", err)
	}

	// Initial diff (raw stacks vs .live/) identifies candidate files for
	// linting. The "true" diff (staging vs live) is computed below after
	// staging is built, since the canonical form may match live even when
	// the raw source differs.
	_, candidateChanged, removed, err := diffTrees(opts.StacksDir, opts.LiveDir)
	if err != nil {
		return nil, fmt.Errorf("computing diff: %w", err)
	}

	// Run modlint on candidate-changed module, usercrate, and
	// scaffolding files (envoys.vgo / roles.vgo / waivers.vgo /
	// environments.vgo at any depth). Returns a map of rel-path →
	// linted bytes WITHOUT writing to stacks. Paths are
	// stacks-relative now (e.g., "configcrates/nginx.vgo").
	candidateLintable := append(
		filterBySubdir(candidateChanged, "configcrates"),
		filterUsercrates(candidateChanged)...,
	)
	candidateLintable = append(candidateLintable, filterScaffoldingFiles(candidateChanged)...)
	var lintedOverrides map[string][]byte
	if len(candidateLintable) > 0 {
		lintedOverrides, err = lintModules(opts.StacksDir, candidateLintable)
		if err != nil {
			return nil, fmt.Errorf("linting configcrates: %w", err)
		}
	}

	// Build the staging dir (= stacks + linted overrides via hardlinks
	// for unchanged files + fresh writes for overrides). Used for both
	// validation and the .live sync.
	stagingDir, cleanup, err := buildStagingDir(opts.StacksDir, lintedOverrides)
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
	// changed list (raw stacks != linted .live).
	allFiles, changed, _, err := diffTrees(stagingDir, opts.LiveDir)
	if err != nil {
		cleanupOnError()
		return nil, fmt.Errorf("computing staging diff: %w", err)
	}

	// buildStagingDir produces a flat content root at stagingDir mirroring
	// stacks/'s layout, so the loader can walk it directly.
	store, err := config.NewConfigStoreQuiet(stagingDir, &stubProvider{}, false)
	if err != nil {
		cleanupOnError()
		return nil, err
	}

	// Capture per-file validation errors from the config loader. Translate
	// staging-dir paths back to stacks-relative paths so error messages
	// reference what the operator actually edits.
	var valErrors []FileError
	if reloadErr := store.LastReloadError(); reloadErr != nil {
		var ie *config.IdempotencyErrors
		if errors.As(reloadErr, &ie) {
			for _, m := range ie.Configcrates {
				fe := FileError{Path: rewriteStagingPath(m.Path, stagingDir, opts.StacksDir)}
				for _, f := range m.Findings {
					fe.Messages = append(fe.Messages, fmt.Sprintf("%s (%s): %s", f.ResourceName, f.ResourceType, f.Message))
				}
				valErrors = append(valErrors, fe)
			}
		}
	}

	var urlWarning string
	if opts.CheckURLs && len(candidateLintable) > 0 {
		if err := checkURLs(opts.StacksDir, candidateLintable, opts.URLTimeout); err != nil {
			urlWarning = err.Error()
		}
	}

	// Carrier-graph reachability: which modules / usercrates / templates
	// no role, match block, common.vgo, or environments.vgo can route to
	// an envoy. Shared by the fleet-collision check and the prune below.
	unused := store.Unused(nil)

	// Cross-module fleet collisions (two modules managing the same
	// package or target_path). Computed here — after the loader resolved
	// the carrier graph — so unreachable library modules, which the
	// prune drops from .live anyway, don't generate publish-time noise.
	// Warnings only; never block publish.
	unreachable := make(map[string]bool)
	for _, m := range unused.UnusedModules {
		unreachable[m.SourceFile] = true
	}
	for _, u := range unused.UnusedUsercrates {
		unreachable[u.SourceFile] = true
	}
	for _, f := range fleetFindings(opts.StacksDir, candidateLintable, unreachable) {
		slog.Warn("modlint fleet finding",
			"rule", f.RuleID,
			"severity", string(f.Severity),
			"message", f.Message)
	}

	// Prune unreachable modules / usercrates / templates from staging so
	// .live/ carries only files some carrier can actually consume. The
	// recomputed diff after the prune lets res.Removed surface every
	// dropped file in the publish summary.
	if !opts.NoPrune {
		if err := pruneStaging(stagingDir, unused); err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("pruning staging: %w", err)
		}
		// Recompute the diff against the pruned staging so .Changed +
		// .Removed reflect what will actually land in .live.
		allFiles, changed, removed, err = diffTrees(stagingDir, opts.LiveDir)
		if err != nil {
			cleanupOnError()
			return nil, fmt.Errorf("computing post-prune diff: %w", err)
		}
	}

	stats := store.Stats()
	return &Result{
		Configcrates:     stats.Configcrates,
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

// pruneStaging deletes module / usercrate / template files in stagingDir
// that the carrier graph never reaches. Templates are skipped entirely
// when the reachability analysis flags any dynamic source: expression
// (see config.UnusedResult.TemplatesDynamic) — we keep every template in
// that case so a {{...}}-templated source path can't deselect a file we
// silently delete.
func pruneStaging(stagingDir string, unused *config.UnusedResult) error {
	for _, m := range unused.UnusedModules {
		if err := removeRelative(stagingDir, m.SourceFile); err != nil {
			return err
		}
	}
	for _, u := range unused.UnusedUsercrates {
		if err := removeRelative(stagingDir, u.SourceFile); err != nil {
			return err
		}
	}
	for _, t := range unused.UnusedTemplates {
		if err := removeRelative(stagingDir, t.Path); err != nil {
			return err
		}
	}
	return nil
}

func removeRelative(root, rel string) error {
	if rel == "" {
		return nil
	}
	if err := os.Remove(filepath.Join(root, rel)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", rel, err)
	}
	return nil
}

// rewriteStagingPath translates an absolute path inside the staging dir
// to the equivalent path inside stacks, so validation error messages
// reference files the operator can actually find. Falls through unchanged
// if the path doesn't have the staging prefix (defensive).
func rewriteStagingPath(p, stagingDir, stacksDir string) string {
	if rel, err := filepath.Rel(stagingDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join(stacksDir, rel)
	}
	return p
}

// Publish validates stacksd configs then atomically syncs them to .live.
// After a successful sync, it snapshots .live for rollback support.
//
// The state parameter holds operator-controlled mutable publish state
// (freeze deadline). A nil state bypasses the freeze check — used by the
// CLI, which runs in a separate process and doesn't carry freeze
// semantics across invocations. The server-side caller constructs a
// *State once at startup and threads it through.
//
// Stacks is never modified. The atomic sync runs against the staging
// directory ValidateSource produced (= stacks + linted modlint
// overrides), so .live receives canonical bytes while the operator's
// source files stay byte-stable.
func Publish(opts Options, state *State) (*Result, error) {
	if state.Frozen() {
		return nil, fmt.Errorf("config publish is frozen until %s", state.FreezeUntilTime().Format(time.RFC3339))
	}

	opts = opts.resolvePaths()

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

	// Source for the publish — the staging dir (= stacks + linted
	// overrides) when staging was built, or stacks itself for the
	// rare error path that bailed before staging existed.
	source := ""
	if res.stagingDir != "" {
		source = res.stagingDir
	} else {
		source = opts.StacksDir
	}

	// Compute diff for DB attribution. Diff is staging-vs-live so paths
	// match what's actually being synced; operators see them as
	// stacks-relative because staging mirrors stacks structure.
	res.DiffSummary = ComputeAllDiffs(source, opts.LiveDir, res.Changed, res.Removed)

	// Capture removed module content from .live/ before sync overwrites it.
	// The retraction feature uses this to generate retract modules.
	if removedMods := DetectRemovedModules(res.Removed); len(removedMods) > 0 {
		res.RemovedModuleContent = ReadRemovedModuleContent(opts.LiveDir, removedMods)
	}

	// Capture added/changed module content for pre-generating retract
	// modules alongside the new modules. Reads from staging so the
	// captured bytes match what's about to land in .live.
	if changedMods := DetectChangedModules(res.Changed); len(changedMods) > 0 {
		res.ChangedModuleContent = ReadChangedModuleContent(source, changedMods)
	}

	unlockLiveDir(opts.LiveDir)
	files, err := atomicSync(source, opts.LiveDir)
	if err != nil {
		lockdownLiveDir(opts.LiveDir) // re-lock even on failure
		return nil, fmt.Errorf("sync failed: %w", err)
	}
	res.Files = files

	// Lock down .live so only the publish command (running as root) can write.
	// The server reads via the vigo group. Operators cannot write directly.
	lockdownLiveDir(opts.LiveDir)

	// Snapshot .live for rollback support.
	_, _ = SnapshotLive(opts.LiveDir, opts.HistoryDir, DefaultMaxVersions)

	return res, nil
}

// --- Diff ---

// diffTrees compares stacks vs live and returns paths (relative to root)
// that are new/changed or removed.
func diffTrees(stacks, live string) (allFiles, changed, removed []string, err error) {
	stacksFiles, err := hashTree(stacks)
	if err != nil {
		return nil, nil, nil, err
	}

	liveFiles, err := hashTree(live)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, nil, err
	}

	for path := range stacksFiles {
		allFiles = append(allFiles, path)
	}
	sort.Strings(allFiles)

	for path, hash := range stacksFiles {
		if liveHash, ok := liveFiles[path]; !ok || liveHash != hash {
			changed = append(changed, path)
		}
	}

	for path := range liveFiles {
		if _, ok := stacksFiles[path]; !ok {
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
	case ".vgo", ".tmpl":
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

// filterUsercrates returns paths that live directly inside a usercrates/
// directory at any depth (stacks/usercrates/, stacks/<site>/usercrates/,
// etc.). Mirrors the detection logic in loader.go loadUsercrateDefs.
func filterUsercrates(paths []string) []string {
	var out []string
	for _, p := range paths {
		if modlint.IsUsercratePath(p) {
			out = append(out, p)
		}
	}
	return out
}

// scaffoldingFileBasenames are the well-known per-directory scaffolding
// files that the loader walks to assemble fleet-wide config:
// hostcrates (envoys.vgo), role definitions (roles.vgo), compliance
// waivers (waivers.vgo), and per-environment overrides
// (environments.vgo). Each gets its own modlint entry point; this set
// drives the dispatch in lintModules.
var scaffoldingFileBasenames = map[string]bool{
	"envoys.vgo":       true,
	"roles.vgo":        true,
	"waivers.vgo":      true,
	"environments.vgo": true,
}

// filterScaffoldingFiles returns paths whose basename matches a known
// scaffolding file. Used alongside filterBySubdir(modules) and
// filterUsercrates to build the full lintable set.
func filterScaffoldingFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if scaffoldingFileBasenames[filepath.Base(p)] {
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
// "configcrates/nginx.vgo") and returns all http/https URLs found.
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

// atomicSync copies the entire config tree from stacks to live.
// Root-level files are copied directly. Subdirectories use a
// tmp+rename strategy for atomicity. Returns total files copied.
func atomicSync(stacks, live string) (int, error) {
	// Ensure live parent exists.
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return 0, fmt.Errorf("creating live parent: %w", err)
	}
	if err := os.MkdirAll(live, 0o750); err != nil {
		return 0, fmt.Errorf("creating live dir: %w", err)
	}

	totalFiles := 0

	// Discover top-level entries in stacks.
	stacksEntries, err := os.ReadDir(stacks)
	if err != nil {
		return 0, fmt.Errorf("reading stacks dir: %w", err)
	}

	stacksNames := make(map[string]bool)
	for _, entry := range stacksEntries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			continue
		}
		stacksNames[name] = true

		if entry.IsDir() {
			n, syncErr := syncSubdir(stacks, live, name)
			if syncErr != nil {
				return totalFiles, syncErr
			}
			totalFiles += n
		} else if isPublishableFile(name) {
			// Copy root-level files only if content changed.
			srcData, readErr := os.ReadFile(filepath.Join(stacks, name))
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

	// Remove entries in live that are not in stacks.
	liveEntries, err := os.ReadDir(live)
	if err != nil {
		return totalFiles, nil // best-effort
	}
	for _, entry := range liveEntries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if !stacksNames[name] {
			os.RemoveAll(filepath.Join(live, name))
		}
	}

	return totalFiles, nil
}

// syncSubdir atomically replaces live/sub with contents from stacks/sub.
func syncSubdir(stacks, live, sub string) (int, error) {
	src := filepath.Join(stacks, sub)
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
			_ = os.Rename(backupDir, dst) // best-effort restore; the rename failure is already being returned
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

// PublishFile copies a single .vgo file into stacks/, validates the full
// config tree, and syncs only that file to .live/. The file path must be
// relative to the stacks directory (e.g., "configcrates/nginx.vgo").
//
// The state parameter holds operator-controlled mutable publish state
// (freeze deadline). A nil state bypasses the freeze check — see Publish.
func PublishFile(srcPath string, opts Options, state *State) (*Result, error) {
	if state.Frozen() {
		return nil, fmt.Errorf("config publish is frozen until %s", state.FreezeUntilTime().Format(time.RFC3339))
	}

	opts = opts.resolvePaths()

	// Determine the relative path within the stacks content tree.
	// Accept either an absolute path or a relative path like "configcrates/nginx.vgo".
	relPath := srcPath
	if filepath.IsAbs(srcPath) {
		if rel, err := filepath.Rel(opts.StacksDir, srcPath); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = rel
		} else {
			return nil, fmt.Errorf("file %s is not inside %s — copy it there first or use a relative path", srcPath, opts.StacksDir)
		}
	}

	// Validate the full config tree (cross-references require all files).
	store, err := config.NewConfigStore(opts.StacksDir, &stubProvider{}, false)
	if err != nil {
		return nil, err
	}

	// Unlock .live/ for writing, re-lock after.
	unlockLiveDir(opts.LiveDir)
	defer lockdownLiveDir(opts.LiveDir)

	// Ensure the target subdirectory exists in .live/.
	destPath := filepath.Join(opts.LiveDir, relPath)
	if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
		return nil, fmt.Errorf("creating target dir: %w", err)
	}

	// Read source and write to .live/.
	data, err := os.ReadFile(filepath.Join(opts.StacksDir, relPath))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", relPath, err)
	}
	if err := os.WriteFile(destPath, data, 0o640); err != nil {
		return nil, fmt.Errorf("writing %s: %w", relPath, err)
	}

	stats := store.Stats()
	return &Result{
		Configcrates: stats.Configcrates,
		Roles:        len(store.ListRoleNames()),
		Entries:      stats.Entries,
		Files:        1,
		AllFiles:     []string{relPath},
		Changed:      []string{relPath},
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

// normalizeStacksPermissions walks the stacks directory and ensures all
// config files are 0644 (owner rw, group/other r) and directories are
// 0755 (owner rwx, group/other rx). This fixes files created by editors,
// cp, or tar that end up with restrictive permissions, which would cause
// subsequent steps (whitespace normalization, config loading) to fail
// with "permission denied".
func normalizeStacksPermissions(dir string) error {
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

// normalizeWhitespace walks the stacks directory and strips trailing
// whitespace from every line of .vgo files. Files are rewritten
// in-place only if they changed. This prevents editor artifacts
// (trailing spaces, mixed line endings) from causing spurious diffs.
//
// Templates (.tmpl) are deliberately not normalized — operators may
// rely on whitespace inside Go template directives, so we leave the
// bytes alone.
// formatSourceLabels walks every .vgo crate under dir and rewrites it in place
// to carry modlint's `#~` standard labels, preserving the operator's hand `#`
// comments and all content (see modlint.FormatSource). Idempotent — a file is
// only rewritten when its labels actually change. Scaffolding, compliance, and
// invalid files are returned unchanged by FormatSource, so they're never
// written here; validation errors surface later in lintModules.
func formatSourceLabels(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".vgo" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		formatted, _ := modlint.FormatSource(rel, data, modlint.Options{})
		if string(formatted) == string(data) {
			return nil
		}
		mode := os.FileMode(0o640)
		if info, ierr := d.Info(); ierr == nil {
			mode = info.Mode().Perm()
		}
		return os.WriteFile(path, formatted, mode)
	})
}

func normalizeWhitespace(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".vgo" {
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
// paths like "configcrates/nginx.vgo") and returns the linted bytes for each file
// whose content differs from the source. It does NOT modify any file under
// baseDir — stacks is operator territory and must stay byte-stable across
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
// compliance.vgo files under configcrates/ are directory-level compliance claim
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

		// Dispatch to the right modlint entry point by filename — the
		// canonical "lint a file by its kind" path, shared with
		// `vigocli config validate` so the two never disagree. StripComments
		// yields the clean machine-facing form for `.live/`: the server reads
		// `.live/` and ignores comments, so it carries no `#~` labels and no
		// operator `#` comments. `content:` data is preserved. The labels live
		// in the operator's source (formatted in place by formatSourceLabels).
		result := modlint.LintFile(rel, data, modlint.Options{StripComments: true})

		if !result.OK() {
			for _, e := range result.Errors {
				lintErrors = append(lintErrors, fmt.Sprintf("  %s: %s", rel, e))
			}
			continue // collect all errors before failing
		}

		// The stripped form always differs from the commented source, so it's
		// always stashed as the `.live/` override; the later staging-vs-live
		// diff limits the actual sync to files whose stripped bytes changed.
		overrides[rel] = []byte(result.Output)
	}

	if len(lintErrors) > 0 {
		return nil, fmt.Errorf("configcrate validation failed:\n%s", strings.Join(lintErrors, "\n"))
	}

	return overrides, nil
}

// fleetFindings runs the fleet-level cross-module checks and returns
// any findings — the caller logs them; they never block publish.
//
// Configcrates and usercrates that no carrier can route to a real envoy
// are dropped via the unreachable set (built from ConfigStore.Unused,
// the same reachability the .live/ prune uses). A collision among
// files none of which can reach an envoy can't physically happen, so
// reporting it would just be publish-time noise.
func fleetFindings(baseDir string, relPaths []string, unreachable map[string]bool) []modlint.Finding {
	files := make([]modlint.FleetFile, 0, len(relPaths))
	for _, rel := range relPaths {
		// Skip non-module file types — LintFleet only inspects modules.
		if scaffoldingFileBasenames[filepath.Base(rel)] {
			continue
		}
		if filepath.Base(rel) == "compliance.vgo" {
			continue
		}
		// Skip configcrates/usercrates no carrier can reach — the prune
		// drops them from .live anyway, so they can never collide on a
		// real envoy.
		if unreachable[rel] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(baseDir, rel))
		if err != nil {
			continue
		}
		files = append(files, modlint.FleetFile{Path: rel, Data: data})
	}
	return modlint.LintFleet(files)
}

// buildStagingDir produces a temp directory whose contents mirror
// stacksDir flat at the top level, with linted bytes written in
// place of every entry in `overrides`. The loader walks the staging
// dir as a content root directly — there is no inner stacks/ wrapper
// (and no inner stacks/ in .live/ either; the two layouts are
// symmetric).
//
// Files NOT in overrides are hardlinked from stacks (cheap, O(1) per
// file, no data copy). Files in overrides are written as fresh content.
// Subdirectories are recreated with the same names so atomicSync's
// per-subdir rename strategy works directly against the staging dir.
//
// The temp dir is created as a sibling of stacksDir's parent so
// hardlinks stay on the same filesystem. The returned cleanup function
// is safe to call multiple times and removes the entire staging tree.
func buildStagingDir(stacksDir string, overrides map[string][]byte) (string, func(), error) {
	parent := filepath.Dir(stacksDir)
	staging, err := os.MkdirTemp(parent, ".publish-stage-")
	if err != nil {
		return "", nil, fmt.Errorf("creating staging dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(staging) }

	walkErr := filepath.Walk(stacksDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(stacksDir, path)
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
