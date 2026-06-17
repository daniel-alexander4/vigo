// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliance

import "sort"

// ConfigcrateProvision is one functional capability a configcrate makes available
// for compliance attribution. Configcrates declare `provides: [time-sync]`;
// the loader resolves each tag through ConfigcrateProvisions and unions the
// resulting (framework, control) pairs with the configcrate's explicit
// `compliance:` block.
//
// The value of provisions IS the cross-walk maintenance — moving each
// framework's cross-walk into one Vigo-curated place so adding a
// framework touches one file instead of every configcrate, and so two
// configcrates that solve the same problem (chrony vs ntpd, rsyslog vs
// fluent-bit) can't drift apart silently. The catalog is therefore
// code-curated: adding or revising a provision is a PR, not an
// operator-editable file.
//
// This is orthogonal to ConnwaerCapabilityByControl in
// server/api/report.go — that map exists to *escalate* a framework
// control when a Connwaer trait proves it's enforced, whereas
// ConfigcrateProvisions exists to *attribute* a framework control to the
// configcrate that provides it. The two layers compose but never overlap;
// the renaming is deliberate.
type ConfigcrateProvision struct {
	// ID is the kebab-case tag operators write in YAML. Matches the
	// map key.
	ID string

	// Title is the human label shown by `vigocli compliance provisions`.
	Title string

	// Description is one or two sentences explaining what a configcrate
	// claiming this provision delivers — operators read this to decide
	// whether their configcrate actually satisfies it.
	Description string

	// Frameworks maps each framework slug (matching
	// server/config.StandardKeyForSlug values) to the control IDs this
	// provision satisfies in that framework. Control IDs are the
	// framework's native form (no normalization beyond what the
	// framework itself publishes).
	Frameworks map[string][]string
}

// ConfigcrateProvisions is the source-of-truth catalog. Keys are the
// kebab-case provision IDs that operators reference under `provides:`.
//
// Authoring note: every entry's framework → control list is
// cross-checked against at least one real-world configcrate under
// example-configs/stacks/configcrates/ that historically
// hand-rolled the same cross-walk in its `compliance:` block — the
// catalog is grounded in actual usage, not aspirational benchmark
// categories.
var ConfigcrateProvisions = map[string]ConfigcrateProvision{
	"time-sync": {
		ID:          "time-sync",
		Title:       "System time synchronization",
		Description: "Installs and runs a time-sync daemon (chrony or ntpd) that keeps the system clock aligned with an authoritative source. Required for audit-log timestamp correlation across the fleet.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"2.1.1.1", "2.1.1.2"},
			"cis-rhel":    {"2.1.1.1", "2.1.1.2"},
			"iso-27001":   {"A.8.17"},
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
			"iso-27001":   {"A.8.17"},
			"nist-800-53": {"AU-8"},
			"pci-dss":     {"10.6.1", "10.4.1", "10.4.3"},
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
			"iso-27001":   {"A.8.15"},
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
			"iso-27001":   {"A.8.15", "A.8.32"},
			"nist-800-53": {"SI-7", "CM-3"},
			"pci-dss":     {"11.5.2"},
		},
	},
	"brute-force-protection": {
		ID:          "brute-force-protection",
		Title:       "Brute-force-login protection",
		Description: "Runs fail2ban (or equivalent) that watches authentication logs and bans source IPs after repeated failures.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(5)(ii)(D)"},
			"iso-27001":   {"A.8.5"},
			"nist-800-53": {"AC-7"},
			"pci-dss":     {"8.3.4"},
		},
	},
	"mandatory-access-control": {
		ID:          "mandatory-access-control",
		Title:       "Mandatory access control (LSM)",
		Description: "Enables a Linux Security Module (AppArmor or SELinux) that confines processes to declared policies.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.3", "A.8.15"},
			"nist-800-53": {"AC-3", "AC-6", "SI-4"},
		},
	},
	"log-shipping": {
		ID:          "log-shipping",
		Title:       "Log forwarding to a central collector",
		Description: "Forwards host logs (rsyslog, fluent-bit, promtail, …) to a central aggregation point so logs survive host loss and become correlatable. Narrow claim: forwarding only. Configcrates that also parse/filter (fluent-bit, vector) layer extra controls via an explicit `compliance:` block.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-2", "AU-3", "AU-9"},
			"pci-dss":     {"10.3.3"},
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
			"pci-dss":     {"10.5.1", "10.7.1"},
			"soc2":        {"CC7.2", "CC7.3"},
		},
	},
	"log-aggregation": {
		ID:          "log-aggregation",
		Title:       "Central log aggregation",
		Description: "Runs a log search/index backend (Elasticsearch, Loki, …) that accepts forwarded logs and serves them for review and alerting.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-2", "AU-6", "AU-9"},
			"pci-dss":     {"10.2.1", "10.3.3"},
		},
	},
	"host-firewall": {
		ID:          "host-firewall",
		Title:       "Host-level firewall",
		Description: "Installs and configures a host firewall (ufw, firewalld, nftables, iptables, Windows Firewall) with a default-deny stance. Covers both the firewall-installed + default-deny base case and configcrates that assert specific allow/deny rule sets — a host firewall with any rule set satisfies the same broad control set.",
		Frameworks: map[string][]string{
			"cis-ubuntu":  {"3.4.1.1", "3.4.1.2"},
			"iso-27001":   {"A.8.20"},
			"nist-800-53": {"SC-7", "AC-4"},
			"pci-dss":     {"1.2.1", "1.2.5", "1.3.1", "1.4.1"},
		},
	},
	"tcp-access-control": {
		ID:          "tcp-access-control",
		Title:       "TCP-wrappers / host-based ACLs",
		Description: "Configures TCP wrappers or equivalent per-service ACLs to restrict inbound connections by source.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.3"},
			"nist-800-53": {"AC-3"},
		},
	},
	"central-identity": {
		ID:          "central-identity",
		Title:       "Central identity / directory integration",
		Description: "Joins the host to a central identity service (FreeIPA, AD, LDAP, sssd) so account and authorization decisions are centrally managed. Covers basic directory join + credential resolution; configcrates that add MFA layer it via the `multi-factor-auth` provision.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.5.16", "A.8.5"},
			"nist-800-53": {"IA-2", "IA-5"},
		},
	},
	"multi-factor-auth": {
		ID:          "multi-factor-auth",
		Title:       "Multi-factor authentication",
		Description: "Adds a second authentication factor (hardware token, TOTP) at the OS / PAM layer.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.5"},
			"nist-800-53": {"IA-2(1)"},
		},
	},
	"vpn": {
		ID:          "vpn",
		Title:       "Encrypted remote access (VPN)",
		Description: "Runs a VPN endpoint (WireGuard, Tailscale, etc.) that encrypts traffic in transit between hosts or sites with a strong cipher suite. Covers tunnel transport + cryptographic-control posture; configcrates that ride a PKI-based bidirectional auth (OpenVPN-style) declare `vpn-with-pki` instead.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.20", "A.5.14"},
			"nist-800-53": {"AC-17", "SC-8", "SC-13"},
			"pci-dss":     {"4.2.1"},
		},
	},
	"vpn-with-pki": {
		ID:          "vpn-with-pki",
		Title:       "Encrypted remote access (PKI-authenticated)",
		Description: "Runs a VPN endpoint that authenticates peers via X.509 (OpenVPN, IKEv2). Covers the same transport encryption as `vpn` plus mutual PKI authentication and bidirectional certificate validation.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(e)(1)"},
			"iso-27001":   {"A.5.14"},
			"nist-800-53": {"AC-17", "SC-8", "SC-13"},
			"pci-dss":     {"4.2.1"},
		},
	},
	"vpn-endpoint-client": {
		ID:          "vpn-endpoint-client",
		Title:       "VPN endpoint client",
		Description: "Installs and configures a VPN client (FortiClient, AnyConnect, etc.) on a workstation/endpoint so it can reach the corporate VPN.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(e)(1)"},
			"iso-27001":   {"A.5.14"},
			"nist-800-53": {"AC-17", "SC-8"},
		},
	},
	"host-backup": {
		ID:          "host-backup",
		Title:       "Host file backup",
		Description: "Backs up host data on a schedule to a remote target. Narrow claim: scheduled backups exist and the backup plan is documented. Configcrates that also assert encryption at rest declare `host-backup-encrypted` instead.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(7)(ii)(A)"},
			"iso-27001":   {"A.8.13"},
			"nist-800-53": {"CP-9"},
			"soc2":        {"A1.2"},
		},
	},
	"host-backup-encrypted": {
		ID:          "host-backup-encrypted",
		Title:       "Encrypted host file backup",
		Description: "Backs up host data to a remote target with at-rest encryption (repository key, passphrase, or KMS). Adds cryptographic-protection + key-management claims on top of `host-backup`; configcrates use one or the other, not both.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(7)(ii)(A)", "164.310(d)(2)(iv)"},
			"iso-27001":   {"A.8.13", "A.8.24"},
			"nist-800-53": {"CP-9", "CP-10", "SC-28"},
			"pci-dss":     {"9.5.1", "3.5.1"},
		},
	},
	"vigo-stacks-backup": {
		ID:          "vigo-stacks-backup",
		Title:       "Vigo stacks snapshot",
		Description: "Snapshots the Vigo server's stacks (config-as-code state) on a schedule to a remote target. Vigo-specific: backs up the config-management *control plane* itself, not the managed-host data.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(7)(ii)(A)"},
			"iso-27001":   {"A.8.13", "A.8.32"},
			"nist-800-53": {"CP-9", "CM-2", "CM-3"},
			"soc2":        {"CC8.1"},
		},
	},
	"vigo-litestream-replication": {
		ID:          "vigo-litestream-replication",
		Title:       "Vigo SQLite replication (Litestream)",
		Description: "Streams the Vigo SQLite database via Litestream to S3-compatible storage with continuous WAL replication. Backs the Vigo server's audit-chain + state durability story.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(7)(ii)(A)", "164.310(d)(2)(iv)"},
			"iso-27001":   {"A.8.13", "A.5.29"},
			"nist-800-53": {"CP-9", "CP-10", "AU-9"},
			"pci-dss":     {"9.5.1", "9.5.1.2"},
			"soc2":        {"A1.2", "A1.3"},
		},
	},
	"log-processing": {
		ID:          "log-processing",
		Title:       "Log shipper with parsing/buffering",
		Description: "Tails host logs, parses structure, buffers to disk, and forwards to a downstream collector (fluent-bit, promtail, vector). Distinct from `log-shipping` (raw syslog forwarding) by the buffering + storage-capacity guarantee.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-3", "AU-4", "AU-6"},
		},
	},
	"log-processing-pci": {
		ID:          "log-processing-pci",
		Title:       "PCI-aware log shipper with parsing",
		Description: "Log shipper that asserts PCI 10.2/10.3 alignment (audit-trail capture + retention) on top of the basic parsing claim. Fluent-bit's PCI claim is the canonical fit.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-3", "AU-4", "AU-6"},
			"pci-dss":     {"10.2.1", "10.2.2"},
		},
	},
	"vigo-log-forward": {
		ID:          "vigo-log-forward",
		Title:       "Vigo container log forwarder",
		Description: "Forwards vigosrv container logs to a remote syslog/Loki endpoint with hash-chain durability claims layered in (Vigo's audit-trail story).",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(b)", "164.308(a)(1)(ii)(D)"},
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-3", "AU-4", "AU-6", "AU-12"},
			"pci-dss":     {"10.2.1", "10.2.2", "10.3.4"},
			"soc2":        {"CC7.2", "CC7.3"},
		},
	},
	"dns-server": {
		ID:          "dns-server",
		Title:       "DNS resolver / authoritative server",
		Description: "Runs a DNS server (BIND, Unbound, Knot, …) for recursive resolution or authoritative zone serving.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.20"},
			"nist-800-53": {"SC-20", "SC-21"},
		},
	},
	"dhcp-server": {
		ID:          "dhcp-server",
		Title:       "DHCP server",
		Description: "Runs a DHCP server that hands out leases on configured subnets.",
		Frameworks: map[string][]string{
			"nist-800-53": {"CM-6"},
		},
	},
	"audit-rules": {
		ID:          "audit-rules",
		Title:       "Auditd rule sets",
		Description: "Loads a rule set into the OS audit subsystem to capture specific syscalls + file accesses + auth events. Pairs with the `audit-trail` provision: `audit-trail` runs the daemon; `audit-rules` declares what the daemon watches.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(b)"},
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-2", "AU-3", "AU-12"},
			"pci-dss":     {"10.2.1"},
		},
	},
	"password-policy": {
		ID:          "password-policy",
		Title:       "Password complexity policy (pwquality)",
		Description: "Configures password-strength rules via pwquality / PAM. Narrow claim: complexity rules. Configcrates that also assert aging / lockout / history layer `password-policy-comprehensive` instead.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.5"},
			"nist-800-53": {"IA-5(1)"},
			"pci-dss":     {"8.3.6", "8.3.9"},
		},
	},
	"password-policy-comprehensive": {
		ID:          "password-policy-comprehensive",
		Title:       "Password policy (complexity + aging + lockout)",
		Description: "Configures the full password-policy surface: pwquality complexity + login.defs aging + PAM faillock lockout + history.",
		Frameworks: map[string][]string{
			"cis-rhel":    {"5.4.1.1", "5.4.1.2", "5.4.1.3", "5.4.1.4", "5.4.1.5"},
			"cis-ubuntu":  {"5.4.1.1", "5.4.1.2", "5.4.1.3", "5.4.1.4", "5.4.1.5"},
			"hipaa":       {"164.308(a)(5)(ii)(D)"},
			"iso-27001":   {"A.8.5"},
			"nist-800-53": {"IA-5"},
			"pci-dss":     {"8.3.5", "8.3.6", "8.3.7", "8.3.9"},
		},
	},
	"ssh-hardening": {
		ID:          "ssh-hardening",
		Title:       "OpenSSH server hardening",
		Description: "Configures the OpenSSH server with CIS-aligned settings: strong crypto, root login disabled, key-only auth, protocol pinning, idle timeout, max-auth-tries.",
		Frameworks: map[string][]string{
			"cis-rhel":    {"5.2.1", "5.2.4", "5.2.6", "5.2.7", "5.2.10", "5.2.13", "5.2.14", "5.2.15", "5.2.20"},
			"cis-ubuntu":  {"5.2.1", "5.2.4", "5.2.6", "5.2.7", "5.2.10", "5.2.13", "5.2.14", "5.2.15", "5.2.20"},
			"hipaa":       {"164.312(a)(1)", "164.312(d)", "164.312(e)(1)"},
			"iso-27001":   {"A.8.5", "A.5.14"},
			"nist-800-53": {"AC-17", "IA-2"},
			"pci-dss":     {"2.2.7", "8.3.1"},
		},
	},
	"selinux-enforcing": {
		ID:          "selinux-enforcing",
		Title:       "SELinux in enforcing mode",
		Description: "Sets SELinux to enforcing mode with a hardened policy (targeted or strict). RHEL-family hosts only.",
		Frameworks: map[string][]string{
			"cis-rhel":    {"1.6.1.1", "1.6.1.2", "1.6.1.3", "1.6.1.4", "1.6.1.5"},
			"iso-27001":   {"A.8.3"},
			"nist-800-53": {"AC-3"},
		},
	},
	"sudo-policy": {
		ID:          "sudo-policy",
		Title:       "Sudo least-privilege policy",
		Description: "Configures sudo with use_pty, logfile, default require-password, and command authorization — moves OS users out of direct-root and into per-command least privilege.",
		Frameworks: map[string][]string{
			"cis-rhel":    {"5.3.1", "5.3.2", "5.3.3", "5.3.4"},
			"cis-ubuntu":  {"5.3.1", "5.3.2", "5.3.3", "5.3.4"},
			"hipaa":       {"164.308(a)(4)(ii)(B)", "164.312(a)(1)"},
			"iso-27001":   {"A.8.2", "A.8.18"},
			"nist-800-53": {"AC-6", "AU-2"},
			"pci-dss":     {"7.2.1", "7.2.2", "7.2.3", "8.2.1", "10.2.1.2"},
		},
	},
	"anti-malware": {
		ID:          "anti-malware",
		Title:       "Anti-malware scanner",
		Description: "Runs an anti-malware scanner (ClamAV, Defender, …) with scheduled scans + signature updates + on-access scanning where supported.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.7"},
			"nist-800-53": {"SI-3", "SI-7"},
			"pci-dss":     {"5.2.1", "5.3.1"},
		},
	},
	"rootkit-detection": {
		ID:          "rootkit-detection",
		Title:       "Rootkit / kernel-module integrity detection",
		Description: "Runs a rootkit detector (rkhunter, chkrootkit) on a schedule and surfaces anomalies.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.7"},
			"nist-800-53": {"SI-3", "SI-4"},
		},
	},
	"host-vuln-scanning": {
		ID:          "host-vuln-scanning",
		Title:       "Host vulnerability scanning",
		Description: "Runs a local vulnerability scanner (Lynis, OpenSCAP, hardening checks) with scheduled scans + result archival.",
		Frameworks: map[string][]string{
			"hipaa":       {"164.308(a)(1)(ii)(D)"},
			"iso-27001":   {"A.8.8"},
			"nist-800-53": {"RA-5", "SI-2", "SI-7"},
			"pci-dss":     {"6.3.1", "6.3.3", "11.5.2"},
		},
	},
	"host-vuln-scanning-windows": {
		ID:          "host-vuln-scanning-windows",
		Title:       "Host vulnerability scanning (Windows)",
		Description: "Runs the Windows-platform vulnerability scanner (Defender baseline scans, MBSA-style audits).",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.8"},
			"nist-800-53": {"RA-5", "SI-2"},
			"pci-dss":     {"5.2.1", "6.3.3", "11.3.1"},
		},
	},
	"kerberos-auth": {
		ID:          "kerberos-auth",
		Title:       "Kerberos authentication client",
		Description: "Configures a Kerberos client (krb5 / SSPI on Windows) for the host so users can present TGT-derived service tickets to network services.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.5"},
			"nist-800-53": {"IA-2", "IA-5"},
		},
	},
	"tls-cert-acme": {
		ID:          "tls-cert-acme",
		Title:       "ACME-based TLS certificate management",
		Description: "Manages public TLS certificates via ACME (Let's Encrypt, ZeroSSL, BuyPass). Automatic renewal + reload of consumer services.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.24", "A.5.14"},
			"nist-800-53": {"SC-8", "SC-13", "SC-17"},
			"pci-dss":     {"4.2.1"},
		},
	},
	"internal-pki-ca": {
		ID:          "internal-pki-ca",
		Title:       "Internal PKI / certificate authority",
		Description: "Runs an internal CA (Step-CA, Vault PKI, EJBCA) that mints certs for internal services + workloads.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.24"},
			"nist-800-53": {"SC-8", "SC-12", "SC-13", "SC-17"},
			"pci-dss":     {"3.5.1", "4.2.1"},
		},
	},
	"system-ca-trust": {
		ID:          "system-ca-trust",
		Title:       "System CA trust store management",
		Description: "Installs / refreshes the host's system CA anchor set so the OS trust store accepts the operator's chosen roots (corporate CA, internal CA).",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.24"},
			"nist-800-53": {"SC-12", "SC-17"},
		},
	},
	"system-crypto-policy": {
		ID:          "system-crypto-policy",
		Title:       "System-wide cryptographic policy",
		Description: "Pins the host's system-wide crypto policy (RHEL crypto-policies, Debian openssl.cnf hardening) to a known baseline (FUTURE / FIPS / DEFAULT).",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.24", "A.5.14"},
			"nist-800-53": {"SC-12", "SC-13"},
			"pci-dss":     {"4.2.1"},
		},
	},
	"secrets-storage-hardening": {
		ID:          "secrets-storage-hardening",
		Title:       "Secrets storage permissions hardening",
		Description: "Enforces tight filesystem permissions on secret-bearing directories + files (Vigo secrets vault, application credentials, TLS private keys).",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(a)(2)(iv)"},
			"iso-27001":   {"A.8.3", "A.8.24"},
			"nist-800-53": {"AC-3", "AC-6", "SC-12", "SC-28"},
			"pci-dss":     {"3.5.1", "7.2.1"},
			"soc2":        {"CC6.1"},
		},
	},
	"tls-public-cert": {
		ID:          "tls-public-cert",
		Title:       "Public-facing TLS material",
		Description: "Provisions and rotates TLS material for public-facing services (Vigo's UI/REST endpoint, customer-facing app TLS).",
		Frameworks: map[string][]string{
			"hipaa":       {"164.312(e)(1)"},
			"iso-27001":   {"A.8.24", "A.5.14"},
			"nist-800-53": {"SC-8", "SC-13", "SC-17"},
			"pci-dss":     {"4.2.1"},
			"soc2":        {"CC6.7"},
		},
	},
	"filesystem-snapshots": {
		ID:          "filesystem-snapshots",
		Title:       "Filesystem snapshots",
		Description: "Configures snapshot-capable filesystems (btrfs, ZFS) with retention so point-in-time restores are available.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.13"},
			"nist-800-53": {"CP-9"},
		},
	},
	"filesystem-encrypted-snapshots": {
		ID:          "filesystem-encrypted-snapshots",
		Title:       "Encrypted filesystem snapshots",
		Description: "Snapshot-capable filesystem with at-rest encryption (ZFS native encryption, btrfs+LUKS).",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.13"},
			"nist-800-53": {"CP-9", "SC-28"},
		},
	},
	"storage-raid": {
		ID:          "storage-raid",
		Title:       "Block-storage RAID",
		Description: "Configures software RAID (mdadm) for disk redundancy.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.13"},
			"nist-800-53": {"CP-9", "CP-10"},
		},
	},
	"iscsi-initiator": {
		ID:          "iscsi-initiator",
		Title:       "iSCSI initiator with CHAP",
		Description: "Configures the host as an iSCSI initiator with CHAP authentication for remote block-storage access.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.20"},
			"nist-800-53": {"SC-8", "SC-12"},
		},
	},
	"failover-cluster": {
		ID:          "failover-cluster",
		Title:       "Failover cluster (VRRP)",
		Description: "Runs a VRRP-style failover daemon (keepalived) so a virtual IP migrates between hosts on failure.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.14"},
			"nist-800-53": {"CP-7"},
		},
	},
	"container-keepalive": {
		ID:          "container-keepalive",
		Title:       "Container watchdog / keepalive",
		Description: "Runs a watchdog that monitors critical containers and restarts them on failure (Vigo's docker-keepalive pattern).",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.6", "A.5.29"},
			"nist-800-53": {"CP-2", "CP-10"},
			"soc2":        {"A1.2"},
		},
	},
	"login-banner": {
		ID:          "login-banner",
		Title:       "Login banner / MOTD",
		Description: "Sets a login banner (issue / motd) with the operator's chosen warning text — CIS-aligned framing for legal notice.",
		Frameworks: map[string][]string{
			"cis-rhel":    {"1.7.1", "1.7.2", "1.7.3"},
			"cis-ubuntu":  {"1.7.1", "1.7.2", "1.7.3"},
			"iso-27001":   {"A.8.3"},
			"nist-800-53": {"AC-8"},
		},
	},
	"vigo-login-banner": {
		ID:          "vigo-login-banner",
		Title:       "Vigo operator MOTD",
		Description: "Sets a Vigo-managed MOTD that surfaces fleet-membership status + last-converged timestamp at SSH login.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.3"},
			"nist-800-53": {"AC-8"},
		},
	},
	"os-user-management": {
		ID:          "os-user-management",
		Title:       "OS user account management",
		Description: "Creates and manages OS user accounts with declarative shell, groups, SSH keys, and home-directory permissions.",
		Frameworks: map[string][]string{
			"cis-rhel":    {"5.4.1"},
			"cis-ubuntu":  {"5.4.1"},
			"hipaa":       {"164.312(a)(1)", "164.312(a)(2)(i)", "164.312(d)"},
			"iso-27001":   {"A.5.16", "A.8.5"},
			"nist-800-53": {"AC-3", "AC-6", "AC-6(1)", "IA-2", "IA-5"},
			"pci-dss":     {"8.1.1", "8.1.2", "8.2.6"},
			"soc2":        {"CC6.1", "CC6.2", "CC6.3"},
		},
	},
	"journal-retention": {
		ID:          "journal-retention",
		Title:       "systemd-journal retention cap",
		Description: "Caps the systemd-journal storage size so the host doesn't exhaust disk via journal growth. Complements `log-rotation` for the journald-only case.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AU-4"},
		},
	},
	"network-device-baseline": {
		ID:          "network-device-baseline",
		Title:       "Network device baseline (switch/router)",
		Description: "Configures baseline hardening on a managed network device: banner, NTP, syslog destination, snmp community policy. Used by the Vigo network-device executors (Cisco, Juniper).",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"AC-8", "AU-8", "CM-6"},
		},
	},
	"network-segmentation": {
		ID:          "network-segmentation",
		Title:       "Network segmentation (VLANs / subnets)",
		Description: "Configures VLAN tags / port assignments on a switch so traffic between trust zones is segmented at L2.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.22"},
			"nist-800-53": {"AC-4", "SC-7"},
			"pci-dss":     {"1.2.1", "1.4.1"},
		},
	},
	"imap-encryption": {
		ID:          "imap-encryption",
		Title:       "IMAP/POP3 with TLS",
		Description: "Runs an IMAP/POP3 server (Dovecot) with STARTTLS / implicit TLS so mailbox access is encrypted in transit.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.24", "A.5.14"},
			"nist-800-53": {"AC-3", "SC-8", "SC-13"},
		},
	},
	"dkim-signing": {
		ID:          "dkim-signing",
		Title:       "DKIM outbound signing",
		Description: "Signs outbound mail with DKIM so receivers can verify message origin + integrity.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.5.14"},
			"nist-800-53": {"SC-8", "SC-23"},
		},
	},
	"dmarc-enforcement": {
		ID:          "dmarc-enforcement",
		Title:       "DMARC policy enforcement",
		Description: "Enforces DMARC on inbound mail (reject / quarantine) so spoofed senders are filtered.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.5.14"},
			"nist-800-53": {"SC-8", "SI-3"},
		},
	},
	"service-uptime-probe": {
		ID:          "service-uptime-probe",
		Title:       "Service uptime probe (host-resident)",
		Description: "Runs host-resident service-uptime probes (NRPE, monit) that report local service state to a central monitoring system.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"SI-4"},
		},
	},
	"vulnerability-scanning": {
		ID:          "vulnerability-scanning",
		Title:       "Vulnerability scanning",
		Description: "Runs a vulnerability scanner (OpenVAS, Nessus, …) on a schedule and surfaces findings.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.8"},
			"nist-800-53": {"RA-5"},
			"pci-dss":     {"11.2.1", "11.3.1"},
		},
	},
	"system-resource-monitoring": {
		ID:          "system-resource-monitoring",
		Title:       "System resource monitoring (CPU/mem/disk)",
		Description: "Exposes host resource metrics (node-exporter, collectd, telegraf, …) so saturation and resource exhaustion are visible.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
			"nist-800-53": {"SI-4", "AU-12"},
			"soc2":        {"CC7.1"},
		},
	},
	"monitoring-probe": {
		ID:          "monitoring-probe",
		Title:       "External probe / synthetic monitoring",
		Description: "Runs a probe (blackbox-exporter, SNMP poller, NRPE) that synthesizes checks against services from outside.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.6"},
			"nist-800-53": {"SI-4", "CA-7"},
		},
	},
	"alerting": {
		ID:          "alerting",
		Title:       "Alert delivery",
		Description: "Runs an alerting router (Alertmanager or equivalent) that converts monitoring conditions into pages/emails/tickets.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.6.8", "A.5.25"},
			"nist-800-53": {"SI-4", "IR-4", "IR-6"},
		},
	},
	"dashboard": {
		ID:          "dashboard",
		Title:       "Operator observability dashboard",
		Description: "Runs a dashboard front-end (Grafana, Kibana, …) over the host's monitoring + log backends.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.8.15"},
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
			"iso-27001":   {"A.8.6", "A.5.29"},
			"nist-800-53": {"CP-2", "CP-10"},
			"soc2":        {"A1.2"},
		},
	},
	"disk-health-monitoring": {
		ID:          "disk-health-monitoring",
		Title:       "Disk health monitoring",
		Description: "Polls SMART attributes (smartmontools / equivalent) and surfaces drive failure predictors before they take the host down.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.5.29"},
			"nist-800-53": {"SI-4", "CP-2"},
		},
	},
	"email-anti-malware": {
		ID:          "email-anti-malware",
		Title:       "Email anti-malware / anti-spam",
		Description: "Runs an email content scanner (rspamd, ClamAV, …) on mail in transit.",
		Frameworks: map[string][]string{
			"iso-27001":   {"A.5.14"},
			"nist-800-53": {"SI-3", "SI-8"},
		},
	},
}

// ResolveProvision returns the framework → control IDs map for a given
// provision ID. Returns nil for an unknown ID (modlint catches unknown
// provisions at configcrate-load; the loader treats nil as zero contribution).
func ResolveProvision(id string) map[string][]string {
	p, ok := ConfigcrateProvisions[id]
	if !ok {
		return nil
	}
	return p.Frameworks
}

// KnownProvisionIDs returns every catalog ID in alphabetical order.
// Used by modlint's did-you-mean and by `vigocli compliance provisions`.
func KnownProvisionIDs() []string {
	ids := make([]string, 0, len(ConfigcrateProvisions))
	for id := range ConfigcrateProvisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
