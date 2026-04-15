// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package publish

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- helpers ---

// withTestDirs temporarily overrides StockpileDir and LiveDir for testing,
// restoring them when the test completes.
func withTestDirs(t *testing.T) (stockpile, live string) {
	t.Helper()
	stockpile = t.TempDir()
	live = filepath.Join(t.TempDir(), ".live")

	origStockpile := StockpileDir
	origLive := LiveDir

	// Override package-level "constants" via a test seam.
	setTestDirs(stockpile, live)

	t.Cleanup(func() {
		unlockLiveDir(live) // ensure temp dir cleanup can remove read-only files
		setTestDirs(origStockpile, origLive)
	})

	for _, sub := range []string{"modules", "roles", "envoys"} {
		if err := os.MkdirAll(filepath.Join(stockpile, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return stockpile, live
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeValidConfig(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(dir, "envoys", "web.vgo"), `
envoys:
  - match: "web.example.com"
    modules: [nginx]
`)
}

func noURLOpts() Options {
	return Options{CheckURLs: false}
}

// --- Structural validation tests ---

func TestValidateSource_Valid(t *testing.T) {
	stockpile, _ := withTestDirs(t)
	writeValidConfig(t, stockpile)

	res, err := ValidateSource(noURLOpts())
	if err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
	if res.Modules != 1 {
		t.Errorf("expected 1 module, got %d", res.Modules)
	}
	if res.Entries != 1 {
		t.Errorf("expected 1 entry, got %d", res.Entries)
	}
}

func TestValidateSource_MissingModuleRef(t *testing.T) {
	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(stockpile, "envoys", "web.vgo"), `
envoys:
  - match: "web.example.com"
    modules: [nginx, nonexistent]
`)

	_, err := ValidateSource(noURLOpts())
	if err == nil {
		t.Fatal("expected error for missing module reference")
	}
}

func TestValidateSource_IdempotencyError(t *testing.T) {
	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "risky.vgo"), `
name: risky
resources:
  - name: run-something
    type: exec
    command: "echo hello"
`)
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [risky]
`)

	_, err := ValidateSource(noURLOpts())
	if err == nil {
		t.Fatal("expected idempotency error for unguarded exec")
	}
}

func TestPublish_SyncsFiles(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeValidConfig(t, stockpile)

	res, err := Publish(noURLOpts())
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if res.Files == 0 {
		t.Error("expected files to be synced")
	}

	if _, err := os.Stat(filepath.Join(live, "modules", "nginx.vgo")); err != nil {
		t.Error("nginx.vgo not synced to live modules/")
	}
	if _, err := os.Stat(filepath.Join(live, "envoys", "web.vgo")); err != nil {
		t.Error("web.vgo not synced to live envoys/")
	}
}

// TestPublish_StockpileBytesUnchanged is the regression test for the
// "modlint mangled my source" complaint. Before this fix, every publish
// ran modlint and wrote the linted output BACK to the stockpile path,
// reformatting whatever the operator had written. After the fix, publish
// builds an in-memory linted-overrides map, materializes it into a
// staging dir alongside hardlinks for unchanged files, and syncs the
// staging dir to .live — leaving stockpile byte-stable.
//
// The test writes a module with intentional non-canonical formatting
// (compact flow-style sequences, hand-chosen key order), runs Publish,
// and asserts:
//
//  1. The bytes in stockpile/modules/users/dan.vgo are EXACTLY what was
//     written, byte for byte. No modlint mangling.
//  2. The bytes in .live/modules/users/dan.vgo are the canonical (linted)
//     form, including the flow-style preservation from Fix B.
func TestPublish_StockpileBytesUnchanged(t *testing.T) {
	stockpile, live := withTestDirs(t)

	// Hand-written compact form. Flow-style sequences, non-alphabetic key
	// order. Modlint must not "fix" any of this in stockpile.
	original := `name: users/dan
resources:
  - name: dan
    type: user
    username: dan
    groups:
      danlap:      [docker]
      default:     []
      girlslaptop: [docker]
      plex:        [plex]
      raspberrypi: [docker]
    shell: /bin/bash
    state: present
`
	srcPath := filepath.Join(stockpile, "modules", "users", "dan.vgo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, srcPath, original)
	writeFile(t, filepath.Join(stockpile, "envoys", "girlslaptop.vgo"), `
envoys:
  - match: "girlslaptop"
    modules: [users/dan]
`)

	res, err := Publish(noURLOpts())
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if res.Files == 0 {
		t.Error("expected files to be synced to .live")
	}

	// Stockpile must be byte-identical to what we wrote.
	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("re-reading stockpile dan.vgo: %v", err)
	}
	if string(got) != original {
		t.Errorf("stockpile dan.vgo was modified by publish.\nwant:\n%s\n\ngot:\n%s", original, string(got))
	}

	// .live must contain the linted bytes — at minimum, the canonical
	// Lint output. Round-trip stability is enough: if we lint the
	// stockpile source ourselves and compare, .live should match.
	livePath := filepath.Join(live, "modules", "users", "dan.vgo")
	liveBytes, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatalf(".live dan.vgo missing: %v", err)
	}
	// Sanity check: .live should preserve the flow style our Fix B added.
	if !contains(string(liveBytes), "girlslaptop: [docker]") {
		t.Errorf(".live dan.vgo missing flow-style groups entry; got:\n%s", string(liveBytes))
	}
	if !contains(string(liveBytes), "default: []") {
		t.Errorf(".live dan.vgo missing default empty list; got:\n%s", string(liveBytes))
	}
}

// contains is a stdlib-free strings.Contains used to keep the test file
// import list short.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestPublish_RemovesFilesNotInProposed(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeValidConfig(t, stockpile)

	// Pre-populate live with an extra file.
	os.MkdirAll(filepath.Join(live, "modules"), 0o755)
	writeFile(t, filepath.Join(live, "modules", "old.vgo"), "old content")

	_, err := Publish(noURLOpts())
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(live, "modules", "old.vgo")); !os.IsNotExist(err) {
		t.Error("old.vgo should have been removed from live")
	}
}

func TestPublish_LeavesLiveOnValidationFailure(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "envoys", "bad.vgo"), `
envoys:
  - match: "bad.example.com"
    modules: [nonexistent]
`)
	os.MkdirAll(filepath.Join(live, "modules"), 0o755)
	writeFile(t, filepath.Join(live, "modules", "existing.vgo"), "keep me")

	_, err := Publish(noURLOpts())
	if err == nil {
		t.Fatal("expected validation failure")
	}

	data, readErr := os.ReadFile(filepath.Join(live, "modules", "existing.vgo"))
	if readErr != nil {
		t.Fatal("live file should still exist")
	}
	if string(data) != "keep me" {
		t.Error("live file content was modified")
	}
}

func TestPublish_SkipValidationAllowsCustomType(t *testing.T) {
	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "custom.vgo"), `
name: custom
resources:
  - name: deploy-app
    type: capistrano
    skip_validation: true
    command: "cap deploy"
`)
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [custom]
`)

	res, err := Publish(Options{})
	if err != nil {
		t.Fatalf("expected skip_validation publish to succeed, got: %v", err)
	}
	if res.Files == 0 {
		t.Error("expected files to be synced")
	}
}

func TestPublish_SkipsMissingProposedSubdirs(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeValidConfig(t, stockpile)

	os.MkdirAll(filepath.Join(live, "vars"), 0o755)
	writeFile(t, filepath.Join(live, "vars", "old.yaml"), "old")

	_, err := Publish(noURLOpts())
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(live, "vars", "old.yaml")); !os.IsNotExist(err) {
		t.Error("vars/old.yaml should have been removed")
	}
}

func TestPublish_NothingToPublish(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeValidConfig(t, stockpile)

	// First publish.
	_, err := Publish(noURLOpts())
	if err != nil {
		t.Fatalf("first publish failed: %v", err)
	}

	// Second publish — nothing changed.
	res, err := Publish(noURLOpts())
	if err != nil {
		t.Fatalf("second publish failed: %v", err)
	}
	if res.Files != 0 {
		t.Errorf("expected 0 files synced on no-change publish, got %d", res.Files)
	}
	if len(res.Changed) != 0 {
		t.Errorf("expected 0 changed, got %d", len(res.Changed))
	}
	_ = live
}

func TestDiff_DetectsChanges(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeValidConfig(t, stockpile)

	// Populate live with different content.
	os.MkdirAll(filepath.Join(live, "modules"), 0o755)
	writeFile(t, filepath.Join(live, "modules", "nginx.vgo"), "old content")
	os.MkdirAll(filepath.Join(live, "modules"), 0o755)
	writeFile(t, filepath.Join(live, "modules", "deleted.vgo"), "will be removed")

	_, changed, removed, err := diffTrees(stockpile, live)
	if err != nil {
		t.Fatalf("diff failed: %v", err)
	}

	if len(changed) < 1 {
		t.Error("expected at least 1 changed file")
	}
	if len(removed) < 1 {
		t.Error("expected at least 1 removed file")
	}
}

func TestDiff_EmptyLive(t *testing.T) {
	stockpile, live := withTestDirs(t)
	writeValidConfig(t, stockpile)

	// .live doesn't exist yet — everything is new.
	_, changed, removed, err := diffTrees(stockpile, live)
	if err != nil {
		t.Fatalf("diff failed: %v", err)
	}
	if len(changed) == 0 {
		t.Error("expected all files to be changed on first publish")
	}
	if len(removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(removed))
	}
}

// --- URL aliveness tests ---

func TestCheckURLs_ReachableURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "download.vgo"), fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    url: "%s/archive.tar.gz"
    target_path: "/opt/app"
`, srv.URL))
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [download]
`)

	_, err := ValidateSource(Options{CheckURLs: true, URLTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("expected reachable URL to pass, got: %v", err)
	}
}

func TestCheckURLs_404Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "download.vgo"), fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    url: "%s/missing.tar.gz"
    target_path: "/opt/app"
`, srv.URL))
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [download]
`)

	res, err := ValidateSource(Options{CheckURLs: true, URLTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("URL check should warn, not fail: %v", err)
	}
	if res.URLWarning == "" {
		t.Fatal("expected URL warning for 404")
	}
}

func TestCheckURLs_SkipsNonHTTPURLs(t *testing.T) {
	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "gitmod.vgo"), `
name: gitmod
resources:
  - name: clone-repo
    type: git
    repo: "git@github.com:example/repo.git"
    path: "/opt/repo"
`)
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [gitmod]
`)

	_, err := ValidateSource(Options{CheckURLs: true, URLTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("expected SSH git URL to be skipped, got: %v", err)
	}
}

func TestCheckURLs_SkippedWhenDisabled(t *testing.T) {
	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "download.vgo"), `
name: download
resources:
  - name: get-archive
    type: source_package
    url: "http://nonexistent.invalid/file.tar.gz"
    target_path: "/opt/app"
`)
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [download]
`)

	_, err := ValidateSource(Options{CheckURLs: false})
	if err != nil {
		t.Fatalf("expected skip-url-check to pass, got: %v", err)
	}
}

func TestCheckURLs_HEAD405FallsBackToGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "download.vgo"), fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    url: "%s/archive.tar.gz"
    target_path: "/opt/app"
`, srv.URL))
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [download]
`)

	_, err := ValidateSource(Options{CheckURLs: true, URLTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("expected HEAD→GET fallback to pass, got: %v", err)
	}
}

func TestCheckURLs_KeyURLChecked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	stockpile, _ := withTestDirs(t)
	writeFile(t, filepath.Join(stockpile, "modules", "repo.vgo"), fmt.Sprintf(`
name: repo
resources:
  - name: add-repo
    type: repository
    repo: "deb https://example.com/apt stable main"
    key_url: "%s/gpg.key"
`, srv.URL))
	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [repo]
`)

	res, err := ValidateSource(Options{CheckURLs: true, URLTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("URL check should warn, not fail: %v", err)
	}
	if res.URLWarning == "" {
		t.Fatal("expected URL warning for 404 key_url")
	}
}

func TestCheckURLs_OnlyChecksChangedModules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	stockpile, live := withTestDirs(t)

	// Module with a bad URL — but it's already in live unchanged.
	badModule := fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    url: "%s/missing.tar.gz"
    target_path: "/opt/app"
`, srv.URL)
	writeFile(t, filepath.Join(stockpile, "modules", "download.vgo"), badModule)
	os.MkdirAll(filepath.Join(live, "modules"), 0o755)
	writeFile(t, filepath.Join(live, "modules", "download.vgo"), badModule) // same content

	writeFile(t, filepath.Join(stockpile, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [download]
`)
	os.MkdirAll(filepath.Join(live, "envoys"), 0o755)
	writeFile(t, filepath.Join(live, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    modules: [download]
`)

	// URL check enabled but module hasn't changed — should pass.
	_, err := ValidateSource(Options{CheckURLs: true, URLTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("expected unchanged module to skip URL check, got: %v", err)
	}
}
