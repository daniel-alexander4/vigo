package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// AptManager implements PackageManager for Debian/Ubuntu systems using apt-get.
type AptManager struct {
	Runner CommandRunner
}

func (a *AptManager) runner() CommandRunner {
	if a.Runner != nil {
		return a.Runner
	}
	return &DefaultRunner{}
}

func (a *AptManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := a.runner().Run(ctx, "dpkg", "-s", name)
	if err != nil {
		// dpkg exits non-zero for packages that are not installed
		return false, "", nil
	}
	version := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Version:") {
			version = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
			break
		}
	}
	return true, version, nil
}

func (a *AptManager) Install(ctx context.Context, name, version string) error {
	pkg := name
	if version != "" {
		pkg = name + "=" + version
	}
	out, err := a.runner().Run(ctx, "apt-get", "install", "-y", pkg)
	if err != nil {
		return fmt.Errorf("apt-get install %s: %w: %s", pkg, err, string(out))
	}
	return nil
}

func (a *AptManager) Remove(ctx context.Context, name string) error {
	out, err := a.runner().Run(ctx, "apt-get", "remove", "-y", name)
	if err != nil {
		return fmt.Errorf("apt-get remove %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (a *AptManager) Upgrade(ctx context.Context, name, version string) error {
	return a.Install(ctx, name, version)
}
