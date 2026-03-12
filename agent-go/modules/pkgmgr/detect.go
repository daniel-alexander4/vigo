// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package pkgmgr

import (
	"fmt"
	"strings"
)

// Detect returns the appropriate PackageManager for the given OS family and distro.
// osFamily is typically "linux" or "darwin". osDistro is the lowercase distribution
// name (e.g., "ubuntu", "fedora", "alpine").
func Detect(osFamily, osDistro string) (PackageManager, error) {
	family := strings.ToLower(osFamily)
	distro := strings.ToLower(osDistro)

	if family == "darwin" {
		return &BrewManager{}, nil
	}

	// gopsutil may return the distro name (e.g. "debian") as PlatformFamily
	// instead of "linux". Normalize by checking the distro list directly.
	if family != "linux" {
		switch family {
		case "debian", "ubuntu", "mint", "linuxmint", "fedora", "rhel", "rocky", "alma",
			"centos", "alpine", "arch", "manjaro", "opensuse", "sles":
			// family is actually a distro name; treat as Linux.
			if distro == "" {
				distro = family
			}
		default:
			return nil, fmt.Errorf("unsupported OS family: %s", family)
		}
	}

	switch distro {
	case "ubuntu", "debian", "mint", "linuxmint":
		return &AptManager{}, nil
	case "fedora", "rhel", "rocky", "alma":
		return &DnfManager{}, nil
	case "centos":
		return &YumManager{}, nil
	case "alpine":
		return &ApkManager{}, nil
	case "arch", "manjaro":
		return &PacmanManager{}, nil
	case "opensuse", "sles":
		return &ZypperManager{}, nil
	default:
		return nil, fmt.Errorf("unsupported Linux distribution: %s", distro)
	}
}

// DetectRepoManager returns the appropriate RepoManager for the given OS
// family and distro. Returns nil (not an error) for platforms that don't
// support repository management (e.g., macOS/brew, Alpine/apk).
func DetectRepoManager(osFamily, osDistro string) RepoManager {
	family := strings.ToLower(osFamily)
	distro := strings.ToLower(osDistro)

	if family != "linux" {
		// Normalize: gopsutil may report distro as family.
		switch family {
		case "debian", "ubuntu", "mint", "linuxmint", "fedora", "rhel", "rocky", "alma",
			"centos", "opensuse", "sles":
			if distro == "" {
				distro = family
			}
		default:
			return nil
		}
	}

	switch distro {
	case "ubuntu", "debian", "mint", "linuxmint":
		return &AptRepoManager{}
	case "fedora", "rhel", "rocky", "alma", "centos":
		return &DnfRepoManager{}
	case "opensuse", "sles":
		return &DnfRepoManager{} // zypper uses same /etc/yum.repos.d/ layout
	default:
		return nil
	}
}
