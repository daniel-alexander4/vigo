// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Publish state lives on disk next to .live, under the server root, so the
// two processes that change .live — vigocli (config publish) and vigosrv
// (rollback, auto-rollback) — see the same freeze, the same lock and the
// same record of which snapshot is live.
const (
	lockFileName        = ".publish.lock"
	freezeFileName      = ".publish-freeze"
	liveVersionFileName = ".live-version"
)

func stateFile(liveDir, name string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(liveDir)), name)
}

// lockPublish takes the exclusive cross-process publish lock for liveDir:
// an flock(2) on <server-root>/.publish.lock. Every writer of .live holds
// it — Publish, PublishFile, RollbackToVersion — so a rollback cannot swap
// .live out from under a publish's sync, and two publishes cannot
// interleave. The kernel releases it when the holder exits, so it cannot
// go stale.
func lockPublish(liveDir string) (func(), error) {
	path := stateFile(liveDir, lockFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating publish lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening publish lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("taking publish lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// SetFreeze freezes config publishes for liveDir until t. The deadline is
// written to <server-root>/.publish-freeze so vigocli's local publish
// honours a freeze set through the REST API.
func SetFreeze(liveDir string, t time.Time) error {
	path := stateFile(liveDir, freezeFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(t.UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing publish freeze: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing publish freeze: %w", err)
	}
	return nil
}

// ClearFreeze removes any publish freeze for liveDir.
func ClearFreeze(liveDir string) error {
	if err := os.Remove(stateFile(liveDir, freezeFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clearing publish freeze: %w", err)
	}
	return nil
}

// FrozenUntil returns the active freeze deadline for liveDir, or the zero
// time when publishes are not frozen. An unreadable or malformed freeze
// file is an error: a freeze that cannot be read must not silently lift.
func FrozenUntil(liveDir string) (time.Time, error) {
	data, err := os.ReadFile(stateFile(liveDir, freezeFileName))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("reading publish freeze: %w", err)
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, fmt.Errorf("publish freeze file %s is malformed: %w", stateFile(liveDir, freezeFileName), err)
	}
	if !time.Now().Before(t) {
		return time.Time{}, nil
	}
	return t, nil
}

// checkFreeze returns an error when publishes to liveDir are frozen, by
// either freeze: the runtime one (SetFreeze, `vigocli config freeze`) or
// `maintenance.freeze_until` in <server-root>/server.yaml, which can only
// be lifted by editing the file.
func checkFreeze(liveDir string) error {
	until, err := FrozenUntil(liveDir)
	if err != nil {
		return err
	}
	if !until.IsZero() {
		return fmt.Errorf("config publish is frozen until %s", until.Format(time.RFC3339))
	}
	yamlUntil, err := serverYAMLFreeze(stateFile(liveDir, "server.yaml"))
	if err != nil {
		return err
	}
	if !yamlUntil.IsZero() && time.Now().Before(yamlUntil) {
		return fmt.Errorf("config publish is frozen until %s (maintenance.freeze_until in server.yaml)", yamlUntil.Format(time.RFC3339))
	}
	return nil
}

// serverYAMLFreeze reads maintenance.freeze_until from server.yaml. A
// missing file means no freeze; a malformed deadline is an error.
func serverYAMLFreeze(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("reading %s for maintenance.freeze_until: %w", path, err)
	}
	var doc struct {
		Maintenance struct {
			FreezeUntil string `yaml:"freeze_until"`
		} `yaml:"maintenance"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return time.Time{}, fmt.Errorf("parsing %s for maintenance.freeze_until: %w", path, err)
	}
	raw := strings.TrimSpace(doc.Maintenance.FreezeUntil)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("maintenance.freeze_until in %s is not RFC3339: %w", path, err)
	}
	return t, nil
}

// recordLiveVersion notes which snapshot .live now holds. The convergence
// monitor reads it to pick the snapshot that preceded the live one.
func recordLiveVersion(liveDir, version string) error {
	path := stateFile(liveDir, liveVersionFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("recording live version: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("recording live version: %w", err)
	}
	return nil
}

// LiveVersion returns the snapshot version .live holds, or "" when unknown.
func LiveVersion(liveDir string) string {
	data, err := os.ReadFile(stateFile(liveDir, liveVersionFileName))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
