// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliance

import "sort"

// ModuleProvision is one functional capability a module makes available
// for compliance attribution. Modules declare `provides: [time-sync]`;
// the loader resolves each tag through ModuleProvisions and unions the
// resulting (framework, control) pairs with the module's explicit
// `compliance:` block.
//
// The value of provisions IS the cross-walk maintenance — moving each
// framework's cross-walk into one Vigo-curated place so adding a
// framework touches one file instead of every module, and so two
// modules that solve the same problem (chrony vs ntpd, rsyslog vs
// fluent-bit) can't drift apart silently. The catalog is therefore
// code-curated: adding or revising a provision is a PR, not an
// operator-editable file.
//
// This is orthogonal to ConnwaerCapabilityByControl in
// server/api/report.go — that map exists to *escalate* a framework
// control when a Connwaer trait proves it's enforced, whereas
// ModuleProvisions exists to *attribute* a framework control to the
// module that provides it. The two layers compose but never overlap;
// the renaming is deliberate.
type ModuleProvision struct {
	// ID is the kebab-case tag operators write in YAML. Matches the
	// map key.
	ID string

	// Title is the human label shown by `vigocli compliance provisions`.
	Title string

	// Description is one or two sentences explaining what a module
	// claiming this provision delivers — operators read this to decide
	// whether their module actually satisfies it.
	Description string

	// Frameworks maps each framework slug (matching
	// server/config.StandardKeyForSlug values) to the control IDs this
	// provision satisfies in that framework. Control IDs are the
	// framework's native form (no normalization beyond what the
	// framework itself publishes).
	Frameworks map[string][]string
}

// ModuleProvisions is the source-of-truth catalog. Keys are the
// kebab-case provision IDs that operators reference under `provides:`.
//
// Authoring note: every entry's framework → control list is
// cross-checked against at least one real-world module under
// example-configs/worksite/stockpile/modules/ that historically
// hand-rolled the same cross-walk in its `compliance:` block — the
// catalog is grounded in actual usage, not aspirational benchmark
// categories.
var ModuleProvisions = map[string]ModuleProvision{
	"time-sync": {
		ID:          "time-sync",
		Title:       "System time synchronization",
		Description: "Installs and runs a time-sync daemon (chrony or ntpd) that keeps the system clock aligned with an authoritative source. Required for audit-log timestamp correlation across the fleet.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"2.1.1.1", "2.1.1.2"},
			"cis-rhel":    {"2.1.1.1", "2.1.1.2"},
			"iso-27001":   {"A.12.4.4"},
			"nist-800-53": {"AU-8"},
			"pci-dss":     {"10.6.1", "10.6.2", "10.6.3"},
		},
	},
	"timezone": {
		ID:          "timezone",
		Title:       "System timezone configuration",
		Description: "Sets the host's timezone to a known value (typically UTC) so audit and log timestamps are unambiguous across the fleet.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"6.1.1"},
			"cis-rhel":    {"6.1.1"},
			"hipaa":       {"164.312(b)"},
			"iso-27001":   {"A.12.4.4"},
			"nist-800-53": {"AU-8", "AU-8(1)"},
			"pci-dss":     {"10.4", "10.4.1", "10.4.3"},
			"soc2":        {"CC7.2"},
		},
	},
	"audit-trail": {
		ID:          "audit-trail",
		Title:       "Host-level audit logging",
		Description: "Runs the OS audit subsystem (auditd / Windows Event Log forwarding) to capture privileged actions, authentication events, and security-relevant syscalls.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"4.1.1.1", "4.1.1.2", "4.1.1.3", "4.1.1.4"},
			"cis-rhel":    {"4.1.1.1", "4.1.1.2", "4.1.1.3", "4.1.1.4"},
			"hipaa":       {"164.312(b)"},
			"iso-27001":   {"A.12.4.1"},
			"nist-800-53": {"AU-2", "AU-3", "AU-12"},
			"pci-dss":     {"10.2.1", "10.2.2", "10.3.1", "10.3.2"},
			"soc2":        {"CC7.2"},
		},
	},
	"file-integrity": {
		ID:          "file-integrity",
		Title:       "File integrity monitoring",
		Description: "Runs a file-integrity tool (AIDE, Tripwire) that hashes a baseline of critical files and reports unauthorized changes.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(c)(1)"},
			"iso-27001":   {"A.12.4.1", "A.14.2.4"},
			"nist-800-53": {"SI-7", "CM-3"},
			"pci-dss":     {"11.5"},
		},
	},
	"brute-force-protection": {
		ID:          "brute-force-protection",
		Title:       "Brute-force-login protection",
		Description: "Runs fail2ban (or equivalent) that watches authentication logs and bans source IPs after repeated failures.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(5)(ii)(D)"},
			"iso-27001":   {"A.9.4.2"},
			"nist-800-53": {"AC-7"},
			"pci-dss":     {"8.3.4"},
		},
	},
	"mandatory-access-control": {
		ID:          "mandatory-access-control",
		Title:       "Mandatory access control (LSM)",
		Description: "Enables a Linux Security Module (AppArmor or SELinux) that confines processes to declared policies.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.9.4.1", "A.12.4.1"},
			"nist-800-53": {"AC-3", "AC-6", "SI-4"},
		},
	},
	"log-shipping": {
		ID:          "log-shipping",
		Title:       "Log forwarding to a central collector",
		Description: "Forwards host logs (rsyslog, fluent-bit, promtail, …) to a central aggregation point so logs survive host loss and become correlatable. Narrow claim: forwarding only. Modules that also parse/filter (fluent-bit, vector) layer extra controls via an explicit `compliance:` block.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.12.4.1"},
			"nist-800-53": {"AU-2", "AU-3", "AU-9"},
			"pci-dss":     {"10.5.3"},
		},
	},
	"log-rotation": {
		ID:          "log-rotation",
		Title:       "Log rotation + retention",
		Description: "Rotates log files on a schedule (logrotate or equivalent) and enforces retention so the host doesn't exhaust storage and old evidence isn't truncated.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"4.2.1.3", "4.2.3"},
			"cis-rhel":    {"4.2.1.3", "4.2.3"},
			"hipaa":       {"164.312(b)", "164.312(c)(1)"},
			"nist-800-53": {"AU-4", "AU-9", "AU-11"},
			"pci-dss":     {"10.5.1", "10.7", "10.7.1"},
			"soc2":        {"CC7.2", "CC7.3"},
		},
	},
	"log-aggregation": {
		ID:          "log-aggregation",
		Title:       "Central log aggregation",
		Description: "Runs a log search/index backend (Elasticsearch, Loki, …) that accepts forwarded logs and serves them for review and alerting.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.12.4.1"},
			"nist-800-53": {"AU-2", "AU-6", "AU-9"},
			"pci-dss":     {"10.2.1", "10.3.3"},
		},
	},
	"host-firewall": {
		ID:          "host-firewall",
		Title:       "Host-level firewall",
		Description: "Installs and configures a host firewall (ufw, firewalld, nftables, iptables, Windows Firewall) with a default-deny stance. Narrow claim: the firewall is installed + default-deny + enabled. Modules that also assert specific allow/deny rule sets (firewall-rules, distro-specific hardening) layer extra controls via an explicit `compliance:` block.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"3.4.1.1"},
			"iso-27001":   {"A.13.1.1"},
			"nist-800-53": {"SC-7", "AC-4"},
		},
	},
	"tcp-access-control": {
		ID:          "tcp-access-control",
		Title:       "TCP-wrappers / host-based ACLs",
		Description: "Configures TCP wrappers or equivalent per-service ACLs to restrict inbound connections by source.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.9.4.1"},
			"nist-800-53": {"AC-3"},
		},
	},
	"central-identity": {
		ID:          "central-identity",
		Title:       "Central identity / directory integration",
		Description: "Joins the host to a central identity service (FreeIPA, AD, LDAP) so account and authorization decisions are centrally managed. Narrow claim: account management. Modules that also enforce credential lifecycle (sssd password ageing, freeipa OTP) layer extra controls via an explicit `compliance:` block.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.9.2.1"},
			"nist-800-53": {"IA-2", "IA-5"},
		},
	},
	"multi-factor-auth": {
		ID:          "multi-factor-auth",
		Title:       "Multi-factor authentication",
		Description: "Adds a second authentication factor (hardware token, TOTP) at the OS / PAM layer.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.9.4.2"},
			"nist-800-53": {"IA-2(1)", "IA-5(11)"},
		},
	},
	"vpn": {
		ID:          "vpn",
		Title:       "Encrypted remote access (VPN)",
		Description: "Runs a VPN endpoint (WireGuard, OpenVPN, Tailscale, …) that encrypts traffic in transit between hosts or sites. Narrow claim: encrypted tunnel transport. Modules that also assert specific cipher / cryptographic-control posture (OpenVPN's data-cipher pin, WireGuard's curve choice) layer extra controls via an explicit `compliance:` block.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.13.1.1"},
			"nist-800-53": {"AC-17", "SC-8"},
		},
	},
	"vulnerability-scanning": {
		ID:          "vulnerability-scanning",
		Title:       "Vulnerability scanning",
		Description: "Runs a vulnerability scanner (OpenVAS, Nessus, …) on a schedule and surfaces findings.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.12.6.1"},
			"nist-800-53": {"RA-5"},
			"pci-dss":     {"11.2.1", "11.3.1"},
		},
	},
	"system-resource-monitoring": {
		ID:          "system-resource-monitoring",
		Title:       "System resource monitoring (CPU/mem/disk)",
		Description: "Exposes host resource metrics (node-exporter, collectd, telegraf, …) so saturation and resource exhaustion are visible.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.12.4.1"},
			"nist-800-53": {"SI-4", "AU-12"},
			"soc2":        {"CC7.1"},
		},
	},
	"monitoring-probe": {
		ID:          "monitoring-probe",
		Title:       "External probe / synthetic monitoring",
		Description: "Runs a probe (blackbox-exporter, SNMP poller, NRPE) that synthesizes checks against services from outside.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.12.1.3"},
			"nist-800-53": {"SI-4", "CA-7"},
		},
	},
	"alerting": {
		ID:          "alerting",
		Title:       "Alert delivery",
		Description: "Runs an alerting router (Alertmanager or equivalent) that converts monitoring conditions into pages/emails/tickets.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.16.1.2", "A.16.1.4"},
			"nist-800-53": {"SI-4", "IR-4", "IR-6"},
		},
	},
	"dashboard": {
		ID:          "dashboard",
		Title:       "Operator observability dashboard",
		Description: "Runs a dashboard front-end (Grafana, Kibana, …) over the host's monitoring + log backends.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.12.4.1"},
			"nist-800-53": {"AU-6", "SI-4"},
			"soc2":        {"CC7.2"},
		},
	},
	"availability-healthcheck": {
		ID:          "availability-healthcheck",
		Title:       "Service availability healthcheck",
		Description: "Runs a healthcheck on critical services (process + endpoint), restarts on failure, surfaces persistent failures.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(7)(i)"},
			"iso-27001":   {"A.12.1.3", "A.17.1.2"},
			"nist-800-53": {"CP-2", "SI-13", "CP-10"},
			"soc2":        {"A1.2"},
		},
	},
	"disk-health-monitoring": {
		ID:          "disk-health-monitoring",
		Title:       "Disk health monitoring",
		Description: "Polls SMART attributes (smartmontools / equivalent) and surfaces drive failure predictors before they take the host down.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.17.1.2"},
			"nist-800-53": {"SI-4", "CP-2"},
		},
	},
	"email-anti-malware": {
		ID:          "email-anti-malware",
		Title:       "Email anti-malware / anti-spam",
		Description: "Runs an email content scanner (rspamd, ClamAV, …) on mail in transit.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.13.2.3"},
			"nist-800-53": {"SI-3", "SI-8"},
		},
	},
}

// ResolveProvision returns the framework → control IDs map for a given
// provision ID. Returns nil for an unknown ID (modlint catches unknown
// provisions at module-load; the loader treats nil as zero contribution).
func ResolveProvision(id string) map[string][]string {
	p, ok := ModuleProvisions[id]
	if !ok {
		return nil
	}
	return p.Frameworks
}

// KnownProvisionIDs returns every catalog ID in alphabetical order.
// Used by modlint's did-you-mean and by `vigocli compliance provisions`.
func KnownProvisionIDs() []string {
	ids := make([]string, 0, len(ModuleProvisions))
	for id := range ModuleProvisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
