// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package pkgmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// AptRepoManager implements RepoManager for Debian/Ubuntu using
// /etc/apt/keyrings/ for GPG keys and /etc/apt/sources.list.d/ for repos.
type AptRepoManager struct {
	Runner CommandRunner
}

func (a *AptRepoManager) runner() CommandRunner {
	if a.Runner != nil {
		return a.Runner
	}
	return &DefaultRunner{}
}

func (a *AptRepoManager) IsRepoPresent(_ context.Context, name string) (bool, error) {
	listPath := filepath.Join("/etc/apt/sources.list.d", name+".list")
	sourcesPath := filepath.Join("/etc/apt/sources.list.d", name+".sources")
	_, errList := os.Stat(listPath)
	_, errSources := os.Stat(sourcesPath)
	return errList == nil || errSources == nil, nil
}

func (a *AptRepoManager) AddRepo(ctx context.Context, name, keyURL, repoLine string) error {
	// Import GPG key if provided.
	if keyURL != "" {
		keyringDir := "/etc/apt/keyrings"
		if err := os.MkdirAll(keyringDir, 0755); err != nil {
			return fmt.Errorf("create keyrings dir: %w", err)
		}

		keyPath := filepath.Join(keyringDir, name+".gpg")
		// curl key | gpg --dearmor > keyfile
		out, err := a.runner().Run(ctx, "sh", "-c",
			fmt.Sprintf("curl -fsSL %q | gpg --batch --dearmor -o %q", keyURL, keyPath))
		if err != nil {
			return fmt.Errorf("import GPG key from %s: %w: %s", keyURL, err, string(out))
		}
	}

	// Write repo list file.
	listPath := filepath.Join("/etc/apt/sources.list.d", name+".list")
	if err := os.WriteFile(listPath, []byte(repoLine+"\n"), 0644); err != nil {
		return fmt.Errorf("write repo file %s: %w", listPath, err)
	}

	return nil
}

func (a *AptRepoManager) RemoveRepo(_ context.Context, name string) error {
	listPath := filepath.Join("/etc/apt/sources.list.d", name+".list")
	sourcesPath := filepath.Join("/etc/apt/sources.list.d", name+".sources")
	keyPath := filepath.Join("/etc/apt/keyrings", name+".gpg")

	for _, p := range []string{listPath, sourcesPath, keyPath} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
}

func (a *AptRepoManager) RefreshIndex(ctx context.Context) error {
	out, err := a.runner().Run(ctx, "apt-get", "update")
	if err != nil {
		return fmt.Errorf("apt-get update: %w: %s", err, string(out))
	}
	return nil
}
