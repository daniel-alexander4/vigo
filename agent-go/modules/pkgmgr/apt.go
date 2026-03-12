// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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

// dpkgStatus returns the parsed status and version from dpkg -s.
// Returns empty status if the package is unknown to dpkg.
func (a *AptManager) dpkgStatus(ctx context.Context, name string) (status, version string) {
	out, err := a.runner().Run(ctx, "dpkg", "-s", name)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Status:") {
			status = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
		if strings.HasPrefix(line, "Version:") {
			version = strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		}
	}
	return status, version
}

func (a *AptManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	status, version := a.dpkgStatus(ctx, name)
	if status == "" {
		return false, "", nil
	}
	// Only treat "install ok installed" as actually installed.
	if strings.Contains(status, "install ok installed") {
		return true, version, nil
	}
	return false, "", nil
}

func (a *AptManager) Install(ctx context.Context, name, version string) error {
	// If the package is in a broken state (half-configured, unpacked, etc.),
	// attempt dpkg --configure to repair it before trying apt-get install.
	status, _ := a.dpkgStatus(ctx, name)
	if status != "" && !strings.Contains(status, "install ok installed") {
		a.runner().Run(ctx, "dpkg", "--configure", name)
		// If repair succeeded, the package is now fully installed.
		if repaired, _ := a.dpkgStatus(ctx, name); strings.Contains(repaired, "install ok installed") {
			return nil
		}
	}

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
