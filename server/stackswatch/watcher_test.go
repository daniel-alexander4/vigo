// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package stackswatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// commitTree makes a bare repo whose HEAD holds files and returns its path.
func commitTree(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	work := filepath.Join(base, "work")
	bare := filepath.Join(base, "bare.git")
	writeTree(t, work, files)
	run(t, base, "git", "init", "-q", "-b", "main", work)
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-q", "-m", "c")
	run(t, base, "git", "clone", "-q", "--bare", work, bare)
	return bare
}

var validTree = map[string]string{
	"configcrates/nginx.vgo": "name: nginx\nresources:\n  - name: install-nginx\n    type: package\n    package: nginx\n    state: present\n",
	"envoys/web.vgo":         "envoys:\n  - match: \"web.example.com\"\n    configcrates: [nginx]\n",
}

func newTestWatcher(t *testing.T, bare string) *Watcher {
	root := t.TempDir()
	live := filepath.Join(root, ".live")
	t.Cleanup(func() {
		_ = filepath.Walk(live, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	return &Watcher{bareRepo: bare, liveDir: live, historyDir: filepath.Join(root, "config-history")}
}

// TestPullIntoLivePublishesThroughValidation: a valid HEAD lands in .live
// with a rollback snapshot; an invalid HEAD is refused and .live keeps the
// previous tree.
func TestPullIntoLivePublishesThroughValidation(t *testing.T) {
	w := newTestWatcher(t, commitTree(t, validTree))
	if err := w.pullIntoLive(); err != nil {
		t.Fatalf("pullIntoLive(valid): %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.liveDir, "configcrates", "nginx.vgo")); err != nil {
		t.Fatalf("valid tree did not reach .live: %v", err)
	}
	snaps, _ := filepath.Glob(filepath.Join(w.historyDir, "*.tar.gz"))
	if len(snaps) != 1 {
		t.Fatalf("expected one rollback snapshot, got %d", len(snaps))
	}

	bad := map[string]string{
		"configcrates/nginx.vgo": validTree["configcrates/nginx.vgo"],
		"envoys/web.vgo":         "envoys:\n  - match: \"web.example.com\"\n    configcrates: [missing]\n",
	}
	w.bareRepo = commitTree(t, bad)
	if err := w.pullIntoLive(); err == nil {
		t.Fatal("pullIntoLive(invalid) succeeded, want a validation refusal")
	}
	data, err := os.ReadFile(filepath.Join(w.liveDir, "envoys", "web.vgo"))
	if err != nil || !strings.Contains(string(data), "[nginx]") {
		t.Fatalf(".live was changed by an invalid HEAD: %q %v", data, err)
	}
}

// TestExtractHeadTimesOut: a git that hangs is killed at the timeout
// instead of blocking the watcher loop.
func TestExtractHeadTimesOut(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	err := extractHead(filepath.Join(bin, "none.git"), t.TempDir(), 300*time.Millisecond)
	if err == nil {
		t.Fatal("extractHead with a hung git returned nil")
	}
	if el := time.Since(start); el > 10*time.Second {
		t.Fatalf("extractHead took %s, the timeout did not bound it", el)
	}
}
