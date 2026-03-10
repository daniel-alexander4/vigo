// Package pkgmgr defines the PackageManager interface and provides backends
// for apt, dnf, yum, apk, pacman, brew, and zypper.
package pkgmgr

import (
	"context"

	"github.com/hobnob/hobnob/agent/internal/cmdrun"
)

// PackageManager abstracts package install/remove/query operations across
// different OS package managers.
type PackageManager interface {
	// IsInstalled checks whether a package is installed and returns its version.
	IsInstalled(ctx context.Context, name string) (installed bool, version string, err error)

	// Install installs a package, optionally at a specific version.
	// Pass empty string for version to install latest.
	Install(ctx context.Context, name, version string) error

	// Remove removes an installed package.
	Remove(ctx context.Context, name string) error

	// Upgrade upgrades a package, optionally to a specific version.
	Upgrade(ctx context.Context, name, version string) error
}

// CommandRunner is an alias for the shared command runner interface.
type CommandRunner = cmdrun.Runner

// DefaultRunner is an alias for the shared default command runner.
type DefaultRunner = cmdrun.Default
