// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package stockpilewatch watches the stockpile gitback bare repo's
// refs directory and triggers a config reload when a new commit
// lands (ADR-025 Slice 3). The bare repo at
// `/var/lib/vigo/swarm/gitback/projects/<id>.git/refs/` is updated
// by gitback's post-receive hook (own push or peer fan-out) and by
// `git fetch` from peers — both paths surface as fsnotify events on
// the refs subtree.
//
// Slice 3 scope: react to ref changes by calling ConfigReloader.Reload().
// Slice 4 will add the bare→.live pull step before the reload (today
// the .live/ dir is unchanged so the reload is a no-op functionally,
// but the watcher infrastructure is in place).
//
// Design parallels server/watcher/ (the secrets-rotation watcher):
// long-lived goroutine, single Reload entry point, callbacks invoked
// from a background context. ConfigStore.Reload is concurrent-safe
// (writes briefly hold the store's RWMutex; readers stay live during
// the swap).
package stockpilewatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ConfigReloader is the subset of *config.ConfigStore this package
// needs. Defined as an interface to keep the dependency one-way:
// stockpilewatch imports nothing from config.
type ConfigReloader interface {
	Reload() error
}

// QuiesceDebounce is how long the watcher waits after the last
// fsnotify event before firing a reload. git push lands several
// writes in quick succession (pack files, ref updates, HEAD); 250ms
// is comfortably longer than any real push's burst on a local disk
// and small enough that the operator-visible reload latency stays
// under a second.
const QuiesceDebounce = 250 * time.Millisecond

// Watcher monitors a single project's bare-repo refs dir and, on
// detected ref change, pulls the project's HEAD content into the
// `.live/` dir and triggers a config reload (ADR-025 Slice 4).
//
// `git archive HEAD | tar -x` is the pull mechanism — no working
// git tree in `.live/`, just a snapshot of the latest commit. The
// sync into `.live/` is delete-aware (files removed from the
// stockpile are removed from `.live/`).
type Watcher struct {
	bareRepo string
	liveDir  string
	refsDir  string
	reloader ConfigReloader
}

// New returns a Watcher for the project at projectsRoot/<projectIDHex>.git
// whose HEAD content syncs into liveDir on every ref-change settle.
// projectsRoot is typically `/var/lib/vigo/swarm/gitback/projects`,
// projectIDHex is the hex of the stockpile project_id, and liveDir
// is typically `/srv/vigo/stacks/.live`.
func New(projectsRoot, projectIDHex, liveDir string, reloader ConfigReloader) *Watcher {
	repoDir := filepath.Join(projectsRoot, projectIDHex+".git")
	return &Watcher{
		bareRepo: repoDir,
		liveDir:  liveDir,
		refsDir:  filepath.Join(repoDir, "refs"),
		reloader: reloader,
	}
}

// Run blocks until ctx is cancelled. Returns once the watcher has
// cleanly torn down its fsnotify subscription. Non-fatal errors
// (reload failure, transient fsnotify error) are logged; only
// terminal errors (refs dir doesn't exist, fsnotify setup failure)
// return.
func (w *Watcher) Run(ctx context.Context) error {
	if w.reloader == nil {
		return errors.New("stockpilewatch: nil reloader")
	}
	if _, err := os.Stat(w.refsDir); err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// Watch refs/ and its children (refs/heads/, refs/tags/, refs/remotes/).
	// fsnotify on Linux uses inotify and doesn't recurse — register each
	// subdir we want to see updates from. Refs land under heads/ for
	// branches; the others matter less here but cost nothing to watch.
	for _, sub := range []string{"", "heads", "tags", "remotes"} {
		p := filepath.Join(w.refsDir, sub)
		if _, err := os.Stat(p); err != nil {
			continue // sub may not exist on a fresh bare repo; that's fine
		}
		if err := watcher.Add(p); err != nil {
			slog.Warn("stockpilewatch: failed to watch subdir",
				"path", p,
				"error", err,
			)
		}
	}

	slog.Info("stockpilewatch: started", "refs_dir", w.refsDir, "debounce", QuiesceDebounce)

	// Debounce by resetting a timer on every event. A nil timer.C
	// channel is never selected (chan operations on nil never fire),
	// so the initial timer is created lazily on the first event.
	var quiesce *time.Timer
	defer func() {
		if quiesce != nil {
			quiesce.Stop()
		}
	}()
	var fireC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			slog.Info("stockpilewatch: stopping")
			return nil
		case _, ok := <-watcher.Events:
			if !ok {
				return errors.New("stockpilewatch: watcher channel closed")
			}
			if quiesce == nil {
				quiesce = time.NewTimer(QuiesceDebounce)
				fireC = quiesce.C
			} else {
				if !quiesce.Stop() {
					// Timer already fired or was already stopped — drain.
					select {
					case <-quiesce.C:
					default:
					}
				}
				quiesce.Reset(QuiesceDebounce)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return errors.New("stockpilewatch: watcher errors channel closed")
			}
			slog.Warn("stockpilewatch: fsnotify error", "error", err)
		case <-fireC:
			fireC = nil
			quiesce = nil
			start := time.Now()
			if err := w.pullIntoLive(); err != nil {
				slog.Error("stockpilewatch: bare→.live pull failed; skipping reload",
					"error", err,
					"bare_repo", w.bareRepo,
					"live_dir", w.liveDir,
				)
				continue
			}
			if err := w.reloader.Reload(); err != nil {
				slog.Error("stockpilewatch: config reload failed", "error", err)
			} else {
				slog.Info("stockpilewatch: bare→.live pull + reload complete",
					"elapsed_ms", time.Since(start).Milliseconds(),
				)
			}
		}
	}
}

// pullIntoLive runs `git archive HEAD | tar -x` from the bare repo
// into a temp dir, then syncs the temp into `.live/` with
// delete-awareness (anything in `.live/` not present in HEAD is
// removed). No working git tree lands in `.live/` — operators
// inspecting it still see plain YAML.
//
// On bare repos with no commits yet (e.g. between project founding
// and the first push), `git archive HEAD` fails with a clear
// error; the watcher logs and skips the reload — `.live/` keeps
// whatever it had.
func (w *Watcher) pullIntoLive() error {
	tmp, err := os.MkdirTemp("", "vigo-stockpile-pull-*")
	if err != nil {
		return fmt.Errorf("creating temp pull dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	// `git archive --format=tar HEAD` writes a tarball of HEAD's
	// tree to stdout. Pipe through `tar -x` to materialize in tmp.
	// Use a single shell command so the pipe is set up by the
	// shell — no Go-side plumbing needed.
	archive := exec.Command("git", "--git-dir="+w.bareRepo, "archive", "--format=tar", "HEAD")
	untar := exec.Command("tar", "-xf", "-", "-C", tmp)
	pipeR, pipeW := io.Pipe()
	archive.Stdout = pipeW
	untar.Stdin = pipeR
	if err := archive.Start(); err != nil {
		return fmt.Errorf("starting git archive: %w", err)
	}
	if err := untar.Start(); err != nil {
		_ = archive.Process.Kill() // best-effort cleanup; the start failure is already being returned
		return fmt.Errorf("starting tar -x: %w", err)
	}
	// archive writes the tarball into the pipe; untar drains it
	// concurrently. Wait for archive, close the write end so tar sees
	// EOF, then wait for tar — each command waited exactly once.
	archiveErr := archive.Wait()
	pipeW.Close()
	untarErr := untar.Wait()
	if archiveErr != nil {
		return fmt.Errorf("git archive HEAD (no commits yet?): %w", archiveErr)
	}
	if untarErr != nil {
		return fmt.Errorf("tar -x: %w", untarErr)
	}

	// Ensure live dir exists.
	if err := os.MkdirAll(w.liveDir, 0o755); err != nil {
		return fmt.Errorf("creating live dir %s: %w", w.liveDir, err)
	}

	// Delete files in live/ that aren't in tmp/.
	if err := pruneMissing(w.liveDir, tmp); err != nil {
		return fmt.Errorf("pruning live dir: %w", err)
	}
	// Copy tmp/ over live/ (adds + updates; preserved files are
	// just overwritten with identical content — cheap).
	if err := copyTree(tmp, w.liveDir); err != nil {
		return fmt.Errorf("copying into live dir: %w", err)
	}
	return nil
}

// pruneMissing walks `dst` and removes any file or directory whose
// relative path doesn't exist under `src`. Symmetric `git rm` for
// the .live/ dir without depending on git.
func pruneMissing(dst, src string) error {
	return filepath.Walk(dst, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dst, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		srcCounterpart := filepath.Join(src, rel)
		if _, statErr := os.Stat(srcCounterpart); os.IsNotExist(statErr) {
			if info.IsDir() {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return filepath.SkipDir
			}
			return os.Remove(path)
		}
		return nil
	})
}

// copyTree recursively copies src/* into dst/, preserving relative
// paths. Used in lockstep with pruneMissing to achieve
// rsync-with-delete semantics.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			defer out.Close()
			_, err = io.Copy(out, in)
			return err
		}
		// Symlinks / specials: skip (stockpile is plain files).
		return nil
	})
}
