// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package watcher polls isopass for secret version changes and triggers
// force-push re-convergence for envoys whose configs reference rotated secrets.
package watcher

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/daniel-alexander4/vigo/server/metrics"
)

// VersionProvider retrieves version information for a secret path.
type VersionProvider interface {
	GetVersion(ctx context.Context, key string) (int, error)
}

// EnvoyStore resolves which envoy hostnames reference a given secret path.
type EnvoyStore interface {
	EnvoysReferencingSecret(secretPath string) []string
}

// ConfigReloader triggers a full config reload (re-resolves secrets from provider).
type ConfigReloader interface {
	Reload() error
}

// DB abstracts the database operations needed by the watcher.
type DB interface {
	SetForcePush(ctx context.Context, id string) error
	CreateSecretRotationEvent(ctx context.Context, params RotationRecord) error
	GetEnvoyByHostname(ctx context.Context, hostname string) (string, error)
	AppendRotatedSecret(ctx context.Context, id, secretPath string) error
}

// EventDispatcher fires webhook events for secret rotation.
type EventDispatcher interface {
	DispatchRotation(ctx context.Context, event string, secretPath string, affected int)
}

// NotifyFunc is an optional callback for sending notifications (e.g. SMTP)
// when a secret rotation is detected. The details map contains "path" and
// "affected" keys.
type NotifyFunc func(event string, details map[string]any)

// RotationRecord contains the parameters for recording a secret rotation event.
type RotationRecord struct {
	SecretPath      string
	OldVersion      string
	NewVersion      string
	AffectedEnvoys int32
}

// Watcher polls isopass at a configurable interval and detects secret
// version changes. When a rotation is detected, all envoys referencing
// that secret have their force_push flag set.
type Watcher struct {
	provider            VersionProvider
	db                  DB
	configStore         EnvoyStore
	configReloader      ConfigReloader
	dispatcher          EventDispatcher
	notifyFunc          NotifyFunc
	pollInterval        time.Duration
	watchPaths          []string
	versions            map[string]int
	mu                  sync.Mutex
	consecutiveFailures int
}

// NewWatcher creates a watcher that monitors the given secret paths for
// version changes at the specified polling interval.
func NewWatcher(
	provider VersionProvider,
	db DB,
	configStore EnvoyStore,
	dispatcher EventDispatcher,
	pollInterval time.Duration,
	watchPaths []string,
) *Watcher {
	return &Watcher{
		provider:     provider,
		db:           db,
		configStore:  configStore,
		dispatcher:   dispatcher,
		pollInterval: pollInterval,
		watchPaths:   watchPaths,
		versions:     make(map[string]int),
	}
}

// SetConfigReloader sets the config reloader used to re-resolve secrets
// when a rotation is detected.
func (w *Watcher) SetConfigReloader(r ConfigReloader) {
	w.configReloader = r
}

// SetNotifyFunc sets an optional callback invoked on secret rotation events.
func (w *Watcher) SetNotifyFunc(fn NotifyFunc) {
	w.notifyFunc = fn
}

// maxBackoffMultiplier caps exponential backoff at 32x the poll interval.
const maxBackoffMultiplier = 32

// Run starts the polling loop. It initializes the version map, then checks
// for rotations on each tick. On consecutive provider failures, it backs off
// exponentially (2^failures * pollInterval, capped at 32x) to avoid hammering
// an unreachable provider. Blocks until the context is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	w.initVersions(ctx)

	slog.Info("secret watcher started",
		"paths", w.watchPaths,
		"interval", w.pollInterval)

	for {
		// Compute next sleep: normal interval or backed-off interval on failures.
		sleep := w.pollInterval
		if w.consecutiveFailures > 0 {
			multiplier := 1 << w.consecutiveFailures // 2^failures
			if multiplier > maxBackoffMultiplier {
				multiplier = maxBackoffMultiplier
			}
			sleep = time.Duration(multiplier) * w.pollInterval
			slog.Warn("watcher: backing off due to provider failures",
				"consecutive_failures", w.consecutiveFailures,
				"next_poll", sleep)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
			w.checkRotations(ctx)
		}
	}
}

// initVersions queries the current version for each watched path and stores
// it as the baseline for rotation detection.
func (w *Watcher) initVersions(ctx context.Context) {
	for _, path := range w.watchPaths {
		ver, err := w.provider.GetVersion(ctx, path)
		if err != nil {
			slog.Error("watcher: failed to get initial version",
				"path", path, "error", err)
			continue
		}
		w.mu.Lock()
		w.versions[path] = ver
		w.mu.Unlock()
	}
}

// checkRotations queries the current version for each watched path and
// triggers force-push + audit record if a version bump is detected.
func (w *Watcher) checkRotations(ctx context.Context) {
	hadError := false
	rotated := false
	for _, path := range w.watchPaths {
		currentVersion, err := w.provider.GetVersion(ctx, path)
		if err != nil {
			slog.Error("watcher: checking secret version",
				"path", path, "error", err)
			hadError = true
			continue
		}

		w.mu.Lock()
		lastVersion := w.versions[path]
		w.mu.Unlock()

		if currentVersion <= lastVersion {
			continue
		}

		metrics.SecretRotationsTotal.Inc()
		slog.Info("secret rotated",
			"path", path,
			"old_version", lastVersion,
			"new_version", currentVersion)

		w.mu.Lock()
		w.versions[path] = currentVersion
		w.mu.Unlock()

		rotated = true

		// Identify affected envoys.
		hostnames := w.configStore.EnvoysReferencingSecret(path)
		var affectedIDs []string
		for _, hostname := range hostnames {
			envoyID, err := w.db.GetEnvoyByHostname(ctx, hostname)
			if err != nil {
				slog.Error("watcher: looking up envoy",
					"hostname", hostname, "error", err)
				continue
			}
			if envoyID == "" {
				continue
			}
			affectedIDs = append(affectedIDs, envoyID)
		}

		// Set force-push flag and record pending rotation for each affected envoy.
		for _, id := range affectedIDs {
			if err := w.db.SetForcePush(ctx, id); err != nil {
				slog.Error("watcher: setting force push",
					"envoy_id", id, "error", err)
			}
			if err := w.db.AppendRotatedSecret(ctx, id, path); err != nil {
				slog.Error("watcher: appending rotated secret",
					"envoy_id", id, "path", path, "error", err)
			}
		}

		// Record audit event.
		if err := w.db.CreateSecretRotationEvent(ctx, RotationRecord{
			SecretPath:      path,
			OldVersion:      strconv.Itoa(lastVersion),
			NewVersion:      strconv.Itoa(currentVersion),
			AffectedEnvoys: int32(len(affectedIDs)),
		}); err != nil {
			slog.Error("watcher: recording rotation event",
				"path", path, "error", err)
		}

		// Fire webhook.
		if w.dispatcher != nil {
			w.dispatcher.DispatchRotation(ctx, "secret.rotated", path, len(affectedIDs))
		}

		// Fire SMTP notification.
		if w.notifyFunc != nil {
			w.notifyFunc("secret.rotated", map[string]any{
				"path":     path,
				"affected": len(affectedIDs),
			})
		}
	}

	// Re-resolve secrets in config store so next check-in gets fresh values.
	if rotated && w.configReloader != nil {
		if err := w.configReloader.Reload(); err != nil {
			slog.Error("watcher: config reload after secret rotation failed", "error", err)
		}
	}

	// Track consecutive failures for backoff in the Run loop.
	if hadError {
		w.consecutiveFailures++
	} else {
		w.consecutiveFailures = 0
	}
}
