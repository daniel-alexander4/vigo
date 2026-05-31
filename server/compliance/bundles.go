// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliance

import "sort"

// ConfigcrateBundle is one framework-scope-cut a module implements. Bundles
// are the sibling concept to ConfigcrateProvision — provisions name what a
// module *does* (functional capability that may satisfy controls across
// many frameworks); bundles name *which framework section* a module
// satisfies (e.g. CIS-Ubuntu §5 access controls, Cyber Essentials Plus
// firewall theme, ABA-Cyber audit-logging baseline).
//
// The split is deliberate. A `provides: [time-sync]` claim is
// framework-agnostic — the underlying module ships a chrony daemon and
// happens to satisfy controls in HIPAA, PCI, NIST, ISO, CIS. A
// `bundle: [cis-ubuntu-access-baseline]` claim is framework-specific —
// the underlying module exists to implement CIS-Ubuntu's access-control
// rule set, and its cross-walks to other frameworks are derived from
// CIS's own published mappings rather than from the module's
// functional behavior. Operators reading a module header should be
// able to tell at a glance which mental model the module is operating
// under.
//
// Structurally a ConfigcrateBundle is identical to a ConfigcrateProvision for
// v1. Bundles get their own type so they can grow bundle-specific
// fields (baseline version pin, primary framework slug, last-reviewed
// date) without touching provisions.
type ConfigcrateBundle struct {
	// ID is the kebab-case tag operators write in YAML. Matches the
	// map key. Bundle IDs carry the framework prefix so the operator
	// can scan a module header and see which framework section it
	// implements without consulting the catalog (e.g.
	// "cis-ubuntu-access-baseline" vs "ce-firewalls").
	ID string

	// Title is the human label shown by `vigocli compliance bundles`.
	Title string

	// Description is one or two sentences explaining what a module
	// claiming this bundle delivers — operators read this to decide
	// whether their module actually satisfies it.
	Description string

	// Frameworks maps each framework slug (matching
	// server/config.StandardKeyForSlug values) to the control IDs this
	// bundle satisfies in that framework. The primary framework
	// (matching the bundle ID's prefix) always appears; cross-walks to
	// other frameworks are added per the framework's own published
	// mappings (CIS → NIST CSF → PCI, etc.).
	Frameworks map[string][]string
}

// ConfigcrateBundles is the source-of-truth catalog. Keys are the
// kebab-case bundle IDs that operators reference under `bundle:`.
// Authored by collapsing each framework-bundle module under
// example-configs/worksite/stockpile/modules/compliance/ into a single
// catalog entry whose Frameworks map captures the module's historical
// claim verbatim.
var ConfigcrateBundles = map[string]ConfigcrateBundle{
	"aba-audit-logging": {
		ID:          "aba-audit-logging",
		Title:       "ABA — Audit Logging",
		Description: "Framework-bundle scope-cut: aba-audit-logging. Implements one section of the aba baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"aba-cyber": {"ABA-477R-9"},
		},
	},
	"aba-endpoint-hardening": {
		ID:          "aba-endpoint-hardening",
		Title:       "ABA — Endpoint Hardening",
		Description: "Framework-bundle scope-cut: aba-endpoint-hardening. Implements one section of the aba baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"aba-cyber": {"ABA-477R-4", "ABA-1.6-1", "ABA-1.6-3", "ABA-1.6-5"},
		},
	},
	"aba-malware-protection": {
		ID:          "aba-malware-protection",
		Title:       "ABA — Malware Protection",
		Description: "Framework-bundle scope-cut: aba-malware-protection. Implements one section of the aba baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"aba-cyber": {"ABA-477R-8"},
		},
	},
	"aba-network-security": {
		ID:          "aba-network-security",
		Title:       "ABA — Network Security",
		Description: "Framework-bundle scope-cut: aba-network-security. Implements one section of the aba baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"aba-cyber": {"ABA-477R-3", "ABA-477R-7", "ABA-1.6-2"},
		},
	},
	"ccss-audit-integrity": {
		ID:          "ccss-audit-integrity",
		Title:       "CCSS — Audit Integrity",
		Description: "Framework-bundle scope-cut: ccss-audit-integrity. Implements one section of the ccss baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ccss": {"CCSS-8.1", "CCSS-8.2", "CCSS-9.1", "CCSS-10.1"},
		},
	},
	"ccss-key-storage": {
		ID:          "ccss-key-storage",
		Title:       "CCSS — Key Storage",
		Description: "Framework-bundle scope-cut: ccss-key-storage. Implements one section of the ccss baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ccss": {"CCSS-2.1", "CCSS-2.2", "CCSS-2.3", "CCSS-2.4"},
		},
	},
	"ccss-network-security": {
		ID:          "ccss-network-security",
		Title:       "CCSS — Network Security",
		Description: "Framework-bundle scope-cut: ccss-network-security. Implements one section of the ccss baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ccss": {"CCSS-3.1", "CCSS-3.2", "CCSS-6.1", "CCSS-6.2"},
		},
	},
	"cis-rhel-access": {
		ID:          "cis-rhel-access",
		Title:       "CIS — Rhel Access",
		Description: "Framework-bundle scope-cut: cis-rhel-access. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"5.1.1", "5.1.2", "5.1.3", "5.1.4", "5.1.5", "5.1.6", "5.1.7", "5.1.8", "5.2.1", "5.2.2", "5.2.3", "5.2.4", "5.2.5", "5.2.6", "5.2.7", "5.2.8", "5.2.9", "5.2.10", "5.2.11", "5.2.12", "5.2.13", "5.2.14", "5.2.15", "5.2.16", "5.2.17", "5.2.18", "5.2.19", "5.2.20", "5.3.1", "5.3.2", "5.3.3", "5.3.4", "5.4.1", "5.4.2", "5.4.3", "5.4.4", "5.4.5", "5.5.1.1", "5.5.1.2", "5.5.1.3", "5.5.1.4", "5.5.1.5", "5.5.2", "5.5.3", "5.5.4", "5.5.5"},
			"hipaa": {"164.312(d)", "164.312(e)(1)"},
			"iso-27001": {"A.9.2.1", "A.9.2.4", "A.9.4.1", "A.13.1.1"},
			"nist-800-53": {"AC-17", "IA-2", "IA-5", "SC-8"},
			"pci-dss": {"2.2.1", "2.2.4", "8.2.3", "8.3.1", "8.3.6"},
		},
	},
	"cis-rhel-filesystem": {
		ID:          "cis-rhel-filesystem",
		Title:       "CIS — Rhel Filesystem",
		Description: "Framework-bundle scope-cut: cis-rhel-filesystem. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4", "1.1.1.5", "1.1.1.6", "1.1.1.7", "1.1.1.8", "1.1.2", "1.1.3", "1.1.4", "1.1.5", "1.4.1", "1.4.2", "1.5.1", "1.5.2", "1.5.3"},
			"nist-800-53": {"CM-6", "CM-7", "SC-7"},
			"pci-dss": {"2.2.1", "2.2.5"},
		},
	},
	"cis-rhel-logging": {
		ID:          "cis-rhel-logging",
		Title:       "CIS — Rhel Logging",
		Description: "Framework-bundle scope-cut: cis-rhel-logging. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"4.1.1.1", "4.1.1.2", "4.1.1.3", "4.1.1.4", "4.1.2", "4.1.3", "4.1.4", "4.1.5", "4.1.6", "4.1.7", "4.1.8", "4.1.9", "4.1.10", "4.1.11", "4.1.12", "4.1.13", "4.1.14", "4.1.15", "4.1.16", "4.1.17", "4.2.1.1", "4.2.1.2", "4.2.1.3", "4.2.3"},
			"hipaa": {"164.312(b)"},
			"iso-27001": {"A.12.4.1", "A.12.4.3"},
			"nist-800-53": {"AU-2", "AU-3", "AU-12"},
			"pci-dss": {"10.2.1", "10.2.2", "10.2.3", "10.2.4", "10.2.5", "10.2.6", "10.2.7"},
			"soc2": {"CC7.2"},
		},
	},
	"cis-rhel-maintenance": {
		ID:          "cis-rhel-maintenance",
		Title:       "CIS — Rhel Maintenance",
		Description: "Framework-bundle scope-cut: cis-rhel-maintenance. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"6.1.1", "6.1.2", "6.1.3", "6.1.4", "6.1.5", "6.1.6", "6.1.7", "6.1.8", "6.1.9", "6.1.10", "6.2.1", "6.2.2", "6.2.3", "6.2.5"},
			"nist-800-53": {"CM-6", "AC-3"},
			"pci-dss": {"2.2.1", "7.1.1"},
		},
	},
	"cis-rhel-network": {
		ID:          "cis-rhel-network",
		Title:       "CIS — Rhel Network",
		Description: "Framework-bundle scope-cut: cis-rhel-network. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"3.1.1", "3.1.2", "3.2.1", "3.2.2", "3.2.3", "3.2.4", "3.2.5", "3.2.6", "3.2.7", "3.2.8", "3.2.9", "3.3.1", "3.3.2", "3.3.3", "3.4.1.1", "3.4.1.2", "3.4.2.1", "3.4.2.2", "3.4.2.3", "3.4.2.4", "3.4.2.5", "3.4.2.6"},
			"iso-27001": {"A.13.1.1"},
			"nist-800-53": {"SC-7", "CM-6"},
			"pci-dss": {"1.3.1", "1.3.2", "2.2.1"},
		},
	},
	"cis-rhel-services": {
		ID:          "cis-rhel-services",
		Title:       "CIS — Rhel Services",
		Description: "Framework-bundle scope-cut: cis-rhel-services. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"2.1.1", "2.1.2", "2.1.3", "2.1.4", "2.1.5", "2.1.6", "2.1.7", "2.1.8", "2.1.9", "2.1.10", "2.1.11", "2.2.1", "2.2.2", "2.2.3", "2.2.4", "2.2.5"},
			"nist-800-53": {"CM-7"},
			"pci-dss": {"2.2.2", "2.2.3"},
		},
	},
	"cis-ubuntu-access": {
		ID:          "cis-ubuntu-access",
		Title:       "CIS — Ubuntu Access",
		Description: "Framework-bundle scope-cut: cis-ubuntu-access. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-ubuntu": {"5.1.1", "5.1.2", "5.1.3", "5.1.4", "5.1.5", "5.1.6", "5.1.7", "5.1.8", "5.2.1", "5.2.2", "5.2.3", "5.2.4", "5.2.5", "5.2.6", "5.2.7", "5.2.8", "5.2.9", "5.2.10", "5.2.11", "5.2.12", "5.2.13", "5.2.14", "5.2.15", "5.2.16", "5.2.17", "5.2.18", "5.2.19", "5.2.20", "5.2.21", "5.2.22", "5.2.23", "5.3.1", "5.3.2", "5.3.3", "5.3.4", "5.4.1.1", "5.4.1.2", "5.4.1.3", "5.4.1.4", "5.4.1.5", "5.4.2", "5.4.3", "5.4.4", "5.4.5"},
			"hipaa": {"164.312(d)", "164.312(e)(1)"},
			"iso-27001": {"A.9.2.1", "A.9.2.4", "A.9.4.1", "A.13.1.1"},
			"nist-800-53": {"AC-17", "IA-2", "IA-5", "SC-8"},
			"pci-dss": {"2.2.1", "2.2.4", "8.2.3", "8.3.1", "8.3.6"},
		},
	},
	"cis-ubuntu-filesystem": {
		ID:          "cis-ubuntu-filesystem",
		Title:       "CIS — Ubuntu Filesystem",
		Description: "Framework-bundle scope-cut: cis-ubuntu-filesystem. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-ubuntu": {"1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4", "1.1.1.5", "1.1.1.6", "1.1.1.7", "1.1.1.8", "1.1.2", "1.1.3", "1.1.4", "1.1.5", "1.4.1", "1.4.2", "1.4.3", "1.5.1", "1.5.2", "1.5.3", "1.5.4"},
			"nist-800-53": {"CM-6", "CM-7", "SC-7"},
			"pci-dss": {"2.2.1", "2.2.5"},
		},
	},
	"cis-ubuntu-logging": {
		ID:          "cis-ubuntu-logging",
		Title:       "CIS — Ubuntu Logging",
		Description: "Framework-bundle scope-cut: cis-ubuntu-logging. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-ubuntu": {"4.1.1.1", "4.1.1.2", "4.1.1.3", "4.1.2", "4.1.3", "4.1.4", "4.1.5", "4.1.6", "4.1.7", "4.1.8", "4.1.9", "4.1.10", "4.1.11", "4.1.12", "4.1.13", "4.1.14", "4.1.15", "4.1.16", "4.1.17", "4.2.1.1", "4.2.1.2", "4.2.1.3", "4.2.1.4", "4.2.2.1", "4.2.3"},
			"hipaa": {"164.312(b)"},
			"iso-27001": {"A.12.4.1", "A.12.4.3"},
			"nist-800-53": {"AU-2", "AU-3", "AU-12"},
			"pci-dss": {"10.2.1", "10.2.2", "10.2.3", "10.2.4", "10.2.5", "10.2.6", "10.2.7"},
			"soc2": {"CC7.2"},
		},
	},
	"cis-ubuntu-maintenance": {
		ID:          "cis-ubuntu-maintenance",
		Title:       "CIS — Ubuntu Maintenance",
		Description: "Framework-bundle scope-cut: cis-ubuntu-maintenance. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-ubuntu": {"6.1.1", "6.1.2", "6.1.3", "6.1.4", "6.1.5", "6.1.6", "6.1.7", "6.1.8", "6.1.9", "6.1.10", "6.2.1", "6.2.2", "6.2.3", "6.2.4", "6.2.5", "6.2.6", "6.2.7"},
			"nist-800-53": {"CM-6", "AC-3"},
			"pci-dss": {"2.2.1", "7.1.1"},
		},
	},
	"cis-ubuntu-network": {
		ID:          "cis-ubuntu-network",
		Title:       "CIS — Ubuntu Network",
		Description: "Framework-bundle scope-cut: cis-ubuntu-network. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-ubuntu": {"3.1.1", "3.1.2", "3.2.1", "3.2.2", "3.2.3", "3.2.4", "3.2.5", "3.2.6", "3.2.7", "3.2.8", "3.2.9", "3.3.1", "3.3.2", "3.3.3", "3.3.4", "3.4.1", "3.4.2", "3.4.3", "3.4.4"},
			"iso-27001": {"A.13.1.1"},
			"nist-800-53": {"SC-7", "CM-6"},
			"pci-dss": {"1.3.1", "1.3.2", "2.2.1"},
		},
	},
	"cis-ubuntu-services": {
		ID:          "cis-ubuntu-services",
		Title:       "CIS — Ubuntu Services",
		Description: "Framework-bundle scope-cut: cis-ubuntu-services. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-ubuntu": {"2.1.1", "2.1.2", "2.1.3", "2.1.4", "2.1.5", "2.1.6", "2.1.7", "2.1.8", "2.1.9", "2.1.10", "2.1.11", "2.1.12", "2.1.13", "2.1.14", "2.1.15", "2.1.16", "2.1.17", "2.2.1", "2.2.2", "2.2.3", "2.2.4", "2.2.5", "2.2.6"},
			"nist-800-53": {"CM-7"},
			"pci-dss": {"2.2.2", "2.2.3"},
		},
	},
	"cis-windows-account-policies": {
		ID:          "cis-windows-account-policies",
		Title:       "CIS — Windows Account Policies",
		Description: "Framework-bundle scope-cut: cis-windows-account-policies. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"1.1.1", "1.1.2", "1.1.3", "1.1.4", "1.1.5", "1.2.1", "1.2.2", "1.2.3"},
		},
	},
	"cis-windows-audit-policies": {
		ID:          "cis-windows-audit-policies",
		Title:       "CIS — Windows Audit Policies",
		Description: "Framework-bundle scope-cut: cis-windows-audit-policies. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"17.1.1", "17.2.1", "17.3.1", "17.3.2", "17.5.1", "17.6.1", "17.7.1", "17.9.1"},
		},
	},
	"cis-windows-components": {
		ID:          "cis-windows-components",
		Title:       "CIS — Windows Components",
		Description: "Framework-bundle scope-cut: cis-windows-components. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"18.1.1", "18.3.1", "18.3.2", "18.8.1", "18.8.2", "18.8.3", "18.9.1", "18.9.2", "18.9.3", "18.9.4", "18.9.5", "18.9.6"},
		},
	},
	"cis-windows-firewall": {
		ID:          "cis-windows-firewall",
		Title:       "CIS — Windows Firewall",
		Description: "Framework-bundle scope-cut: cis-windows-firewall. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"9.1.1", "9.1.2", "9.1.3", "9.2.1", "9.2.2", "9.3.1", "9.3.2"},
		},
	},
	"cis-windows-network": {
		ID:          "cis-windows-network",
		Title:       "CIS — Windows Network",
		Description: "Framework-bundle scope-cut: cis-windows-network. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"18.4.1", "18.4.2", "18.4.3", "18.4.4", "18.4.5", "18.4.6", "18.4.7", "18.4.8"},
		},
	},
	"cis-windows-security-options": {
		ID:          "cis-windows-security-options",
		Title:       "CIS — Windows Security Options",
		Description: "Framework-bundle scope-cut: cis-windows-security-options. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"2.3.1.1", "2.3.7.1", "2.3.10.1", "2.3.10.2", "18.4.1", "18.9.47.1"},
		},
	},
	"cis-windows-system-services": {
		ID:          "cis-windows-system-services",
		Title:       "CIS — Windows System Services",
		Description: "Framework-bundle scope-cut: cis-windows-system-services. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"5.1", "5.2", "5.3", "5.4", "5.5", "5.6", "5.7", "5.8", "5.9", "5.10"},
		},
	},
	"cis-windows-user-rights": {
		ID:          "cis-windows-user-rights",
		Title:       "CIS — Windows User Rights",
		Description: "Framework-bundle scope-cut: cis-windows-user-rights. Implements one section of the cis baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-windows": {"2.2.1", "2.2.2", "2.2.3", "2.2.4", "2.2.5", "2.2.6", "2.2.7", "2.2.8", "2.2.9", "2.2.10", "2.2.11", "2.2.12"},
		},
	},
	"ce-app-whitelisting": {
		ID:          "ce-app-whitelisting",
		Title:       "CE — App Whitelisting",
		Description: "Framework-bundle scope-cut: ce-app-whitelisting. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-MW-4", "CE-MW-5"},
			"iso-27001": {"A.12.5.1"},
			"nist-800-53": {"CM-11"},
		},
	},
	"ce-firewalls": {
		ID:          "ce-firewalls",
		Title:       "CE — Firewalls",
		Description: "Framework-bundle scope-cut: ce-firewalls. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-FW-1", "CE-FW-2", "CE-FW-3", "CE-FW-4", "CE-FW-5"},
			"iso-27001": {"A.13.1.1"},
			"nist-800-53": {"SC-7"},
			"pci-dss": {"1.2.1", "1.2.5", "1.3.1"},
		},
	},
	"ce-malware": {
		ID:          "ce-malware",
		Title:       "CE — Malware",
		Description: "Framework-bundle scope-cut: ce-malware. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-MW-1", "CE-MW-2", "CE-MW-3"},
			"hitrust": {"09.i"},
			"iso-27001": {"A.12.2.1"},
			"nist-800-53": {"SI-3"},
			"pci-dss": {"5.2.1", "5.2.2", "5.3.1"},
		},
	},
	"ce-mfa": {
		ID:          "ce-mfa",
		Title:       "CE — Mfa",
		Description: "Framework-bundle scope-cut: ce-mfa. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-AC-4"},
			"hitrust": {"01.i"},
			"iso-27001": {"A.9.4.2"},
			"nist-800-53": {"IA-2", "IA-8"},
			"pci-dss": {"8.3.1", "8.3.6"},
		},
	},
	"ce-monitoring": {
		ID:          "ce-monitoring",
		Title:       "CE — Monitoring",
		Description: "Framework-bundle scope-cut: ce-monitoring. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-ML-3"},
			"hitrust": {"11.a"},
			"iso-27001": {"A.16.1.7"},
			"nist-800-53": {"IR-4", "CA-7"},
			"soc2": {"CC7.5"},
		},
	},
	"ce-patching": {
		ID:          "ce-patching",
		Title:       "CE — Patching",
		Description: "Framework-bundle scope-cut: ce-patching. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-SU-1", "CE-SU-2", "CE-SU-3", "CE-SU-4"},
			"iso-27001": {"A.12.6.1"},
			"nist-800-53": {"SI-2"},
			"pci-dss": {"6.3.3"},
		},
	},
	"ce-secure-config": {
		ID:          "ce-secure-config",
		Title:       "CE — Secure Config",
		Description: "Framework-bundle scope-cut: ce-secure-config. Implements one section of the ce baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"1.1.1", "5.2.x"},
			"cis-ubuntu": {"1.1.1", "5.2.x"},
			"cyber-essentials": {"CE-SC-1", "CE-SC-2", "CE-SC-3", "CE-SC-4", "CE-SC-5", "CE-SC-6"},
			"nist-800-53": {"CM-6", "CM-7"},
			"pci-dss": {"2.2.1", "2.2.4", "2.2.5"},
		},
	},
	"access-review-export-windows": {
		ID:          "access-review-export-windows",
		Title:       "ACCESS — Review Export Windows",
		Description: "Framework-bundle scope-cut: access-review-export-windows. Implements one section of the access baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.308(a)(3)(ii)(C)", "164.308(a)(4)(ii)(C)"},
			"iso-27001": {"A.9.2.5"},
			"nist-800-53": {"AC-2", "AC-2(7)", "AC-6"},
			"pci-dss": {"7.1.4", "7.2.1"},
			"soc2": {"CC6.2", "CC6.3"},
		},
	},
	"access-review-export": {
		ID:          "access-review-export",
		Title:       "ACCESS — Review Export",
		Description: "Framework-bundle scope-cut: access-review-export. Implements one section of the access baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.308(a)(3)(ii)(C)", "164.308(a)(4)(ii)(C)"},
			"iso-27001": {"A.9.2.5"},
			"nist-800-53": {"AC-2", "AC-2(7)", "AC-6"},
			"pci-dss": {"7.1.4", "7.2.1"},
			"soc2": {"CC6.2", "CC6.3"},
		},
	},
	"backup-verification-windows": {
		ID:          "backup-verification-windows",
		Title:       "BACKUP — Verification Windows",
		Description: "Framework-bundle scope-cut: backup-verification-windows. Implements one section of the backup baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.308(a)(7)(ii)(D)"},
			"iso-27001": {"A.12.3.1", "A.17.1.3"},
			"nist-800-53": {"CP-9", "CP-10(2)", "CP-10(4)"},
			"pci-dss": {"12.10.1"},
			"soc2": {"A1.3"},
		},
	},
	"backup-verification": {
		ID:          "backup-verification",
		Title:       "BACKUP — Verification",
		Description: "Framework-bundle scope-cut: backup-verification. Implements one section of the backup baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.308(a)(7)(ii)(D)"},
			"iso-27001": {"A.12.3.1", "A.17.1.3"},
			"nist-800-53": {"CP-9", "CP-10(2)", "CP-10(4)"},
			"pci-dss": {"12.10.1"},
			"soc2": {"A1.3"},
		},
	},
	"encryption-attestation-windows": {
		ID:          "encryption-attestation-windows",
		Title:       "ENCRYPTION — Attestation Windows",
		Description: "Framework-bundle scope-cut: encryption-attestation-windows. Implements one section of the encryption baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(a)(2)(iv)", "164.312(e)(2)(ii)"},
			"iso-27001": {"A.10.1.1", "A.10.1.2"},
			"nist-800-53": {"SC-28", "SC-28(1)"},
			"pci-dss": {"3.4", "3.4.1"},
			"soc2": {"CC6.1"},
		},
	},
	"encryption-attestation": {
		ID:          "encryption-attestation",
		Title:       "ENCRYPTION — Attestation",
		Description: "Framework-bundle scope-cut: encryption-attestation. Implements one section of the encryption baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(a)(2)(iv)", "164.312(e)(2)(ii)"},
			"iso-27001": {"A.10.1.1", "A.10.1.2"},
			"nist-800-53": {"SC-28", "SC-28(1)"},
			"pci-dss": {"3.4", "3.4.1"},
			"soc2": {"CC6.1"},
		},
	},
	"log-shipping-attestation-windows": {
		ID:          "log-shipping-attestation-windows",
		Title:       "LOG — Shipping Attestation Windows",
		Description: "Framework-bundle scope-cut: log-shipping-attestation-windows. Implements one section of the log baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(b)", "164.308(a)(1)(ii)(D)"},
			"iso-27001": {"A.12.4.2", "A.12.4.3"},
			"nist-800-53": {"AU-9", "AU-9(2)", "AU-11"},
			"pci-dss": {"10.5.3", "10.5.4"},
			"soc2": {"CC7.2"},
		},
	},
	"log-shipping-attestation": {
		ID:          "log-shipping-attestation",
		Title:       "LOG — Shipping Attestation",
		Description: "Framework-bundle scope-cut: log-shipping-attestation. Implements one section of the log baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(b)", "164.308(a)(1)(ii)(D)"},
			"iso-27001": {"A.12.4.2", "A.12.4.3"},
			"nist-800-53": {"AU-9", "AU-9(2)", "AU-11"},
			"pci-dss": {"10.5.3", "10.5.4"},
			"soc2": {"CC7.2"},
		},
	},
	"fda-access-controls": {
		ID:          "fda-access-controls",
		Title:       "FDA — Access Controls",
		Description: "Framework-bundle scope-cut: fda-access-controls. Implements one section of the fda baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"fda-21cfr11": {"11.10-d", "11.10-g", "11.300-a", "11.300-b"},
		},
	},
	"fda-audit-trail": {
		ID:          "fda-audit-trail",
		Title:       "FDA — Audit Trail",
		Description: "Framework-bundle scope-cut: fda-audit-trail. Implements one section of the fda baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"fda-21cfr11": {"11.10-e", "11.10-k-2", "11.10-e-1"},
		},
	},
	"fda-system-integrity": {
		ID:          "fda-system-integrity",
		Title:       "FDA — System Integrity",
		Description: "Framework-bundle scope-cut: fda-system-integrity. Implements one section of the fda baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"fda-21cfr11": {"11.10-a", "11.10-c", "11.10-h"},
		},
	},
	"finra-bcp-monitoring": {
		ID:          "finra-bcp-monitoring",
		Title:       "FINRA — Bcp Monitoring",
		Description: "Framework-bundle scope-cut: finra-bcp-monitoring. Implements one section of the finra baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"finra": {"BCP-1", "BCP-2", "BCP-3"},
		},
	},
	"finra-infrastructure": {
		ID:          "finra-infrastructure",
		Title:       "FINRA — Infrastructure",
		Description: "Framework-bundle scope-cut: finra-infrastructure. Implements one section of the finra baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"finra": {"Rule-4370-1", "Rule-4370-2", "Rule-4370-3"},
		},
	},
	"finra-worm-monitoring": {
		ID:          "finra-worm-monitoring",
		Title:       "FINRA — Worm Monitoring",
		Description: "Framework-bundle scope-cut: finra-worm-monitoring. Implements one section of the finra baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"finra": {"SEC-17a4-1", "SEC-17a4-2"},
		},
	},
	"gaming-availability": {
		ID:          "gaming-availability",
		Title:       "GAMING — Availability",
		Description: "Framework-bundle scope-cut: gaming-availability. Implements one section of the gaming baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gaming": {"NGC-TS-2.1", "NGC-TS-2.2", "MGA-RGR-5.1", "MGA-RGR-5.2", "UKGC-RTS-5A", "UKGC-RTS-5B"},
		},
	},
	"gaming-rng-monitoring": {
		ID:          "gaming-rng-monitoring",
		Title:       "GAMING — Rng Monitoring",
		Description: "Framework-bundle scope-cut: gaming-rng-monitoring. Implements one section of the gaming baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gaming": {"NGC-TS-4.1", "NGC-TS-4.2", "MGA-RGR-6.1", "UKGC-RTS-6A"},
		},
	},
	"gaming-system-integrity": {
		ID:          "gaming-system-integrity",
		Title:       "GAMING — System Integrity",
		Description: "Framework-bundle scope-cut: gaming-system-integrity. Implements one section of the gaming baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gaming": {"NGC-TS-1.1", "NGC-TS-1.2", "NGC-TS-3.1", "MGA-RGR-4.1", "MGA-RGR-4.2", "UKGC-RTS-4A", "UKGC-RTS-4B"},
		},
	},
	"gdpr-breach-notification": {
		ID:          "gdpr-breach-notification",
		Title:       "GDPR — Breach Notification",
		Description: "Framework-bundle scope-cut: gdpr-breach-notification. Implements one section of the gdpr baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gdpr": {"Art.33(1)", "Art.33(3)"},
			"hipaa": {"164.308(a)(6)"},
			"iso-27001": {"A.16.1.2", "A.16.1.5"},
			"nist-800-53": {"IR-5", "IR-6"},
			"soc2": {"CC7.3", "CC7.4"},
		},
	},
	"gdpr-data-protection": {
		ID:          "gdpr-data-protection",
		Title:       "GDPR — Data Protection",
		Description: "Framework-bundle scope-cut: gdpr-data-protection. Implements one section of the gdpr baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gdpr": {"Art.5(1)(f)", "Art.25(1)", "Art.32(1)(a)", "Art.32(1)(b)"},
			"hipaa": {"164.312(a)(2)(iv)", "164.312(e)(1)"},
			"iso-27001": {"A.10.1.1", "A.18.1.3"},
			"nist-800-53": {"SC-13", "SC-28"},
		},
	},
	"gdpr-privacy-controls": {
		ID:          "gdpr-privacy-controls",
		Title:       "GDPR — Privacy Controls",
		Description: "Framework-bundle scope-cut: gdpr-privacy-controls. Implements one section of the gdpr baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gdpr": {"Art.25(2)", "Art.30(1)", "Art.32(1)(c)", "Art.32(1)(d)", "Art.35(1)"},
			"hitrust": {"10.k", "10.l", "10.m"},
			"iso-27001": {"A.12.3.1", "A.18.2.2", "A.18.2.3"},
			"nist-800-53": {"CP-9", "CP-10"},
			"soc2": {"A1.2", "PI1.1", "PI1.2"},
		},
	},
	"hipaa-access-controls": {
		ID:          "hipaa-access-controls",
		Title:       "HIPAA — Access Controls",
		Description: "Framework-bundle scope-cut: hipaa-access-controls. Implements one section of the hipaa baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-AC-1", "CE-AC-3", "CE-AC-5"},
			"hipaa": {"164.312(a)(1)", "164.312(a)(2)(i)", "164.312(a)(2)(iii)", "164.312(d)"},
			"hitrust": {"01.b", "01.d", "01.j"},
			"iso-27001": {"A.9.2.1", "A.9.4.1"},
			"nist-800-53": {"AC-2", "AC-3", "AC-7", "IA-2", "IA-5"},
			"soc2": {"CC6.1", "CC6.2", "CC6.3"},
		},
	},
	"hipaa-audit-trail": {
		ID:          "hipaa-audit-trail",
		Title:       "HIPAA — Audit Trail",
		Description: "Framework-bundle scope-cut: hipaa-audit-trail. Implements one section of the hipaa baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(b)", "164.312(c)(1)"},
			"hitrust": {"09.q", "09.s"},
			"nist-800-53": {"AU-2", "AU-3", "AU-6", "AU-9"},
			"pci-dss": {"10.2.1", "10.2.1.1", "10.2.1.2", "10.2.1.3", "10.3.2"},
			"soc2": {"CC7.2", "CC7.3"},
		},
	},
	"hipaa-encryption": {
		ID:          "hipaa-encryption",
		Title:       "HIPAA — Encryption",
		Description: "Framework-bundle scope-cut: hipaa-encryption. Implements one section of the hipaa baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gdpr": {"Art.32(1)(a)"},
			"hipaa": {"164.312(a)(2)(iv)", "164.312(e)(1)", "164.312(e)(2)(ii)"},
			"iso-27001": {"A.10.1.1"},
			"nist-800-53": {"SC-13", "SC-28"},
			"pci-dss": {"3.5.1", "3.5.1.1"},
			"soc2": {"CC6.1"},
		},
	},
	"hipaa-media-disposal": {
		ID:          "hipaa-media-disposal",
		Title:       "HIPAA — Media Disposal",
		Description: "Framework-bundle scope-cut: hipaa-media-disposal. Implements one section of the hipaa baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gdpr": {"Art.17(1)"},
			"hipaa": {"164.310(d)(2)(i)"},
			"iso-27001": {"A.8.1", "A.8.2"},
			"nerc-cip": {"CIP-011-2-R2"},
			"nist-800-53": {"SC-28"},
		},
	},
	"hitrust-endpoint-protection": {
		ID:          "hitrust-endpoint-protection",
		Title:       "HITRUST — Endpoint Protection",
		Description: "Framework-bundle scope-cut: hitrust-endpoint-protection. Implements one section of the hitrust baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-MW-1", "CE-MW-2", "CE-MW-3"},
			"hipaa": {"164.308(a)(5)(ii)(D)"},
			"hitrust": {"09.i", "09.k", "01.n"},
			"iso-27001": {"A.12.2.1"},
			"nist-800-53": {"SI-3", "SI-4"},
		},
	},
	"hitrust-governance": {
		ID:          "hitrust-governance",
		Title:       "HITRUST — Governance",
		Description: "Framework-bundle scope-cut: hitrust-governance. Implements one section of the hitrust baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hitrust": {"06.c", "06.g", "06.h", "06.i", "07.a", "09.b", "09.c", "09.d", "09.l", "10.f", "10.g", "10.k", "10.l", "10.m"},
			"iso-27001": {"A.5.1", "A.6.1", "A.14.1.2", "A.14.2.2", "A.18.2.2", "A.18.2.3"},
			"nist-800-53": {"CM-8", "CA-7"},
			"soc2": {"CC3.1", "CC3.2", "CC3.3", "CC4.1", "CC4.2", "CC5.1", "CC5.2", "CC5.3"},
		},
	},
	"hitrust-operations": {
		ID:          "hitrust-operations",
		Title:       "HITRUST — Operations",
		Description: "Framework-bundle scope-cut: hitrust-operations. Implements one section of the hitrust baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(b)"},
			"hitrust": {"09.q", "09.s", "09.v", "10.h"},
			"iso-27001": {"A.12.4.1", "A.12.4.3"},
			"nist-800-53": {"AU-2", "AU-6", "AU-9", "SI-7"},
			"soc2": {"CC7.2", "CC7.3"},
		},
	},
	"iec62443-access-control": {
		ID:          "iec62443-access-control",
		Title:       "IEC62443 — Access Control",
		Description: "Framework-bundle scope-cut: iec62443-access-control. Implements one section of the iec62443 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iec-62443": {"SR 1.1", "SR 1.2", "SR 1.3", "SR 1.5", "SR 1.7", "SR 1.8", "SR 1.9", "SR 1.10", "SR 2.1", "SR 2.2"},
			"iso-27001": {"A.9.2.1", "A.9.2.3", "A.9.4.1", "A.9.4.3"},
			"nerc-cip": {"CIP-004-6-R4", "CIP-007-6-R5"},
			"nist-800-53": {"AC-2", "AC-3", "AC-7", "IA-2", "IA-5"},
			"pci-dss": {"7.2.1", "8.2.1", "8.3.1", "8.3.6"},
		},
	},
	"iec62443-audit-logging": {
		ID:          "iec62443-audit-logging",
		Title:       "IEC62443 — Audit Logging",
		Description: "Framework-bundle scope-cut: iec62443-audit-logging. Implements one section of the iec62443 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(b)"},
			"iec-62443": {"SR 6.1", "SR 6.2", "SR 2.8", "SR 2.9", "SR 2.10", "SR 2.11", "SR 2.12"},
			"iso-27001": {"A.12.4.1", "A.12.4.2", "A.12.4.3"},
			"nerc-cip": {"CIP-007-6-R4"},
			"nist-800-53": {"AU-2", "AU-3", "AU-4", "AU-5", "AU-6", "AU-9", "AU-12"},
			"pci-dss": {"10.2.1", "10.2.2", "10.3.1", "10.5.1"},
		},
	},
	"iec62443-availability": {
		ID:          "iec62443-availability",
		Title:       "IEC62443 — Availability",
		Description: "Framework-bundle scope-cut: iec62443-availability. Implements one section of the iec62443 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iec-62443": {"SR 7.1", "SR 7.2", "SR 7.3", "SR 7.4", "SR 7.6", "SR 7.7", "SR 7.8", "SR 4.1", "SR 4.3"},
			"iso-27001": {"A.12.3.1", "A.17.1.1", "A.17.1.2"},
			"nerc-cip": {"CIP-009-6-R1", "CIP-009-6-R2"},
			"nist-800-53": {"CP-9", "CP-10", "SC-5", "SC-8", "SC-28"},
		},
	},
	"iec62443-system-integrity": {
		ID:          "iec62443-system-integrity",
		Title:       "IEC62443 — System Integrity",
		Description: "Framework-bundle scope-cut: iec62443-system-integrity. Implements one section of the iec62443 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iec-62443": {"SR 3.1", "SR 3.2", "SR 3.3", "SR 3.4", "SR 3.5", "SR 3.8", "SR 3.9"},
			"iso-27001": {"A.12.2.1", "A.12.5.1", "A.12.6.1"},
			"nerc-cip": {"CIP-007-6-R2", "CIP-007-6-R3", "CIP-010-3-R1"},
			"nist-800-53": {"SI-2", "SI-3", "SI-7", "CM-3", "CM-6"},
			"pci-dss": {"5.2.1", "6.3.3", "11.5.1"},
		},
	},
	"iec62443-zone-segmentation": {
		ID:          "iec62443-zone-segmentation",
		Title:       "IEC62443 — Zone Segmentation",
		Description: "Framework-bundle scope-cut: iec62443-zone-segmentation. Implements one section of the iec62443 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iec-62443": {"SR 5.1", "SR 5.2", "SR 5.3", "SR 5.4"},
			"iso-27001": {"A.13.1.1", "A.13.1.3"},
			"nerc-cip": {"CIP-005-7-R1", "CIP-005-7-R2"},
			"nist-800-53": {"SC-7", "AC-4", "SC-8"},
		},
	},
	"iso-communications-security": {
		ID:          "iso-communications-security",
		Title:       "ISO — Communications Security",
		Description: "Framework-bundle scope-cut: iso-communications-security. Implements one section of the iso baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-FW-1", "CE-FW-4"},
			"iso-27001": {"A.13.1.1", "A.13.2.1"},
			"nist-800-53": {"SC-7", "SC-8"},
			"pci-dss": {"1.2.1", "1.2.5", "3.5.1.1"},
		},
	},
	"iso-operations-security": {
		ID:          "iso-operations-security",
		Title:       "ISO — Operations Security",
		Description: "Framework-bundle scope-cut: iso-operations-security. Implements one section of the iso baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hitrust": {"09.a", "09.i", "09.q"},
			"iso-27001": {"A.12.1.2", "A.12.2.1", "A.12.4.1", "A.12.5.1", "A.12.6.1"},
			"nist-800-53": {"CM-3", "SI-2", "SI-3", "AU-2"},
			"soc2": {"CC7.1", "CC7.2", "CC8.1"},
		},
	},
	"mifid2-capacity": {
		ID:          "mifid2-capacity",
		Title:       "MIFID2 — Capacity",
		Description: "Framework-bundle scope-cut: mifid2-capacity. Implements one section of the mifid2 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"mifid2": {"RTS7-Art11-1", "RTS7-Art11-2", "RTS7-Art14-1"},
		},
	},
	"mifid2-change-management": {
		ID:          "mifid2-change-management",
		Title:       "MIFID2 — Change Management",
		Description: "Framework-bundle scope-cut: mifid2-change-management. Implements one section of the mifid2 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"mifid2": {"RTS7-Art9-1", "RTS7-Art9-2", "RTS7-Art9-3"},
		},
	},
	"mifid2-resilience": {
		ID:          "mifid2-resilience",
		Title:       "MIFID2 — Resilience",
		Description: "Framework-bundle scope-cut: mifid2-resilience. Implements one section of the mifid2 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"mifid2": {"RTS7-Art5-1", "RTS7-Art5-2", "RTS7-Art5-3"},
		},
	},
	"nerc-access-security": {
		ID:          "nerc-access-security",
		Title:       "NERC — Access Security",
		Description: "Framework-bundle scope-cut: nerc-access-security. Implements one section of the nerc baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hitrust": {"01.b", "01.c"},
			"iso-27001": {"A.9.2.1", "A.9.4.1"},
			"nerc-cip": {"CIP-003-8-R1", "CIP-004-6-R4", "CIP-004-6-R5", "CIP-007-6-R2", "CIP-007-6-R3", "CIP-007-6-R5", "CIP-011-2-R1"},
			"nist-800-53": {"AC-2", "AC-3", "SI-2", "SI-3"},
			"pci-dss": {"7.2.1", "8.2.1", "5.2.1"},
		},
	},
	"nerc-asset-management": {
		ID:          "nerc-asset-management",
		Title:       "NERC — Asset Management",
		Description: "Framework-bundle scope-cut: nerc-asset-management. Implements one section of the nerc baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hitrust": {"07.a"},
			"iso-27001": {"A.8.1", "A.8.2"},
			"nerc-cip": {"CIP-002-5.1", "CIP-003-8-R2", "CIP-011-2-R2"},
			"nist-800-53": {"CM-8"},
			"soc2": {"CC6.8"},
		},
	},
	"nerc-esp": {
		ID:          "nerc-esp",
		Title:       "NERC — Esp",
		Description: "Framework-bundle scope-cut: nerc-esp. Implements one section of the nerc baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-FW-1", "CE-FW-3"},
			"iso-27001": {"A.13.1.1"},
			"nerc-cip": {"CIP-005-7-R1", "CIP-005-7-R2", "CIP-007-6-R1"},
			"nist-800-53": {"SC-7", "AC-17"},
			"pci-dss": {"1.2.1", "1.2.5", "1.3.1"},
		},
	},
	"nerc-recovery": {
		ID:          "nerc-recovery",
		Title:       "NERC — Recovery",
		Description: "Framework-bundle scope-cut: nerc-recovery. Implements one section of the nerc baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iso-27001": {"A.12.3.1", "A.17.1.1"},
			"nerc-cip": {"CIP-009-6-R1", "CIP-009-6-R2", "CIP-010-3-R1", "CIP-010-3-R2"},
			"nist-800-53": {"CP-9", "CP-10", "CM-3"},
			"soc2": {"A1.2"},
		},
	},
	"nist-access-control": {
		ID:          "nist-access-control",
		Title:       "NIST — Access Control",
		Description: "Framework-bundle scope-cut: nist-access-control. Implements one section of the nist baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-AC-1", "CE-AC-2", "CE-AC-6"},
			"hipaa": {"164.312(a)(1)", "164.312(d)"},
			"hitrust": {"01.b", "01.c", "01.d"},
			"iso-27001": {"A.9.1.2", "A.9.2.3", "A.9.4.1"},
			"nist-800-53": {"AC-2", "AC-3", "AC-6", "AC-7", "AC-11", "AC-12"},
			"soc2": {"CC6.1", "CC6.3"},
		},
	},
	"nist-audit-retention": {
		ID:          "nist-audit-retention",
		Title:       "NIST — Audit Retention",
		Description: "Framework-bundle scope-cut: nist-audit-retention. Implements one section of the nist baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"4.1.x", "4.1.immutable", "4.2.x"},
			"cis-ubuntu": {"4.1.x", "4.1.immutable", "4.2.x"},
			"hitrust": {"09.l"},
			"iso-27001": {"A.12.4.4"},
			"nist-800-53": {"AU-8", "AU-10", "AU-11"},
			"pci-dss": {"10.4.1"},
			"soc2": {"CC7.2"},
		},
	},
	"nist-config-management": {
		ID:          "nist-config-management",
		Title:       "NIST — Config Management",
		Description: "Framework-bundle scope-cut: nist-config-management. Implements one section of the nist baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"1.1.1", "1.5.1"},
			"cis-ubuntu": {"1.1.1", "1.5.1"},
			"cyber-essentials": {"CE-SC-1", "CE-SC-5"},
			"iso-27001": {"A.12.5.1", "A.12.6.1"},
			"nist-800-53": {"CM-2", "CM-3", "CM-5", "CM-6", "CM-7", "CM-11"},
			"pci-dss": {"2.2.1", "2.2.4", "2.2.5", "2.2.7"},
			"soc2": {"CC8.1"},
		},
	},
	"nist-key-management": {
		ID:          "nist-key-management",
		Title:       "NIST — Key Management",
		Description: "Framework-bundle scope-cut: nist-key-management. Implements one section of the nist baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"gdpr": {"Art.32(1)(a)", "Art.32(2)"},
			"hitrust": {"10.f", "10.g"},
			"iso-27001": {"A.10.1.2"},
			"nist-800-53": {"SC-12"},
			"pci-dss": {"3.5.1", "3.5.1.1"},
			"soc2": {"C1.1", "C1.2"},
		},
	},
	"nist-login-banner": {
		ID:          "nist-login-banner",
		Title:       "NIST — Login Banner",
		Description: "Framework-bundle scope-cut: nist-login-banner. Implements one section of the nist baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cis-rhel": {"1.4.1"},
			"cis-ubuntu": {"1.4.1"},
			"hitrust": {"01.j"},
			"iso-27001": {"A.9.4.2"},
			"nist-800-53": {"AC-8"},
			"pci-dss": {"2.2.4"},
		},
	},
	"nist-system-integrity": {
		ID:          "nist-system-integrity",
		Title:       "NIST — System Integrity",
		Description: "Framework-bundle scope-cut: nist-system-integrity. Implements one section of the nist baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-MW-1", "CE-SU-2"},
			"hipaa": {"164.308(a)(5)(ii)(D)"},
			"iso-27001": {"A.12.2.1", "A.12.6.1"},
			"nist-800-53": {"SI-2", "SI-3", "SI-4", "SI-7"},
			"pci-dss": {"5.2.1", "5.2.2", "6.3.3", "11.5.1", "11.5.1.1"},
			"soc2": {"CC7.1", "CC8.1"},
		},
	},
	"nydfs-access-control": {
		ID:          "nydfs-access-control",
		Title:       "NYDFS — Access Control",
		Description: "Framework-bundle scope-cut: nydfs-access-control. Implements one section of the nydfs baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ny-dfs-cyber": {"NYCRR-500.07", "NYCRR-500.12"},
		},
	},
	"nydfs-audit-trail": {
		ID:          "nydfs-audit-trail",
		Title:       "NYDFS — Audit Trail",
		Description: "Framework-bundle scope-cut: nydfs-audit-trail. Implements one section of the nydfs baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ny-dfs-cyber": {"NYCRR-500.06"},
		},
	},
	"nydfs-encryption": {
		ID:          "nydfs-encryption",
		Title:       "NYDFS — Encryption",
		Description: "Framework-bundle scope-cut: nydfs-encryption. Implements one section of the nydfs baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ny-dfs-cyber": {"NYCRR-500.15"},
		},
	},
	"nydfs-monitoring": {
		ID:          "nydfs-monitoring",
		Title:       "NYDFS — Monitoring",
		Description: "Framework-bundle scope-cut: nydfs-monitoring. Implements one section of the nydfs baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"ny-dfs-cyber": {"NYCRR-500.14", "NYCRR-500.13"},
		},
	},
	"pci-audit": {
		ID:          "pci-audit",
		Title:       "PCI — Audit",
		Description: "Framework-bundle scope-cut: pci-audit. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(b)"},
			"iso-27001": {"A.12.4.1"},
			"nist-800-53": {"AU-2", "AU-3", "AU-12", "AU-8"},
			"pci-dss": {"10.2.1", "10.2.1.2", "10.2.1.3", "10.2.1.4", "10.2.1.5", "10.2.2", "10.3.1", "10.3.2", "10.6.1"},
		},
	},
	"pci-access-control": {
		ID:          "pci-access-control",
		Title:       "PCI — Access Control",
		Description: "Framework-bundle scope-cut: pci-access-control. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hitrust": {"01.c", "01.d", "01.v"},
			"iso-27001": {"A.9.2.3", "A.9.3.1", "A.9.4.3"},
			"nist-800-53": {"AC-3", "AC-6", "IA-5"},
			"pci-dss": {"7.2.1", "7.2.2", "7.2.5", "8.2.1", "8.2.2", "8.3.9", "8.6.1"},
			"soc2": {"CC6.2", "CC6.3"},
		},
	},
	"pci-file-integrity": {
		ID:          "pci-file-integrity",
		Title:       "PCI — File Integrity",
		Description: "Framework-bundle scope-cut: pci-file-integrity. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hipaa": {"164.312(c)(1)"},
			"nerc-cip": {"CIP-010-3-R2"},
			"nist-800-53": {"SI-7"},
			"pci-dss": {"11.5.1", "11.5.1.1"},
			"soc2": {"CC8.1"},
		},
	},
	"pci-hardening-dss": {
		ID:          "pci-hardening-dss",
		Title:       "PCI — Hardening (DSS with CIS/HITRUST cross-walks)",
		Description: "Framework-bundle scope-cut: PCI-DSS hardening with broader CIS / HITRUST / SOC2 cross-walks. Sourced from compliance/pci-dss/pci-hardening.vgo.example. Note: this module's YAML `name:` is `pci-hardening`, which collides with the simpler top-level compliance/pci-hardening.vgo.example — pre-existing data condition; operators load one or the other, not both.",
		Frameworks: map[string][]string{
			"cis-rhel": {"2.1.1", "2.2.x", "2.3.x"},
			"cis-ubuntu": {"2.1.1", "2.2.x", "2.3.x"},
			"hitrust": {"09.b", "09.c", "09.d"},
			"iso-27001": {"A.12.1.4", "A.12.4.4"},
			"nist-800-53": {"AU-8", "CM-7", "CM-8"},
			"pci-dss": {"2.2.2", "2.2.3", "6.3.1", "10.4.1", "11.3.1"},
			"soc2": {"CC6.6", "CC6.7"},
		},
	},
	"pci-logging": {
		ID:          "pci-logging",
		Title:       "PCI — Logging",
		Description: "Framework-bundle scope-cut: pci-logging. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-ML-1", "CE-ML-2"},
			"hipaa": {"164.312(b)"},
			"iso-27001": {"A.12.4.1", "A.12.4.3"},
			"nerc-cip": {"CIP-007-6-R4"},
			"nist-800-53": {"AU-2", "AU-3", "AU-9", "AU-12"},
			"pci-dss": {"10.2.1", "10.2.1.1", "10.2.1.2", "10.2.1.3", "10.2.2", "10.3.1", "10.3.2", "10.6.1"},
			"soc2": {"CC7.2"},
		},
	},
	"pci-network-security": {
		ID:          "pci-network-security",
		Title:       "PCI — Network Security",
		Description: "Framework-bundle scope-cut: pci-network-security. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"cyber-essentials": {"CE-FW-1", "CE-FW-3"},
			"nist-800-53": {"SC-7", "SC-8"},
			"pci-dss": {"1.2.1", "1.2.5", "1.3.1", "1.4.1"},
		},
	},
	"pci-fim": {
		ID:          "pci-fim",
		Title:       "PCI — Fim",
		Description: "Framework-bundle scope-cut: pci-fim. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iso-27001": {"A.12.6.1"},
			"nist-800-53": {"SI-7"},
			"pci-dss": {"10.3.4", "11.5.2"},
		},
	},
	"pci-hardening": {
		ID:          "pci-hardening",
		Title:       "PCI — Hardening",
		Description: "Framework-bundle scope-cut: pci-hardening. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iso-27001": {"A.9.4.3"},
			"nist-800-53": {"CM-6", "CM-7", "IA-5"},
			"pci-dss": {"2.2.1", "2.2.2", "2.2.4", "2.2.5", "2.2.7", "8.3.4"},
		},
	},
	"pci-network": {
		ID:          "pci-network",
		Title:       "PCI — Network",
		Description: "Framework-bundle scope-cut: pci-network. Implements one section of the pci baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iso-27001": {"A.13.1.1"},
			"nist-800-53": {"SC-7", "AC-4"},
			"pci-dss": {"1.2.1", "1.2.5", "1.3.1", "1.3.2", "1.4.1", "1.4.2"},
		},
	},
	"soc2-availability": {
		ID:          "soc2-availability",
		Title:       "SOC2 — Availability",
		Description: "Framework-bundle scope-cut: soc2-availability. Implements one section of the soc2 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iso-27001": {"A.17.1.1"},
			"nist-800-53": {"CP-9", "CP-10"},
			"soc2": {"A1.1", "A1.2"},
		},
	},
	"soc2-change-management": {
		ID:          "soc2-change-management",
		Title:       "SOC2 — Change Management",
		Description: "Framework-bundle scope-cut: soc2-change-management. Implements one section of the soc2 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"iso-27001": {"A.12.1.2", "A.14.2.2"},
			"nist-800-53": {"CM-2", "CM-3", "CM-5"},
			"pci-dss": {"6.5.1"},
			"soc2": {"CC8.1"},
		},
	},
	"soc2-governance": {
		ID:          "soc2-governance",
		Title:       "SOC2 — Governance",
		Description: "Framework-bundle scope-cut: soc2-governance. Implements one section of the soc2 baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"hitrust": {"06.c", "06.g", "06.h", "06.i", "11.a", "11.e", "12.c", "16.a"},
			"iso-27001": {"A.5.1", "A.6.1", "A.8.1", "A.8.2", "A.14.1.2", "A.16.1.7"},
			"nist-800-53": {"CM-8", "CA-7", "IR-4"},
			"soc2": {"CC1.1", "CC1.2", "CC1.3", "CC2.1", "CC2.2", "CC3.1", "CC3.2", "CC3.3", "CC4.1", "CC4.2", "CC5.1", "CC5.2", "CC5.3", "CC6.6", "CC6.7", "CC6.8", "CC7.5", "CC9.1", "PI1.1", "PI1.2"},
		},
	},
	"sox-access-controls": {
		ID:          "sox-access-controls",
		Title:       "SOX — Access Controls",
		Description: "Framework-bundle scope-cut: sox-access-controls. Implements one section of the sox baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"sox": {"ITGC-AC-1", "ITGC-AC-2", "ITGC-AC-3", "ITGC-AC-4", "ITGC-AC-5"},
		},
	},
	"sox-change-management": {
		ID:          "sox-change-management",
		Title:       "SOX — Change Management",
		Description: "Framework-bundle scope-cut: sox-change-management. Implements one section of the sox baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"sox": {"ITGC-CM-1", "ITGC-CM-2", "ITGC-CM-3", "ITGC-CM-4"},
		},
	},
	"sox-system-integrity": {
		ID:          "sox-system-integrity",
		Title:       "SOX — System Integrity",
		Description: "Framework-bundle scope-cut: sox-system-integrity. Implements one section of the sox baseline and its cross-walks; sourced from the module of the same name under example-configs/worksite/stockpile/modules/compliance/.",
		Frameworks: map[string][]string{
			"sox": {"ITGC-SI-1", "ITGC-SI-2", "ITGC-SI-3"},
		},
	},
}

// ResolveBundle returns the framework → control IDs map for a given
// bundle ID. Returns nil for an unknown ID (modlint catches unknown
// bundles at module load; the loader treats nil as zero contribution,
// parallel to ResolveProvision).
func ResolveBundle(id string) map[string][]string {
	b, ok := ConfigcrateBundles[id]
	if !ok {
		return nil
	}
	return b.Frameworks
}

// KnownBundleIDs returns every catalog ID in alphabetical order.
// Used by modlint's did-you-mean and by `vigocli compliance bundles`.
func KnownBundleIDs() []string {
	ids := make([]string, 0, len(ConfigcrateBundles))
	for id := range ConfigcrateBundles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
