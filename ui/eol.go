// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package ui

import (
	"strings"
	"time"
)

// eolEntry maps a distro+version pattern to its end-of-life date.
type eolEntry struct {
	Distro  string // lowercase match against os.distro
	Version string // prefix match against os.version
	EOL     time.Time
	Label   string // human-readable label, e.g. "Ubuntu 20.04"
}

// eolDatabase is the lookup table for known OS end-of-life dates.
// Sources: Ubuntu (https://wiki.ubuntu.com/Releases), Debian (https://wiki.debian.org/DebianReleases),
// RHEL (https://access.redhat.com/support/policy/updates/errata), CentOS, Amazon Linux, SUSE.
var eolDatabase = []eolEntry{
	// Ubuntu LTS releases
	{Distro: "ubuntu", Version: "14.04", EOL: time.Date(2019, 4, 25, 0, 0, 0, 0, time.UTC), Label: "Ubuntu 14.04"},
	{Distro: "ubuntu", Version: "16.04", EOL: time.Date(2021, 4, 30, 0, 0, 0, 0, time.UTC), Label: "Ubuntu 16.04"},
	{Distro: "ubuntu", Version: "18.04", EOL: time.Date(2023, 5, 31, 0, 0, 0, 0, time.UTC), Label: "Ubuntu 18.04"},
	{Distro: "ubuntu", Version: "20.04", EOL: time.Date(2025, 4, 25, 0, 0, 0, 0, time.UTC), Label: "Ubuntu 20.04"},
	{Distro: "ubuntu", Version: "22.04", EOL: time.Date(2027, 4, 30, 0, 0, 0, 0, time.UTC), Label: "Ubuntu 22.04"},
	{Distro: "ubuntu", Version: "24.04", EOL: time.Date(2029, 4, 25, 0, 0, 0, 0, time.UTC), Label: "Ubuntu 24.04"},

	// Debian releases
	{Distro: "debian", Version: "9", EOL: time.Date(2022, 6, 30, 0, 0, 0, 0, time.UTC), Label: "Debian 9 (Stretch)"},
	{Distro: "debian", Version: "10", EOL: time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC), Label: "Debian 10 (Buster)"},
	{Distro: "debian", Version: "11", EOL: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), Label: "Debian 11 (Bullseye)"},
	{Distro: "debian", Version: "12", EOL: time.Date(2028, 6, 30, 0, 0, 0, 0, time.UTC), Label: "Debian 12 (Bookworm)"},

	// RHEL releases
	{Distro: "rhel", Version: "7", EOL: time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC), Label: "RHEL 7"},
	{Distro: "rhel", Version: "8", EOL: time.Date(2029, 5, 31, 0, 0, 0, 0, time.UTC), Label: "RHEL 8"},
	{Distro: "rhel", Version: "9", EOL: time.Date(2032, 5, 31, 0, 0, 0, 0, time.UTC), Label: "RHEL 9"},

	// CentOS releases
	{Distro: "centos", Version: "7", EOL: time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC), Label: "CentOS 7"},
	{Distro: "centos", Version: "8", EOL: time.Date(2021, 12, 31, 0, 0, 0, 0, time.UTC), Label: "CentOS 8"},

	// Rocky / Alma (RHEL rebuilds)
	{Distro: "rocky", Version: "8", EOL: time.Date(2029, 5, 31, 0, 0, 0, 0, time.UTC), Label: "Rocky Linux 8"},
	{Distro: "rocky", Version: "9", EOL: time.Date(2032, 5, 31, 0, 0, 0, 0, time.UTC), Label: "Rocky Linux 9"},
	{Distro: "almalinux", Version: "8", EOL: time.Date(2029, 5, 31, 0, 0, 0, 0, time.UTC), Label: "AlmaLinux 8"},
	{Distro: "almalinux", Version: "9", EOL: time.Date(2032, 5, 31, 0, 0, 0, 0, time.UTC), Label: "AlmaLinux 9"},

	// Amazon Linux
	{Distro: "amzn", Version: "2", EOL: time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC), Label: "Amazon Linux 2"},
	{Distro: "amzn", Version: "2023", EOL: time.Date(2028, 3, 15, 0, 0, 0, 0, time.UTC), Label: "Amazon Linux 2023"},

	// SUSE
	{Distro: "sles", Version: "12", EOL: time.Date(2027, 10, 31, 0, 0, 0, 0, time.UTC), Label: "SLES 12"},
	{Distro: "sles", Version: "15", EOL: time.Date(2031, 7, 31, 0, 0, 0, 0, time.UTC), Label: "SLES 15"},

	// Linux Mint (based on Ubuntu LTS)
	{Distro: "linuxmint", Version: "20", EOL: time.Date(2025, 4, 25, 0, 0, 0, 0, time.UTC), Label: "Linux Mint 20.x"},
	{Distro: "linuxmint", Version: "21", EOL: time.Date(2027, 4, 30, 0, 0, 0, 0, time.UTC), Label: "Linux Mint 21.x"},
	{Distro: "linuxmint", Version: "22", EOL: time.Date(2029, 4, 25, 0, 0, 0, 0, time.UTC), Label: "Linux Mint 22.x"},

	// Oracle Linux
	{Distro: "ol", Version: "7", EOL: time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), Label: "Oracle Linux 7"},
	{Distro: "ol", Version: "8", EOL: time.Date(2029, 7, 31, 0, 0, 0, 0, time.UTC), Label: "Oracle Linux 8"},
	{Distro: "ol", Version: "9", EOL: time.Date(2032, 6, 30, 0, 0, 0, 0, time.UTC), Label: "Oracle Linux 9"},
}

// checkEOL returns the EOL entry if the distro+version is past its end-of-life date,
// or nil if it's still supported or not in the database.
func checkEOL(distro, version string) *eolEntry {
	now := time.Now()
	for i := range eolDatabase {
		e := &eolDatabase[i]
		if strings.EqualFold(e.Distro, distro) && strings.HasPrefix(version, e.Version) {
			if now.After(e.EOL) {
				return e
			}
			return nil // found but not yet EOL
		}
	}
	return nil // not in database
}

// checkEOLSoon returns the EOL entry if the distro+version will reach EOL within
// the given duration, or nil otherwise.
func checkEOLSoon(distro, version string, within time.Duration) *eolEntry {
	now := time.Now()
	cutoff := now.Add(within)
	for i := range eolDatabase {
		e := &eolDatabase[i]
		if strings.EqualFold(e.Distro, distro) && strings.HasPrefix(version, e.Version) {
			if e.EOL.After(now) && e.EOL.Before(cutoff) {
				return e
			}
			return nil
		}
	}
	return nil
}
