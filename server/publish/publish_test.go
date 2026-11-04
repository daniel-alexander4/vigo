// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- helpers ---

// testEnv holds the temp dirs (stacks/live) for a test along
// with the Options pre-populated to point at them. Tests pass opts
// through ValidateSource/Publish/PublishFile directly — the publish
// package holds no path globals to mutate.
type testEnv struct {
	stacks string
	live   string
	opts   Options
}

// withTestDirs builds a per-test temp dir tree and returns a testEnv
// holding (a) the stacks/live paths and (b) an Options struct
// pre-populated with those paths. Tests should pass env.opts (possibly
// further customized) to ValidateSource/Publish/PublishFile.
//
// The returned "stacks" is the operator config-tree root — tests write
// files directly under stacks/configcrates/, stacks/envoys/, stacks/roles/.
func withTestDirs(t *testing.T) testEnv {
	t.Helper()
	stacks := filepath.Join(t.TempDir(), "stacks")
	live := filepath.Join(t.TempDir(), ".live")

	t.Cleanup(func() {
		unlockLiveDir(live) // ensure temp dir cleanup can remove read-only files
	})

	for _, sub := range []string{"configcrates", "templates", "usercrates", "envoys", "roles"} {
		if err := os.MkdirAll(filepath.Join(stacks, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return testEnv{
		stacks: stacks,
		live:   live,
		opts: Options{
			StacksDir:  stacks,
			LiveDir:    live,
			HistoryDir: filepath.Join(t.TempDir(), "history"),
		},
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeValidConfig(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "configcrates", "nginx.vgo"), `
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
    configcrates: [nginx]
`)
}

// noURLOpts returns an Options with URL checking disabled and the given
// test paths applied. Callers pass the per-test env so paths route to
// the right tempdir.
func noURLOpts(env testEnv) Options {
	opts := env.opts
	opts.CheckURLs = false
	return opts
}

// --- Structural validation tests ---

func TestValidateSource_Valid(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	res, err := ValidateSource(noURLOpts(env))
	if err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
	if res.Configcrates != 1 {
		t.Errorf("expected 1 module, got %d", res.Configcrates)
	}
	if res.Entries != 1 {
		t.Errorf("expected 1 entry, got %d", res.Entries)
	}
}

func TestValidateSource_MissingConfigcrateRef(t *testing.T) {
	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: install-nginx
    type: package
    package: nginx
    state: present
`)
	writeFile(t, filepath.Join(env.stacks, "envoys", "web.vgo"), `
envoys:
  - match: "web.example.com"
    configcrates: [nginx, nonexistent]
`)

	_, err := ValidateSource(noURLOpts(env))
	if err == nil {
		t.Fatal("expected error for missing module reference")
	}
}

func TestValidateSource_IdempotencyError(t *testing.T) {
	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "risky.vgo"), `
name: risky
resources:
  - name: run-something
    type: exec
    command: "longdrawer hello"
`)
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [risky]
`)

	_, err := ValidateSource(noURLOpts(env))
	if err == nil {
		t.Fatal("expected idempotency error for unguarded exec")
	}
}

func TestPublish_SyncsFiles(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	res, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if res.Files == 0 {
		t.Error("expected files to be synced")
	}

	if _, err := os.Stat(filepath.Join(env.live, "configcrates", "nginx.vgo")); err != nil {
		t.Error("nginx.vgo not synced to live modules/")
	}
	if _, err := os.Stat(filepath.Join(env.live, "envoys", "web.vgo")); err != nil {
		t.Error("web.vgo not synced to live envoys/")
	}
}

// TestPublish_StacksBytesUnchanged is the regression test for the
// "modlint mangled my source" complaint. Before this fix, every publish
// ran modlint and wrote the linted output BACK to the stacks path,
// reformatting whatever the operator had written. After the fix, publish
// builds an in-memory linted-overrides map, materializes it into a
// staging dir alongside hardlinks for unchanged files, and syncs the
// staging dir to .live — leaving stacks byte-stable.
//
// The test writes a module with intentional non-canonical formatting
// (compact flow-style sequences, hand-chosen key order), runs Publish,
// and asserts:
//
//  1. The bytes in stacks/modules/users/dan.vgo are EXACTLY what was
//     written, byte for byte. No modlint mangling.
//  2. The bytes in .live/modules/users/dan.vgo are the canonical (linted)
//     form, including the flow-style preservation from Fix B.
func TestPublish_StacksBytesUnchanged(t *testing.T) {
	env := withTestDirs(t)

	// Hand-written compact form. Flow-style sequences, non-alphabetic key
	// order. Modlint must not "fix" any of this in stacks.
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
	srcPath := filepath.Join(env.stacks, "configcrates", "users", "dan.vgo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, srcPath, original)
	writeFile(t, filepath.Join(env.stacks, "envoys", "girlslaptop.vgo"), `
envoys:
  - match: "girlslaptop"
    configcrates: [users/dan]
`)

	res, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if res.Files == 0 {
		t.Error("expected files to be synced to .live")
	}

	// Publish formats the source: it adds modlint's `#~` labels but must
	// mangle nothing else — the hand-written compact form (flow-style
	// sequences, non-alphabetic key order) survives. Strip the `#~` lines it
	// added; what remains must be byte-identical to the original.
	got, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("re-reading stacks dan.vgo: %v", err)
	}
	if !strings.Contains(string(got), "#~") {
		t.Error("expected publish to add #~ labels to source")
	}
	var kept []string
	for _, line := range strings.Split(string(got), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#~") {
			continue
		}
		kept = append(kept, line)
	}
	if minusLabels := strings.Join(kept, "\n"); minusLabels != original {
		t.Errorf("publish changed more than the #~ labels.\nwant (minus labels):\n%s\n\ngot (minus labels):\n%s", original, minusLabels)
	}

	// .live must contain the linted bytes — at minimum, the canonical
	// Lint output. Round-trip stability is enough: if we lint the
	// stacks source ourselves and compare, .live should match.
	livePath := filepath.Join(env.live, "configcrates", "users", "dan.vgo")
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

// TestPublish_FormatsSourceStripsLive is the end-to-end contract for the
// comment-aware publish path: source keeps its hand comments (prose AND
// commented-out attributes) and gains the `#~` standard labels, while `.live/`
// is stripped of ALL comments — and `#` lines inside content are data,
// preserved on both sides.
func TestPublish_FormatsSourceStripsLive(t *testing.T) {
	env := withTestDirs(t)

	original := `name: sshd
resources:
  # harden the daemon — keep this note
  #owner: 0644
  - name: sshd-config
    type: file
    state: present
    target_path: /etc/ssh/sshd_config
    owner: root
    group: root
    mode: "0644"
    content: |
      # Authentication
      PermitRootLogin no
`
	srcPath := filepath.Join(env.stacks, "configcrates", "sshd.vgo")
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, srcPath, original)
	writeFile(t, filepath.Join(env.stacks, "envoys", "girlslaptop.vgo"), `
envoys:
  - match: "girlslaptop"
    configcrates: [sshd]
`)

	if _, err := Publish(noURLOpts(env), nil); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	srcBytes, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("re-reading source: %v", err)
	}
	src := string(srcBytes)
	// Source keeps both kinds of hand comment and gains the label.
	for _, want := range []string{
		"# harden the daemon — keep this note",
		"#owner: 0644",
		"#~ ",                    // a Vigo label was added
		"      # Authentication", // content data untouched
	} {
		if !strings.Contains(src, want) {
			t.Errorf("source missing %q after publish:\n%s", want, src)
		}
	}

	liveBytes, err := os.ReadFile(filepath.Join(env.live, "configcrates", "sshd.vgo"))
	if err != nil {
		t.Fatalf("re-reading .live: %v", err)
	}
	live := string(liveBytes)
	// .live is stripped of all comments...
	if strings.Contains(live, "#~") {
		t.Errorf(".live still has #~ labels:\n%s", live)
	}
	if strings.Contains(live, "harden the daemon") || strings.Contains(live, "#owner: 0644") {
		t.Errorf(".live still has operator hand comments:\n%s", live)
	}
	// ...but content data (including its #) is preserved.
	if !strings.Contains(live, "# Authentication") || !strings.Contains(live, "PermitRootLogin no") {
		t.Errorf(".live lost content data:\n%s", live)
	}
}

// TestHashTree_SkipsNestedLiveDir guards the invariant that letting `.live/`
// live inside the stacks does not cause the publish walker to re-read
// already-published files (which would double-load every module at diff
// time). hashTree skips any directory whose basename starts with `.`, plus
// the names in skipDirs. Any regression here — a future walker that
// includes hidden dirs — would surface here.
func TestHashTree_SkipsNestedLiveDir(t *testing.T) {
	stacks := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stacks, "configcrates"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stacks, "configcrates", "nginx.vgo"), "name: nginx\nresources: []\n")

	// Simulate the post-publish layout: .live sits inside stacks and
	// holds a mirror of the modules. These files must NOT appear in the
	// stacks hash.
	liveConfigcrates := filepath.Join(stacks, ".live", "configcrates")
	if err := os.MkdirAll(liveConfigcrates, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(liveConfigcrates, "nginx.vgo"), "name: nginx-canonical\nresources: []\n")

	hashes, err := hashTree(stacks)
	if err != nil {
		t.Fatalf("hashTree: %v", err)
	}
	if _, ok := hashes[filepath.Join("configcrates", "nginx.vgo")]; !ok {
		t.Error("stacks nginx.vgo missing from hash — walker is broken")
	}
	for rel := range hashes {
		if strings.HasPrefix(rel, ".live"+string(filepath.Separator)) {
			t.Errorf("hashTree returned file from nested .live/: %q", rel)
		}
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
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	// Pre-populate live with an extra file.
	os.MkdirAll(filepath.Join(env.live, "configcrates"), 0o755)
	writeFile(t, filepath.Join(env.live, "configcrates", "old.vgo"), "old content")

	_, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.live, "configcrates", "old.vgo")); !os.IsNotExist(err) {
		t.Error("old.vgo should have been removed from live")
	}
}

func TestPublish_LeavesLiveOnValidationFailure(t *testing.T) {
	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "envoys", "bad.vgo"), `
envoys:
  - match: "bad.example.com"
    configcrates: [nonexistent]
`)
	os.MkdirAll(filepath.Join(env.live, "configcrates"), 0o755)
	writeFile(t, filepath.Join(env.live, "configcrates", "existing.vgo"), "keep me")

	_, err := Publish(noURLOpts(env), nil)
	if err == nil {
		t.Fatal("expected validation failure")
	}

	data, readErr := os.ReadFile(filepath.Join(env.live, "configcrates", "existing.vgo"))
	if readErr != nil {
		t.Fatal("live file should still exist")
	}
	if string(data) != "keep me" {
		t.Error("live file content was modified")
	}
}

func TestPublish_SkipValidationAllowsCustomType(t *testing.T) {
	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "custom.vgo"), `
name: custom
resources:
  - name: deploy-app
    type: capistrano
    skip_validation: true
    command: "cap deploy"
`)
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [custom]
`)

	res, err := Publish(env.opts, nil)
	if err != nil {
		t.Fatalf("expected skip_validation publish to succeed, got: %v", err)
	}
	if res.Files == 0 {
		t.Error("expected files to be synced")
	}
}

func TestPublish_SkipsMissingProposedSubdirs(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	os.MkdirAll(filepath.Join(env.live, "vars"), 0o755)
	writeFile(t, filepath.Join(env.live, "vars", "old.yaml"), "old")

	_, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(env.live, "vars", "old.yaml")); !os.IsNotExist(err) {
		t.Error("vars/old.yaml should have been removed")
	}
}

func TestPublish_NothingToPublish(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	// First publish.
	_, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("first publish failed: %v", err)
	}

	// Second publish — nothing changed.
	res, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("second publish failed: %v", err)
	}
	if res.Files != 0 {
		t.Errorf("expected 0 files synced on no-change publish, got %d", res.Files)
	}
	if len(res.Changed) != 0 {
		t.Errorf("expected 0 changed, got %d", len(res.Changed))
	}
}

func TestDiff_DetectsChanges(t *testing.T) {
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	// Populate live with different content.
	os.MkdirAll(filepath.Join(env.live, "configcrates"), 0o755)
	writeFile(t, filepath.Join(env.live, "configcrates", "nginx.vgo"), "old content")
	os.MkdirAll(filepath.Join(env.live, "configcrates"), 0o755)
	writeFile(t, filepath.Join(env.live, "configcrates", "deleted.vgo"), "will be removed")

	_, changed, removed, err := diffTrees(env.opts.StacksDir, env.live)
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
	env := withTestDirs(t)
	writeValidConfig(t, env.stacks)

	// .live doesn't exist yet — everything is new.
	_, changed, removed, err := diffTrees(env.opts.StacksDir, env.live)
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

// urlCheckOpts returns env.opts with URL checking enabled and a 5s
// timeout — keeps each URL-check test concise.
func urlCheckOpts(env testEnv) Options {
	opts := env.opts
	opts.CheckURLs = true
	opts.URLTimeout = 5 * time.Second
	return opts
}

func TestCheckURLs_ReachableURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "download.vgo"), fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    state: present
    url: "%s/archive.tar.gz"
    target_path: "/opt/app"
`, srv.URL))
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [download]
`)

	_, err := ValidateSource(urlCheckOpts(env))
	if err != nil {
		t.Fatalf("expected reachable URL to pass, got: %v", err)
	}
}

func TestCheckURLs_404Fails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "download.vgo"), fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    state: present
    url: "%s/missing.tar.gz"
    target_path: "/opt/app"
`, srv.URL))
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [download]
`)

	res, err := ValidateSource(urlCheckOpts(env))
	if err != nil {
		t.Fatalf("URL check should warn, not fail: %v", err)
	}
	if res.URLWarning == "" {
		t.Fatal("expected URL warning for 404")
	}
}

func TestCheckURLs_SkipsNonHTTPURLs(t *testing.T) {
	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "gitmod.vgo"), `
name: gitmod
resources:
  - name: clone-repo
    type: git
    state: present
    repo: "git@github.com:example/repo.git"
    path: "/opt/repo"
`)
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [gitmod]
`)

	_, err := ValidateSource(urlCheckOpts(env))
	if err != nil {
		t.Fatalf("expected SSH git URL to be skipped, got: %v", err)
	}
}

func TestCheckURLs_SkippedWhenDisabled(t *testing.T) {
	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "download.vgo"), `
name: download
resources:
  - name: get-archive
    type: source_package
    state: present
    url: "http://nonexistent.invalid/file.tar.gz"
    target_path: "/opt/app"
`)
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [download]
`)

	_, err := ValidateSource(noURLOpts(env))
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

	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "download.vgo"), fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    state: present
    url: "%s/archive.tar.gz"
    target_path: "/opt/app"
`, srv.URL))
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [download]
`)

	_, err := ValidateSource(urlCheckOpts(env))
	if err != nil {
		t.Fatalf("expected HEAD→GET fallback to pass, got: %v", err)
	}
}

func TestCheckURLs_KeyURLChecked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	env := withTestDirs(t)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "repo.vgo"), fmt.Sprintf(`
name: repo
resources:
  - name: add-repo
    type: repository
    state: present
    repo: "deb https://example.com/apt stable main"
    key_url: "%s/gpg.key"
`, srv.URL))
	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [repo]
`)

	res, err := ValidateSource(urlCheckOpts(env))
	if err != nil {
		t.Fatalf("URL check should warn, not fail: %v", err)
	}
	if res.URLWarning == "" {
		t.Fatal("expected URL warning for 404 key_url")
	}
}

func TestCheckURLs_OnlyChecksChangedConfigcrates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	env := withTestDirs(t)

	// Module with a bad URL — but it's already in live unchanged.
	badConfigcrate := fmt.Sprintf(`
name: download
resources:
  - name: get-archive
    type: source_package
    state: present
    url: "%s/missing.tar.gz"
    target_path: "/opt/app"
`, srv.URL)
	writeFile(t, filepath.Join(env.stacks, "configcrates", "download.vgo"), badConfigcrate)
	os.MkdirAll(filepath.Join(env.live, "configcrates"), 0o755)
	writeFile(t, filepath.Join(env.live, "configcrates", "download.vgo"), badConfigcrate) // same content

	writeFile(t, filepath.Join(env.stacks, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [download]
`)
	os.MkdirAll(filepath.Join(env.live, "envoys"), 0o755)
	writeFile(t, filepath.Join(env.live, "envoys", "all.vgo"), `
envoys:
  - match: "*.example.com"
    configcrates: [download]
`)

	// URL check enabled but module hasn't changed — should pass.
	_, err := ValidateSource(urlCheckOpts(env))
	if err != nil {
		t.Fatalf("expected unchanged module to skip URL check, got: %v", err)
	}
}

// --- prune ---

// writePruneFixture lays out a stacks with a mix of referenced and
// unreferenced modules, usercrates, and templates so the prune test can
// assert which files survive publish.
func writePruneFixture(t *testing.T, env testEnv) {
	t.Helper()

	// Referenced module → kept.
	writeFile(t, filepath.Join(env.stacks, "configcrates", "nginx.vgo"), `
name: nginx
resources:
  - name: site
    type: file
    state: present
    target_path: /etc/nginx/conf.d/site
    owner: root
    group: root
    mode: "0644"
    source: templates/nginx-site.tmpl
`)
	// Unreferenced module → pruned.
	writeFile(t, filepath.Join(env.stacks, "configcrates", "orphan.vgo"), `
name: orphan
resources:
  - name: noop
    type: exec
    command: "true"
    unless: "true"
`)

	// Referenced usercrate → kept.
	writeFile(t, filepath.Join(env.stacks, "usercrates", "alice.vgo"), `
name: alice
resources:
  - name: alice
    type: user
    state: present
    username: alice
`)
	// Unreferenced usercrate → pruned.
	writeFile(t, filepath.Join(env.stacks, "usercrates", "bob.vgo"), `
name: bob
resources:
  - name: bob
    type: user
    state: present
    username: bob
`)

	// Templates: site.tmpl referenced by nginx module; orphan.tmpl not.
	if err := os.MkdirAll(filepath.Join(env.stacks, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(env.stacks, "templates", "nginx-site.tmpl"), "server { listen 80; }\n")
	writeFile(t, filepath.Join(env.stacks, "templates", "orphan.tmpl"), "no consumer\n")

	writeFile(t, filepath.Join(env.stacks, "envoys", "web.vgo"), `
envoys:
  - match: "web.example.com"
    configcrates: [nginx]
    usercrates: [alice]
`)
}

func TestPublish_PrunesUnreferenced(t *testing.T) {
	env := withTestDirs(t)
	writePruneFixture(t, env)

	res, err := Publish(noURLOpts(env), nil)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if res.Files == 0 {
		t.Fatal("expected files to be synced")
	}

	// Referenced files present.
	for _, rel := range []string{
		"configcrates/nginx.vgo",
		"usercrates/alice.vgo",
		"templates/nginx-site.tmpl",
	} {
		if _, err := os.Stat(filepath.Join(env.live, rel)); err != nil {
			t.Errorf("expected %s in .live/, missing: %v", rel, err)
		}
	}

	// Unreferenced files pruned.
	for _, rel := range []string{
		"configcrates/orphan.vgo",
		"usercrates/bob.vgo",
		"templates/orphan.tmpl",
	} {
		if _, err := os.Stat(filepath.Join(env.live, rel)); !os.IsNotExist(err) {
			t.Errorf("expected %s pruned from .live/, still present (err=%v)", rel, err)
		}
	}

	// Stacks untouched — the operator's library stays whole.
	for _, rel := range []string{
		"configcrates/orphan.vgo",
		"usercrates/bob.vgo",
		"templates/orphan.tmpl",
	} {
		if _, err := os.Stat(filepath.Join(env.stacks, rel)); err != nil {
			t.Errorf("stacks %s should be untouched by prune, missing: %v", rel, err)
		}
	}
}

func TestPublish_NoPrunePreservesEverything(t *testing.T) {
	env := withTestDirs(t)
	writePruneFixture(t, env)
	opts := noURLOpts(env)
	opts.NoPrune = true

	if _, err := Publish(opts, nil); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Every fixture file lands in .live/.
	for _, rel := range []string{
		"configcrates/nginx.vgo",
		"configcrates/orphan.vgo",
		"usercrates/alice.vgo",
		"usercrates/bob.vgo",
		"templates/nginx-site.tmpl",
		"templates/orphan.tmpl",
	} {
		if _, err := os.Stat(filepath.Join(env.live, rel)); err != nil {
			t.Errorf("--no-prune: expected %s in .live/, missing: %v", rel, err)
		}
	}
}

func TestPublish_PrunePreservesStructuralFiles(t *testing.T) {
	env := withTestDirs(t)

	// One unreferenced module so the prune logic does something.
	writeFile(t, filepath.Join(env.stacks, "configcrates", "lonely.vgo"), `
name: lonely
resources:
  - name: noop
    type: exec
    command: "true"
    unless: "true"
`)

	// Structural files at the stacks top level — every one of these
	// must survive prune even though they're not "referenced" in the
	// modules/usercrates sense.
	writeFile(t, filepath.Join(env.stacks, "common.vgo"), "configcrates: []\n")
	writeFile(t, filepath.Join(env.stacks, "environments.vgo"), "environments:\n  prod: {}\n")
	writeFile(t, filepath.Join(env.stacks, "waivers.vgo"), "waivers: {}\n")
	writeFile(t, filepath.Join(env.stacks, "roles", "roles.vgo"), "roles: []\n")
	writeFile(t, filepath.Join(env.stacks, "envoys", "envoys.vgo"), `
envoys:
  - match: "*"
    configcrates: []
`)

	if _, err := Publish(noURLOpts(env), nil); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Every structural file still present in .live/.
	for _, rel := range []string{
		"common.vgo",
		"environments.vgo",
		"waivers.vgo",
		"roles/roles.vgo",
		"envoys/envoys.vgo",
	} {
		if _, err := os.Stat(filepath.Join(env.live, rel)); err != nil {
			t.Errorf("structural file %s must survive prune, missing: %v", rel, err)
		}
	}

	// Unreferenced module pruned even on a structurals-only run.
	if _, err := os.Stat(filepath.Join(env.live, "configcrates", "lonely.vgo")); !os.IsNotExist(err) {
		t.Errorf("unreferenced module should be pruned, still present (err=%v)", err)
	}
}

// TestFleetFindings_SkipsUnreachableConfigcrates verifies that fleet
// collision checks ignore modules no carrier can route to an envoy: a
// clash among unreachable modules can't physically happen, so it must
// not generate publish-time noise. A clash among reachable modules
// still warns.
func TestFleetFindings_SkipsUnreachableConfigcrates(t *testing.T) {
	dir := t.TempDir()
	mod := func(rel, pkg string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(rel), ".vgo")
		body := fmt.Sprintf("name: %s\nresources:\n  - name: %s-pkg\n    type: package\n    package: %s\n    state: present\n", name, name, pkg)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// redis: two reachable modules collide — must warn.
	mod("configcrates/reach-a.vgo", "redis")
	mod("configcrates/reach-b.vgo", "redis")
	// nginx: two unreachable modules collide — must NOT warn.
	mod("configcrates/lib-a.vgo", "nginx")
	mod("configcrates/lib-b.vgo", "nginx")
	// mysql: one reachable + one unreachable — must NOT warn; with only
	// one envoy-reachable side the clash can't physically happen.
	mod("configcrates/reach-c.vgo", "mysql")
	mod("configcrates/lib-c.vgo", "mysql")

	relPaths := []string{
		"configcrates/reach-a.vgo", "configcrates/reach-b.vgo",
		"configcrates/lib-a.vgo", "configcrates/lib-b.vgo",
		"configcrates/reach-c.vgo", "configcrates/lib-c.vgo",
	}
	unreachable := map[string]bool{
		"configcrates/lib-a.vgo": true,
		"configcrates/lib-b.vgo": true,
		"configcrates/lib-c.vgo": true,
	}

	findings := fleetFindings(dir, relPaths, unreachable)

	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 finding (the redis collision), got %d: %v", len(findings), findings)
	}
	f := findings[0]
	if f.RuleID != "fleet-duplicate-package" {
		t.Errorf("RuleID = %q, want fleet-duplicate-package", f.RuleID)
	}
	if !strings.Contains(f.Message, "redis") {
		t.Errorf("finding should be the redis collision; got %q", f.Message)
	}
	if strings.Contains(f.Message, "nginx") || strings.Contains(f.Message, "mysql") {
		t.Errorf("unreachable collisions must be suppressed; got %q", f.Message)
	}
}
