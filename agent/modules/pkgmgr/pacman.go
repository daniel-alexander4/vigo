package pkgmgr

import (
	"context"
	"fmt"
	"strings"
)

// PacmanManager implements PackageManager for Arch Linux using pacman.
type PacmanManager struct {
	Runner CommandRunner
}

func (p *PacmanManager) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return &DefaultRunner{}
}

func (p *PacmanManager) IsInstalled(ctx context.Context, name string) (bool, string, error) {
	out, err := p.runner().Run(ctx, "pacman", "-Q", name)
	if err != nil {
		return false, "", nil
	}
	// Output format: "nginx 1.24.0-2"
	parts := strings.Fields(strings.TrimSpace(string(out)))
	version := ""
	if len(parts) >= 2 {
		version = parts[1]
	}
	return true, version, nil
}

func (p *PacmanManager) Install(ctx context.Context, name, version string) error {
	out, err := p.runner().Run(ctx, "pacman", "-S", "--noconfirm", name)
	if err != nil {
		return fmt.Errorf("pacman -S %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (p *PacmanManager) Remove(ctx context.Context, name string) error {
	out, err := p.runner().Run(ctx, "pacman", "-R", "--noconfirm", name)
	if err != nil {
		return fmt.Errorf("pacman -R %s: %w: %s", name, err, string(out))
	}
	return nil
}

func (p *PacmanManager) Upgrade(ctx context.Context, name, version string) error {
	return p.Install(ctx, name, version)
}
