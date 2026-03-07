package pkgmgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// DnfRepoManager implements RepoManager for Fedora/RHEL/Rocky/Alma/CentOS
// using /etc/yum.repos.d/ for repo files and rpm --import for GPG keys.
// Works for both dnf and yum backends.
type DnfRepoManager struct {
	Runner CommandRunner
}

func (d *DnfRepoManager) runner() CommandRunner {
	if d.Runner != nil {
		return d.Runner
	}
	return &DefaultRunner{}
}

func (d *DnfRepoManager) IsRepoPresent(_ context.Context, name string) (bool, error) {
	repoPath := filepath.Join("/etc/yum.repos.d", name+".repo")
	_, err := os.Stat(repoPath)
	return err == nil, nil
}

func (d *DnfRepoManager) AddRepo(ctx context.Context, name, keyURL, repoLine string) error {
	// Import GPG key if provided.
	if keyURL != "" {
		out, err := d.runner().Run(ctx, "rpm", "--import", keyURL)
		if err != nil {
			return fmt.Errorf("import GPG key from %s: %w: %s", keyURL, err, string(out))
		}
	}

	// Write repo file. repoLine should be a full INI-style repo block, e.g.:
	//   [grafana]
	//   name=Grafana
	//   baseurl=https://rpm.grafana.com
	//   gpgcheck=1
	//   gpgkey=https://rpm.grafana.com/gpg.key
	repoPath := filepath.Join("/etc/yum.repos.d", name+".repo")
	if err := os.WriteFile(repoPath, []byte(repoLine+"\n"), 0644); err != nil {
		return fmt.Errorf("write repo file %s: %w", repoPath, err)
	}

	return nil
}

func (d *DnfRepoManager) RemoveRepo(_ context.Context, name string) error {
	repoPath := filepath.Join("/etc/yum.repos.d", name+".repo")
	if err := os.Remove(repoPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", repoPath, err)
	}
	return nil
}

func (d *DnfRepoManager) RefreshIndex(ctx context.Context) error {
	// dnf/yum auto-refresh on next install, but we can force it.
	// Try dnf first, fall back to yum.
	out, err := d.runner().Run(ctx, "dnf", "makecache")
	if err != nil {
		out, err = d.runner().Run(ctx, "yum", "makecache")
		if err != nil {
			return fmt.Errorf("makecache: %w: %s", err, string(out))
		}
	}
	return nil
}
