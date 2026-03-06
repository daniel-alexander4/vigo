package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// YumManager implements PackageManager for CentOS systems using yum.
type YumManager struct {
	Runner CommandRunner
}

func (y *YumManager) runner() CommandRunner {
	if y.Runner != nil {
		return y.Runner
	}
	return &DefaultRunner{}
}

func (y *YumManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := y.runner().Run(ctx, "rpm", "-q", name)
	if err != nil {
		return false, "", nil
	}
	version := strings.TrimSpace(string(out))
	return true, version, nil
}

func (y *YumManager) Install(ctx context.Context, name, version string) error {
	pkg := name
	if version != "" {
		pkg = name + "-" + version
	}
	out, err := y.runner().Run(ctx, "yum", "install", "-y", pkg)
	if err != nil {
		return fmt.Errorf("yum install %s: %w: %s", pkg, err, string(out))
	}
	return nil
}

func (y *YumManager) Remove(ctx context.Context, name string) error {
	out, err := y.runner().Run(ctx, "yum", "remove", "-y", name)
	if err != nil {
		return fmt.Errorf("yum remove %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (y *YumManager) Upgrade(ctx context.Context, name, version string) error {
	return y.Install(ctx, name, version)
}
