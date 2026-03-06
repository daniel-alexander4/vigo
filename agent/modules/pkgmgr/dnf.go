package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// DnfManager implements PackageManager for Fedora/RHEL/Rocky/Alma systems using dnf.
type DnfManager struct {
	Runner CommandRunner
}

func (d *DnfManager) runner() CommandRunner {
	if d.Runner != nil {
		return d.Runner
	}
	return &DefaultRunner{}
}

func (d *DnfManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := d.runner().Run(ctx, "rpm", "-q", name)
	if err != nil {
		return false, "", nil
	}
	version := strings.TrimSpace(string(out))
	return true, version, nil
}

func (d *DnfManager) Install(ctx context.Context, name, version string) error {
	pkg := name
	if version != "" {
		pkg = name + "-" + version
	}
	out, err := d.runner().Run(ctx, "dnf", "install", "-y", pkg)
	if err != nil {
		return fmt.Errorf("dnf install %s: %w: %s", pkg, err, string(out))
	}
	return nil
}

func (d *DnfManager) Remove(ctx context.Context, name string) error {
	out, err := d.runner().Run(ctx, "dnf", "remove", "-y", name)
	if err != nil {
		return fmt.Errorf("dnf remove %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (d *DnfManager) Upgrade(ctx context.Context, name, version string) error {
	return d.Install(ctx, name, version)
}
