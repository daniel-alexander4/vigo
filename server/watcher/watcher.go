// Package watcher polls isopass for secret version changes and triggers
// force-push re-convergence for nodes whose configs reference rotated secrets.
package watcher

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

// VersionProvider retrieves version information for a secret path.
type VersionProvider interface {
	GetVersion(ctx context.Context, key string) (int, error)
}

// NodeStore resolves which node hostnames reference a given secret path.
type NodeStore interface {
	NodesReferencingSecret(secretPath string) []string
}

// DB abstracts the database operations needed by the watcher.
type DB interface {
	SetForcePush(ctx context.Context, id uuid.UUID) error
	CreateSecretRotationEvent(ctx context.Context, params RotationRecord) error
	GetNodeByHostname(ctx context.Context, hostname string) (uuid.UUID, error)
}

// EventDispatcher fires webhook events for secret rotation.
type EventDispatcher interface {
	DispatchRotation(ctx context.Context, event string, secretPath string, affected int)
}

// RotationRecord contains the parameters for recording a secret rotation event.
type RotationRecord struct {
	SecretPath      string
	OldVersion      string
	NewVersion      string
	AffectedNodes int32
}

// Watcher polls isopass at a configurable interval and detects secret
// version changes. When a rotation is detected, all nodes referencing
// that secret have their force_push flag set.
type Watcher struct {
	provider     VersionProvider
	db           DB
	configStore  NodeStore
	dispatcher   EventDispatcher
	pollInterval time.Duration
	watchPaths   []string
	versions     map[string]int
	mu           sync.Mutex
}

// NewWatcher creates a watcher that monitors the given secret paths for
// version changes at the specified polling interval.
func NewWatcher(
	provider VersionProvider,
	db DB,
	configStore NodeStore,
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

// Run starts the polling loop. It initializes the version map, then checks
// for rotations on each tick. It blocks until the context is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	w.initVersions(ctx)

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	slog.Info("secret watcher started",
		"paths", w.watchPaths,
		"interval", w.pollInterval)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
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
	for _, path := range w.watchPaths {
		currentVersion, err := w.provider.GetVersion(ctx, path)
		if err != nil {
			slog.Error("watcher: checking secret version",
				"path", path, "error", err)
			continue
		}

		w.mu.Lock()
		lastVersion := w.versions[path]
		w.mu.Unlock()

		if currentVersion <= lastVersion {
			continue
		}

		slog.Info("secret rotated",
			"path", path,
			"old_version", lastVersion,
			"new_version", currentVersion)

		w.mu.Lock()
		w.versions[path] = currentVersion
		w.mu.Unlock()

		// Identify affected nodes.
		hostnames := w.configStore.NodesReferencingSecret(path)
		var affectedIDs []uuid.UUID
		for _, hostname := range hostnames {
			nodeID, err := w.db.GetNodeByHostname(ctx, hostname)
			if err != nil {
				slog.Error("watcher: looking up node",
					"hostname", hostname, "error", err)
				continue
			}
			if nodeID == uuid.Nil {
				continue
			}
			affectedIDs = append(affectedIDs, nodeID)
		}

		// Set force-push flag for each affected node.
		for _, id := range affectedIDs {
			if err := w.db.SetForcePush(ctx, id); err != nil {
				slog.Error("watcher: setting force push",
					"node_id", id, "error", err)
			}
		}

		// Record audit event.
		if err := w.db.CreateSecretRotationEvent(ctx, RotationRecord{
			SecretPath:      path,
			OldVersion:      strconv.Itoa(lastVersion),
			NewVersion:      strconv.Itoa(currentVersion),
			AffectedNodes: int32(len(affectedIDs)),
		}); err != nil {
			slog.Error("watcher: recording rotation event",
				"path", path, "error", err)
		}

		// Fire webhook.
		if w.dispatcher != nil {
			w.dispatcher.DispatchRotation(ctx, "secret.rotated", path, len(affectedIDs))
		}
	}
}
