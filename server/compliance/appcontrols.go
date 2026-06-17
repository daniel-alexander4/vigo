// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package compliance is the single source of truth for Vigo's own
// product-level compliance posture — what the Vigo server and agent
// satisfy by construction, independent of any envoy's module
// assignment.
//
// This is deliberately separate from envoy compliance scoring
// (server/api/report.go + server/config/loader.go's
// EnvoyStandardCoverage). Framework reports under /api/v1/report/<slug>
// score envoy state only — module-tag coverage, Connwaer trait
// escalation, and waivers — and never fold Vigo's own architecture
// into per-envoy or fleet aggregates.
//
// Consumers of this registry are read-only surfaces that describe
// what the Vigo product itself does for compliance: the eventual
// /platform/compliance UI page, customer-facing security-posture
// docs, and any SOC 2 / vendor-questionnaire export that asks
// "what does Vigo claim?" rather than "what does my fleet claim?"
package compliance

// AppControl is one capability the Vigo product satisfies by
// construction (not by enforcing a module on an envoy). Each entry
// is informational and never enters envoy compliance scoring.
type AppControl struct {
	// ID is the stable identifier used in URLs / anchors on the
	// /platform/compliance page.
	ID string

	// Title is the human label.
	Title string

	// Description is one or two sentences explaining what Vigo does.
	Description string

	// Evidence is the technical detail an auditor wants to see —
	// algorithm names, file paths, code references, etc.
	Evidence string

	// Frameworks lists the framework controls this capability is
	// relevant to. Informational; framework reports do NOT consume
	// this list — that would put us back in the situation where
	// Vigo's own posture inflates per-envoy scores.
	Frameworks []FrameworkRef
}

// FrameworkRef points at one or more controls in a framework.
// Slug matches server/config.StandardKeyForSlug keys; ControlIDs
// are the framework's native identifiers (e.g., "164.312(e)(1)"
// for HIPAA, "CC6.1" for SOC 2).
type FrameworkRef struct {
	Slug       string
	ControlIDs []string
}

// frameworkLabels maps the framework slugs used in AppControls to
// the human label shown on /platform/compliance. Mirrors the labels
// in server/main.go's framework metrics table. The version string is
// the currently-mapped baseline — i.e., the revision Vigo's control
// IDs are aligned to. When a framework publishes a new revision, the
// label here changes only after the control mappings are reviewed
// against the new revision; auditors reading docs/reference/compliance-matrix.md
// can rely on this label as the version-of-record.
var frameworkLabels = map[string]string{
	"compliance":      "HIPAA Security Rule (45 CFR §164.302–318)",
	"hitrust":         "HITRUST CSF v11.x",
	"soc2":            "SOC 2 Trust Services Criteria 2017",
	"pci":             "PCI DSS v4.0",
	"nist":            "NIST 800-53 Rev. 5",
	"iso27001":        "ISO/IEC 27001:2022 Annex A",
	"gdpr":            "GDPR (technical & organizational measures)",
	"nerccip":         "NERC CIP v6",
	"cyberessentials": "Cyber Essentials Plus (UK)",
	"iec62443":        "IEC 62443",
	"sox":             "SOX",
	"finra":           "FINRA",
	"mifid2":          "MiFID II RTS 24",
	"ccss":            "CCSS",
	"fda21cfr11":      "FDA 21 CFR Part 11",
	"gamingukgc":      "UK Gambling Commission (Remote Technical Standards)",
	"gamingnevada":    "Nevada Gaming Control Board (Reg 14 Technical Standards)",
	"gamingmga":       "Malta Gaming Authority (Technical Infrastructure)",
	"ciswindows":      "CIS Windows 11 Benchmark v3.x",
	"cisubuntu":       "CIS Ubuntu 22.04 Benchmark v1.0.0",
	"cisrhel":         "CIS RHEL 9 Benchmark v2.x",
	"nydfscyber":      "NY DFS Cyber (23 NYCRR 500)",
}

// FrameworkLabel returns the human label for a framework slug as
// referenced from AppControls. Falls back to the slug itself if
// the framework is not in the table.
func FrameworkLabel(slug string) string {
	if l, ok := frameworkLabels[slug]; ok {
		return l
	}
	return slug
}

// AppControls is the registry of Vigo product-level controls.
//
// Update this list when a new architectural security capability
// ships in vigosrv or the Rust agent. Do not add module-driven
// claims here — those live in module .vgo files via the
// `compliance:` block and are surfaced through envoy scoring.
var AppControls = []AppControl{
	{
		ID:          "transport-mtls",
		Title:       "All transport encrypted with mTLS",
		Description: "Every gRPC and REST connection between the Vigo server, agents, and the admin CLI uses mutual TLS (TLS 1.3). No plaintext management traffic is possible.",
		Evidence:    "TLS configured in server/main.go and agent/src/network; certificates managed by server/ca; agents reject any non-mTLS check-in.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.312(e)(1)", "164.312(e)(2)(ii)"}},
			{Slug: "soc2", ControlIDs: []string{"CC6.1", "CC6.7"}},
			{Slug: "pci", ControlIDs: []string{"2.2.7", "8.3.1"}},
			{Slug: "nist", ControlIDs: []string{"SC-8", "IA-8"}},
			{Slug: "iso27001", ControlIDs: []string{"A.13.2.1", "A.14.1.2"}},
			{Slug: "hitrust", ControlIDs: []string{"01.i", "01.j"}},
			{Slug: "iec62443", ControlIDs: []string{"SR 1.2", "SR 3.1", "SR 4.3"}},
		},
	},
	{
		ID:          "agent-signature-auth",
		Title:       "Agent identity verified by ED25519 signatures",
		Description: "Every agent request is signed with the agent's ED25519 keypair and verified server-side against the public key recorded at enrollment. Revoked clients cannot re-enroll with the same UUID.",
		Evidence:    "Signature verification in server/grpc; pubkey stored in envoys table; enrollment tokens are bcrypt-hashed at rest.",
		Frameworks: []FrameworkRef{
			{Slug: "nist", ControlIDs: []string{"AU-10", "IA-8", "SC-12"}},
			{Slug: "soc2", ControlIDs: []string{"PI1.1"}},
			{Slug: "iec62443", ControlIDs: []string{"SR 1.2", "SR 1.8", "SR 1.9"}},
		},
	},
	{
		ID:          "rbac-permissions",
		Title:       "Role-based access control with fine-grained permissions",
		Description: "The web UI and REST API gate every endpoint behind RBAC. Two roles (admin, viewer) are seeded; permissions are checked per-request via RequirePermission middleware.",
		Evidence:    "server/auth: per-user accounts with unique UUIDs, role-based defaults, RequirePermission middleware on every handler.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.312(a)(1)", "164.312(a)(2)(i)"}},
			{Slug: "soc2", ControlIDs: []string{"CC1.3", "CC6.2", "CC6.3"}},
			{Slug: "pci", ControlIDs: []string{"7.2.1", "7.2.2", "7.2.5", "8.2.1", "8.2.2"}},
			{Slug: "nist", ControlIDs: []string{"AC-2", "AC-3", "AC-6"}},
			{Slug: "iso27001", ControlIDs: []string{"A.9.2.1", "A.9.2.3", "A.9.4.1"}},
			{Slug: "hitrust", ControlIDs: []string{"01.b", "01.c"}},
		},
	},
	{
		ID:          "password-policy",
		Title:       "Password policy enforced for web UI accounts",
		Description: "Local web-UI passwords require 12 characters minimum, mixed case, digit, and special. All passwords are bcrypt-hashed at rest. TOTP MFA is available; OIDC delegates to the IdP.",
		Evidence:    "server/auth/basic.go: complexity check + bcrypt cost. Storage in users table; never logged. TOTP via server/auth.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.308(a)(5)(ii)(D)", "164.312(d)"}},
			{Slug: "pci", ControlIDs: []string{"8.3.1", "8.3.6", "8.3.9"}},
			{Slug: "nist", ControlIDs: []string{"IA-5", "IA-2"}},
			{Slug: "iso27001", ControlIDs: []string{"A.9.3.1", "A.9.4.3"}},
			{Slug: "hitrust", ControlIDs: []string{"01.d"}},
		},
	},
	{
		ID:          "session-management",
		Title:       "Session timeouts and idle logoff",
		Description: "Web UI sessions enforce a configurable idle timeout (default 15 min) and an absolute lifetime. Sessions live in SCS; logoff invalidates the cookie immediately.",
		Evidence:    "server/auth session middleware; server.yaml auth.session_idle_timeout / auth.session_max_lifetime.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.312(a)(2)(iii)"}},
			{Slug: "nist", ControlIDs: []string{"AC-11", "AC-12"}},
		},
	},
	{
		ID:          "audit-hash-chain",
		Title:       "Tamper-evident audit log on the server",
		Description: "All security-relevant events on the server (auth, user, token, config publish, push, workflow, emergency, remediation) are written to a SHA-256 hash-chained audit log. Any modification breaks the chain and is detectable via vigocli audit verify.",
		Evidence:    "server/audit: append-only writes; verify walk on the entire chain; SIEM export endpoint.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.312(b)", "164.312(c)(1)"}},
			{Slug: "soc2", ControlIDs: []string{"CC4.1", "CC4.2", "CC7.3", "PI1.2"}},
			{Slug: "pci", ControlIDs: []string{"10.2.1", "10.2.1.2", "10.2.1.3", "10.2.2", "10.3.1"}},
			{Slug: "nist", ControlIDs: []string{"AU-2", "AU-3", "AU-6", "AU-9", "AU-12"}},
			{Slug: "iso27001", ControlIDs: []string{"A.12.4.1", "A.12.4.3"}},
			{Slug: "hitrust", ControlIDs: []string{"06.i", "09.q", "09.s"}},
		},
	},
	{
		ID:          "secrets-encryption",
		Title:       "Secrets and backups encrypted at rest",
		Description: "The local secrets provider encrypts every secret with AES-256-GCM keyed off an Argon2id-derived master key. Litestream backups use the same envelope. The on-wire `secret:` prefix prevents plaintext leakage into config, env vars, logs, DB rows, and gRPC payloads.",
		Evidence:    "server/secrets local provider; server/main.go backup encryption; loader rejects plaintext on the secret: path.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.312(a)(2)(iv)"}},
			{Slug: "pci", ControlIDs: []string{"3.5.1", "3.5.1.1"}},
			{Slug: "nist", ControlIDs: []string{"SC-12", "SC-13", "SC-28"}},
			{Slug: "iso27001", ControlIDs: []string{"A.10.1.1", "A.10.1.2"}},
			{Slug: "gdpr", ControlIDs: []string{"Art.32(1)(a)"}},
			{Slug: "hitrust", ControlIDs: []string{"10.f", "10.g"}},
		},
	},
	{
		ID:          "config-as-code",
		Title:       "Config-as-code with audited publish workflow",
		Description: "All policy is YAML in the worksite tree. `vigocli config publish` validates via modlint, records the diff in the audit chain, and reloads the live store. Rollback restores any prior published revision.",
		Evidence:    "server/config publish handler; server/modlint validation; vigocli config rollback.",
		Frameworks: []FrameworkRef{
			{Slug: "soc2", ControlIDs: []string{"CC8.1"}},
			{Slug: "pci", ControlIDs: []string{"6.5.1"}},
			{Slug: "nist", ControlIDs: []string{"CM-2", "CM-3", "CM-5"}},
			{Slug: "iso27001", ControlIDs: []string{"A.12.1.2", "A.14.2.2"}},
			{Slug: "hitrust", ControlIDs: []string{"09.b", "10.k"}},
		},
	},
	{
		ID:          "drift-detection",
		Title:       "Continuous drift detection on every check-in",
		Description: "Agents re-evaluate desired vs actual state on every check-in (default 5 min). Changed/diverged drift is detected across recent runs and alerts via webhook + SMTP.",
		Evidence:    "server/convergence; server/api/report.go convergence rollup; server/integrations notifier.",
		Frameworks: []FrameworkRef{
			{Slug: "soc2", ControlIDs: []string{"CC4.1", "CC7.1", "CC7.2"}},
			{Slug: "nist", ControlIDs: []string{"CA-7", "SI-4", "SI-7"}},
			{Slug: "iso27001", ControlIDs: []string{"A.18.2.2", "A.18.2.3"}},
			{Slug: "hitrust", ControlIDs: []string{"06.g", "06.h"}},
		},
	},
	{
		ID:          "trait-collectors",
		Title:       "Automatic asset and software inventory",
		Description: "The agent ships ~25 trait collectors that auto-discover hardware, OS, network, and installed packages. Inventory is fleet-queryable via the trait API and surfaced on the Inventory page.",
		Evidence:    "agent/src/traits; server/traits collector loader; FleetIndex live trait map.",
		Frameworks: []FrameworkRef{
			{Slug: "nist", ControlIDs: []string{"CM-8"}},
			{Slug: "iso27001", ControlIDs: []string{"A.8.1"}},
			{Slug: "hitrust", ControlIDs: []string{"07.a"}},
			{Slug: "nerccip", ControlIDs: []string{"CIP-002-5.1"}},
		},
	},
	{
		ID:          "decommission-cascade",
		Title:       "Documented decommissioning with cascade delete",
		Description: "Removing an envoy cascades through enrollment records, run history, traits, and certificates. Sandgorgon issues a NIST 800-88-aligned certificate when bare-metal decommission is invoked.",
		Evidence:    "server/db cascade migrations; server/sandgorgon decommission workflow.",
		Frameworks: []FrameworkRef{
			{Slug: "compliance", ControlIDs: []string{"164.310(d)(2)(i)"}},
			{Slug: "iso27001", ControlIDs: []string{"A.8.2"}},
			{Slug: "gdpr", ControlIDs: []string{"Art.17(1)"}},
		},
	},
	{
		ID:          "litestream-backup",
		Title:       "Continuous database backup via Litestream",
		Description: "SQLite WAL is shipped continuously by Litestream to encrypted object storage. Snapshots are AES-256-GCM enveloped; restores are verified against SHA-256 manifests.",
		Evidence:    "litestream.yml in deploy; Litestream sidecar; backup tests in server/api.",
		Frameworks: []FrameworkRef{
			{Slug: "nist", ControlIDs: []string{"CP-9", "CP-10", "AU-11"}},
			{Slug: "iso27001", ControlIDs: []string{"A.12.3.1", "A.18.1.3"}},
			{Slug: "hitrust", ControlIDs: []string{"06.c", "09.k"}},
			{Slug: "pci", ControlIDs: []string{"10.3.2"}},
		},
	},
}
