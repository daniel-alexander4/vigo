// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

// Package stackswatch watches the stacks gitback bare repo's
// refs directory and triggers a config reload when a new commit
// lands (ADR-025 Slice 3). The bare repo at
// `/var/lib/vigo/swarm/gitback/projects/<id>.git/refs/` is updated
// by gitback's post-receive hook (own push or peer fan-out) and by
// `git fetch` from peers — both paths surface as fsnotify events on
// the refs subtree.
//
// On a settled ref change the watcher extracts HEAD into a temp tree and
// publishes it through publish.Publish — the same validation, lint,
// snapshot, publish lock and atomic sync `vigocli config publish` uses —
// then calls ConfigReloader.Reload(). A tree that fails validation never
// reaches .live/, and a publish freeze holds pushed commits back.
//
// Design parallels server/watcher/ (the secrets-rotation watcher):
// long-lived goroutine, single Reload entry point, callbacks invoked
// from a background context. ConfigStore.Reload serializes overlapping
// reloads.
package stackswatch

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

	"git.alexander4.io/vigo/server/daemon"
	"git.alexander4.io/vigo/server/publish"
	"github.com/fsnotify/fsnotify"
)

// ConfigReloader is the subset of *config.ConfigStore this package
// needs. Defined as an interface to keep the dependency one-way:
// stackswatch imports nothing from config.
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
// `git archive HEAD | tar -x` extracts the latest commit into a temp
// tree; publish.Publish validates it and syncs it into `.live/`
// (delete-aware: files removed from the stacks leave `.live/`).
type Watcher struct {
	bareRepo   string
	liveDir    string
	historyDir string
	refsDir    string
	reloader   ConfigReloader
}

// pullTimeout bounds the git archive | tar extraction. A hung git or tar
// would otherwise block the watcher loop forever.
const pullTimeout = 2 * time.Minute

// New returns a Watcher for the project at projectsRoot/<projectIDHex>.git
// whose HEAD content syncs into liveDir on every ref-change settle.
// projectsRoot is typically `/var/lib/vigo/swarm/gitback/projects`,
// projectIDHex is the hex of the stacks project_id, and liveDir
// is typically `/srv/vigo/.live` and historyDir its sibling
// `config-history` (publish.PathsUnder).
func New(projectsRoot, projectIDHex, liveDir, historyDir string, reloader ConfigReloader) *Watcher {
	repoDir := filepath.Join(projectsRoot, projectIDHex+".git")
	return &Watcher{
		bareRepo:   repoDir,
		liveDir:    liveDir,
		historyDir: historyDir,
		refsDir:    filepath.Join(repoDir, "refs"),
		reloader:   reloader,
	}
}

// Run blocks until ctx is cancelled. Returns once the watcher has
// cleanly torn down its fsnotify subscription. Non-fatal errors
// (reload failure, transient fsnotify error) are logged; only
// terminal errors (refs dir doesn't exist, fsnotify setup failure)
// return.
func (w *Watcher) Run(ctx context.Context) error {
	if w.reloader == nil {
		return errors.New("stackswatch: nil reloader")
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
			slog.Warn("stackswatch: failed to watch subdir",
				"path", p,
				"error", err,
			)
		}
	}

	slog.Info("stackswatch: started", "refs_dir", w.refsDir, "debounce", QuiesceDebounce)

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
			slog.Info("stackswatch: stopping")
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return errors.New("stackswatch: watcher channel closed")
			}
			// A renamed/recreated refs subdir (e.g. refs/heads replaced
			// during a gc, or a namespace dir appearing later) drops the
			// boot-time inotify watch on the old inode. On a CREATE for a
			// directory under refs/, re-Add the watch so updates keep
			// flowing. Errors are best-effort (the dir may vanish again).
			if ev.Op.Has(fsnotify.Create) {
				if info, statErr := os.Stat(ev.Name); statErr == nil && info.IsDir() {
					if err := watcher.Add(ev.Name); err != nil {
						slog.Warn("stackswatch: failed to re-watch created subdir",
							"path", ev.Name, "error", err)
					}
				}
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
				return errors.New("stackswatch: watcher errors channel closed")
			}
			slog.Warn("stackswatch: fsnotify error", "error", err)
		case <-fireC:
			fireC = nil
			quiesce = nil
			daemon.Guard("stacks-watcher", func() {
				start := time.Now()
				if err := w.pullIntoLive(); err != nil {
					slog.Error("stackswatch: bare→.live pull failed; skipping reload",
						"error", err,
						"bare_repo", w.bareRepo,
						"live_dir", w.liveDir,
					)
					return
				}
				if err := w.reloader.Reload(); err != nil {
					slog.Error("stackswatch: config reload failed", "error", err)
				} else {
					slog.Info("stackswatch: bare→.live pull + reload complete",
						"elapsed_ms", time.Since(start).Milliseconds(),
					)
				}
			})
		}
	}
}

// pullIntoLive extracts HEAD from the bare repo into a temp tree with
// `git archive HEAD | tar -x` (both bounded by pullTimeout) and publishes
// that tree into `.live/` through publish.Publish. No working git tree
// lands in `.live/` — operators inspecting it still see plain YAML.
//
// On bare repos with no commits yet (between project founding and the
// first push), `git archive HEAD` fails with a clear error; the watcher
// logs and skips the reload — `.live/` keeps whatever it had. A tree that
// fails validation, or a publish freeze, likewise leaves `.live/` alone.
func (w *Watcher) pullIntoLive() error {
	tmp, err := os.MkdirTemp("", "vigo-stacks-pull-*")
	if err != nil {
		return fmt.Errorf("creating temp pull dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	if err := extractHead(w.bareRepo, tmp, pullTimeout); err != nil {
		return err
	}

	res, err := publish.Publish(publish.Options{
		StacksDir:  tmp,
		LiveDir:    w.liveDir,
		HistoryDir: w.historyDir,
	})
	if err != nil {
		return fmt.Errorf("publishing HEAD into %s: %w", w.liveDir, err)
	}
	slog.Info("stackswatch: published HEAD", "changed", len(res.Changed), "removed", len(res.Removed))
	return nil
}

// extractHead materializes bareRepo's HEAD tree into dst. git and tar run
// under one context with the given timeout; on expiry both are killed.
func extractHead(bareRepo, dst string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	archive := exec.CommandContext(ctx, "git", "--git-dir="+bareRepo, "archive", "--format=tar", "HEAD")
	untar := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", dst)
	pipeR, pipeW := io.Pipe()
	archive.Stdout = pipeW
	untar.Stdin = pipeR
	if err := archive.Start(); err != nil {
		return fmt.Errorf("starting git archive: %w", err)
	}
	if err := untar.Start(); err != nil {
		_ = archive.Process.Kill() // best-effort cleanup; the start failure is already being returned
		_ = archive.Wait()
		return fmt.Errorf("starting tar -x: %w", err)
	}
	// archive writes the tarball into the pipe; untar drains it
	// concurrently. Wait for archive, close the write end so tar sees
	// EOF, then wait for tar — each command waited exactly once.
	archiveErr := archive.Wait()
	pipeW.Close()
	untarErr := untar.Wait()
	if ctx.Err() != nil {
		return fmt.Errorf("git archive | tar timed out after %s: %w", timeout, ctx.Err())
	}
	if archiveErr != nil {
		return fmt.Errorf("git archive HEAD (no commits yet?): %w", archiveErr)
	}
	if untarErr != nil {
		return fmt.Errorf("tar -x: %w", untarErr)
	}
	return nil
}
