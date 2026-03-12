// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// ZypperManager implements PackageManager for openSUSE/SLES using zypper.
type ZypperManager struct {
	Runner CommandRunner
}

func (z *ZypperManager) runner() CommandRunner {
	if z.Runner != nil {
		return z.Runner
	}
	return &DefaultRunner{}
}

func (z *ZypperManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := z.runner().Run(ctx, "zypper", "se", "-i", name)
	if err != nil {
		return false, "", nil
	}
	// Output format includes lines like: "i | nginx | package | 1.21.5"
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) >= 4 {
			status := strings.TrimSpace(fields[0])
			pkg := strings.TrimSpace(fields[1])
			if status == "i" && pkg == name {
				version := strings.TrimSpace(fields[3])
				return true, version, nil
			}
		}
	}
	return false, "", nil
}

func (z *ZypperManager) Install(ctx context.Context, name, version string) error {
	pkg := name
	if version != "" {
		pkg = name + "=" + version
	}
	out, err := z.runner().Run(ctx, "zypper", "install", "-y", pkg)
	if err != nil {
		return fmt.Errorf("zypper install %s: %w: %s", pkg, err, string(out))
	}
	return nil
}

func (z *ZypperManager) Remove(ctx context.Context, name string) error {
	out, err := z.runner().Run(ctx, "zypper", "remove", "-y", name)
	if err != nil {
		return fmt.Errorf("zypper remove %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (z *ZypperManager) Upgrade(ctx context.Context, name, version string) error {
	return z.Install(ctx, name, version)
}
