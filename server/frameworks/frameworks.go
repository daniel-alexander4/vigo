// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package frameworks is the single source of truth for the set of
// compliance frameworks Vigo reports on. Before 0.76.x the 22-framework
// list was hand-duplicated across ~10 sites (config slug map, the report
// builders' metadata, the version-of-record labels, the modlint canonical
// set, the UI report-page maps, …) in three representations — URL slug,
// compliance-tag standard key, and display name — with no canonical home;
// the gaming-split and ABA-retire churn that exposed this is recorded in
// docs/internal.docs/compliance-control-scope-policy.md. Every pure-data
// enumeration of frameworks now derives from All here.
//
// This package is a leaf: stdlib only, no Vigo imports, so config,
// compliance, modlint, the api report layer, the UI, and the CLI can all
// derive from it without an import cycle (matching the cve/risk/certexpiry
// SoT-leaf idiom). Two surfaces stay per-framework by nature and are NOT
// derived from here — they are behavior, not a list: the report builder
// func (server/api/report.go frameworkDef) and the CLI report subcommand
// (cli/report.go). The consistency test in server/api guards that every
// slug here has a builder.
package frameworks

// Framework is the canonical identity + display metadata for one
// compliance framework. Behavior (the control-catalog builder) lives with
// the report handler, keyed by Slug.
type Framework struct {
	// Slug is the URL slug and the key used across the api/ui/cli report
	// paths (e.g. "pci", "gamingmga"). It is the primary key for this
	// package's lookup maps.
	Slug string
	// StandardKey is the compliance-tag key a configcrate writes in its
	// bundle:/provides: catalogs and the key server/config indexes coverage
	// by (e.g. "pci-dss", "gaming-mga").
	StandardKey string
	// ShortName is the short display name (e.g. "PCI DSS v4.0"). Used as the
	// report Name, the executive-summary label, and the compliance-snapshot
	// series name — so it is part of the (snapshot_date, framework) DB key
	// and must stay stable.
	ShortName string
	// ReportTitle is the per-framework report page title (e.g.
	// "PCI DSS v4.0 Compliance Report").
	ReportTitle string
	// Label is the version-of-record shown on /platform/compliance and in
	// docs/reference/compliance-matrix.md — the standard revision Vigo's
	// control IDs are aligned to. Change only after the control mappings are
	// reviewed against the new revision.
	Label string
}

// All is the canonical framework list, in display order. Adding a framework
// means adding one entry here, writing its buildXFindings builder + wiring
// it into frameworkDef (server/api/report.go), and adding its CLI report
// subcommand (cli/report.go) — the rest of the enumeration sites derive from
// this slice. See compliance-control-scope-policy.md "Adding a framework".
var All = []Framework{
	{"compliance", "hipaa", "HIPAA", "HIPAA Compliance Report", "HIPAA Security Rule (45 CFR §164.302–318)"},
	{"hitrust", "hitrust", "HITRUST CSF", "HITRUST CSF Compliance Report", "HITRUST CSF v11.x"},
	{"soc2", "soc2", "SOC 2", "SOC 2 Trust Services Criteria Report", "SOC 2 Trust Services Criteria 2017"},
	{"pci", "pci-dss", "PCI DSS v4.0", "PCI DSS v4.0 Compliance Report", "PCI DSS v4.0"},
	{"nist", "nist-800-53", "NIST 800-53", "NIST 800-53 Compliance Report", "NIST 800-53 Rev. 5"},
	{"iso27001", "iso-27001", "ISO 27001", "ISO 27001 Compliance Report", "ISO/IEC 27001:2022 Annex A"},
	{"gdpr", "gdpr", "GDPR", "GDPR Compliance Report", "GDPR (technical & organizational measures)"},
	{"nerccip", "nerc-cip", "NERC CIP", "NERC CIP Compliance Report", "NERC CIP v6"},
	{"cyberessentials", "cyber-essentials", "Cyber Essentials+", "Cyber Essentials+ Compliance Report", "Cyber Essentials Plus (UK)"},
	{"iec62443", "iec-62443", "IEC 62443", "IEC 62443 Industrial Cybersecurity Report", "IEC 62443"},
	{"sox", "sox", "SOX", "SOX IT General Controls Report", "SOX"},
	{"finra", "finra", "FINRA", "FINRA Compliance Report", "FINRA"},
	{"mifid2", "mifid2", "MiFID II", "MiFID II RTS 7 Compliance Report", "MiFID II RTS 7 / RTS 25"},
	{"ccss", "ccss", "CCSS", "CCSS Cryptocurrency Security Report", "CCSS"},
	{"fda21cfr11", "fda-21cfr11", "FDA 21 CFR Part 11", "FDA 21 CFR Part 11 Compliance Report", "FDA 21 CFR Part 11"},
	{"gamingukgc", "gaming-ukgc", "UKGC", "UK Gambling Commission Compliance Report", "UK Gambling Commission (Remote Technical Standards)"},
	{"gamingnevada", "gaming-nevada-gcb", "Nevada GCB", "Nevada Gaming Control Board Compliance Report", "Nevada Gaming Control Board (Reg 14 Technical Standards)"},
	{"gamingmga", "gaming-mga", "MGA", "Malta Gaming Authority Compliance Report", "Malta Gaming Authority (Technical Infrastructure)"},
	{"ciswindows", "cis-windows", "CIS Windows", "CIS Windows Server Benchmark Report", "CIS Windows Server 2022 Benchmark v3.0.0 (L1 Member Server)"},
	{"cisubuntu", "cis-ubuntu", "CIS Ubuntu", "CIS Ubuntu Linux Benchmark Report", "CIS Ubuntu Benchmark v2.0.0 (L1 Server)"},
	{"cisrhel", "cis-rhel", "CIS RHEL", "CIS RHEL Benchmark Report", "CIS RHEL Benchmark v2.0.0 (L1 Server)"},
	{"nydfscyber", "ny-dfs-cyber", "NY DFS Cyber (23 NYCRR 500)", "NY DFS Cybersecurity (23 NYCRR 500) Report", "NY DFS Cyber (23 NYCRR 500)"},
}

// BySlug indexes All by URL slug.
var BySlug = func() map[string]Framework {
	m := make(map[string]Framework, len(All))
	for _, f := range All {
		m[f.Slug] = f
	}
	return m
}()

// ByStandardKey indexes All by compliance-tag standard key.
var ByStandardKey = func() map[string]Framework {
	m := make(map[string]Framework, len(All))
	for _, f := range All {
		m[f.StandardKey] = f
	}
	return m
}()

// StandardKeyForSlug maps URL slug → compliance-tag standard key.
var StandardKeyForSlug = func() map[string]string {
	m := make(map[string]string, len(All))
	for _, f := range All {
		m[f.Slug] = f.StandardKey
	}
	return m
}()

// SlugForStandardKey is the inverse of StandardKeyForSlug.
var SlugForStandardKey = func() map[string]string {
	m := make(map[string]string, len(All))
	for _, f := range All {
		m[f.StandardKey] = f.Slug
	}
	return m
}()
