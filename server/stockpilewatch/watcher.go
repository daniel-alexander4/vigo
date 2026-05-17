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
	"log/slog"
	"os"
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

// Watcher monitors a single project's bare-repo refs dir.
type Watcher struct {
	refsDir  string
	reloader ConfigReloader
}

// New returns a Watcher for the project at projectsRoot/<projectIDHex>.git.
// projectsRoot is typically `/var/lib/vigo/swarm/gitback/projects` and
// projectIDHex is the hex of the stockpile project_id (looked up by
// the caller from /srv/vigo/ops-puddle/.vigo-gitback/).
func New(projectsRoot, projectIDHex string, reloader ConfigReloader) *Watcher {
	repoDir := filepath.Join(projectsRoot, projectIDHex+".git")
	return &Watcher{
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
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("stockpilewatch: watcher channel closed")
			}
			slog.Debug("stockpilewatch: fs event", "path", event.Name, "op", event.Op.String())
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
			if err := w.reloader.Reload(); err != nil {
				slog.Error("stockpilewatch: config reload failed", "error", err)
			} else {
				slog.Info("stockpilewatch: config reload complete",
					"elapsed_ms", time.Since(start).Milliseconds(),
				)
			}
		}
	}
}
