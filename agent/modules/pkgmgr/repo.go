package pkgmgr

import "context"

// RepoManager abstracts package repository management across OS families.
// Each backend knows how to add/remove repos and import GPG keys using
// the distro's native tooling.
type RepoManager interface {
	// IsRepoPresent checks whether a named repository is already configured.
	IsRepoPresent(ctx context.Context, name string) (bool, error)

	// AddRepo adds a package repository. keyURL is the GPG signing key URL
	// (may be empty if the repo doesn't need one). repoLine is the
	// distro-specific repo specification (e.g., APT deb line, DNF baseurl).
	AddRepo(ctx context.Context, name, keyURL, repoLine string) error

	// RemoveRepo removes a named repository configuration.
	RemoveRepo(ctx context.Context, name string) error

	// RefreshIndex updates the package index after adding/removing repos.
	RefreshIndex(ctx context.Context) error
}
