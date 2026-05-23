// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliance

import "sort"

// ModuleBundle is one framework-scope-cut a module implements. Bundles
// are the sibling concept to ModuleProvision — provisions name what a
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
// Structurally a ModuleBundle is identical to a ModuleProvision for
// v1. Bundles get their own type so they can grow bundle-specific
// fields (baseline version pin, primary framework slug, last-reviewed
// date) without touching provisions.
type ModuleBundle struct {
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

// ModuleBundles is the source-of-truth catalog. Keys are the
// kebab-case bundle IDs that operators reference under `bundle:`.
// Authored by collapsing each framework-bundle module under
// example-configs/worksite/stockpile/modules/compliance/ into a single
// catalog entry whose Frameworks map captures the module's historical
// claim verbatim.
var ModuleBundles = map[string]ModuleBundle{}

// ResolveBundle returns the framework → control IDs map for a given
// bundle ID. Returns nil for an unknown ID (modlint catches unknown
// bundles at module load; the loader treats nil as zero contribution,
// parallel to ResolveProvision).
func ResolveBundle(id string) map[string][]string {
	b, ok := ModuleBundles[id]
	if !ok {
		return nil
	}
	return b.Frameworks
}

// KnownBundleIDs returns every catalog ID in alphabetical order.
// Used by modlint's did-you-mean and by `vigocli compliance bundles`.
func KnownBundleIDs() []string {
	ids := make([]string, 0, len(ModuleBundles))
	for id := range ModuleBundles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
