package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// ApkManager implements PackageManager for Alpine Linux using apk.
type ApkManager struct {
	Runner CommandRunner
}

func (a *ApkManager) runner() CommandRunner {
	if a.Runner != nil {
		return a.Runner
	}
	return &DefaultRunner{}
}

func (a *ApkManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := a.runner().Run(ctx, "apk", "info", "-e", name)
	if err != nil {
		return false, "", nil
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return false, "", nil
	}
	return true, version, nil
}

func (a *ApkManager) Install(ctx context.Context, name, version string) error {
	pkg := name
	if version != "" {
		pkg = name + "=" + version
	}
	out, err := a.runner().Run(ctx, "apk", "add", pkg)
	if err != nil {
		return fmt.Errorf("apk add %s: %w: %s", pkg, err, string(out))
	}
	return nil
}

func (a *ApkManager) Remove(ctx context.Context, name string) error {
	out, err := a.runner().Run(ctx, "apk", "del", name)
	if err != nil {
		return fmt.Errorf("apk del %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (a *ApkManager) Upgrade(ctx context.Context, name, version string) error {
	return a.Install(ctx, name, version)
}
