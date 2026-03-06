package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// BrewManager implements PackageManager for macOS using Homebrew.
type BrewManager struct {
	Runner CommandRunner
}

func (b *BrewManager) runner() CommandRunner {
	if b.Runner != nil {
		return b.Runner
	}
	return &DefaultRunner{}
}

func (b *BrewManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := b.runner().Run(ctx, "brew", "list", name)
	if err != nil {
		return false, "", nil
	}
	output := strings.TrimSpace(string(out))
	if output == "" {
		return false, "", nil
	}
	return true, output, nil
}

func (b *BrewManager) Install(ctx context.Context, name, version string) error {
	pkg := name
	if version != "" {
		pkg = name + "@" + version
	}
	out, err := b.runner().Run(ctx, "brew", "install", pkg)
	if err != nil {
		return fmt.Errorf("brew install %s: %w: %s", pkg, err, string(out))
	}
	return nil
}

func (b *BrewManager) Remove(ctx context.Context, name string) error {
	out, err := b.runner().Run(ctx, "brew", "uninstall", name)
	if err != nil {
		return fmt.Errorf("brew uninstall %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (b *BrewManager) Upgrade(ctx context.Context, name, version string) error {
	return b.Install(ctx, name, version)
}
