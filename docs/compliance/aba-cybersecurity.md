
# ABA Cybersecurity Compliance

This framework maps the American Bar Association's cybersecurity requirements to Vigo's technical controls. It combines three sources of attorney cybersecurity obligations:

- **ABA Formal Opinion 477R** — Securing Communication of Protected Client Information (2017)
- **Model Rule 1.1, Comment 8** — Duty of technology competence
- **Model Rule 1.6(c)** — Reasonable efforts to prevent unauthorized disclosure of client information

## Who This Applies To

Every attorney admitted to practice in a US jurisdiction that has adopted the ABA Model Rules (all 50 states plus DC). The "reasonable efforts" standard in Rule 1.6(c) and the technology competence duty in Rule 1.1 are not optional — they are professional obligations enforceable by state bar disciplinary authorities.

## Control Mapping

### Model Rule 1.1 — Technology Competence

| Control | Title | Status | Vigo Implementation |
|---------|-------|--------|---------------------|
| ABA-1.1 | Technology Competence | Partial | Infrastructure configuration managed by Vigo ensures technical competence of systems; attorney training on technology is organizational |

### Model Rule 1.6(c) — Reasonable Efforts

| Control | Title | Status | Vigo Implementation |
|---------|-------|--------|---------------------|
| ABA-1.6-1 | Encryption at Rest | Enforced | Disk encryption (LUKS/BitLocker) via modules; verified by convergence |
| ABA-1.6-2 | Encryption in Transit | Enforced | TLS enforcement, SSH hardening, plaintext protocol blocking |
| ABA-1.6-3 | Access Controls | Enforced | User management, sudo restrictions, SSH key-only auth, shared account removal |
| ABA-1.6-4 | Multi-Factor Authentication | Partial | PAM MFA modules for system auth; application-level MFA is external |
| ABA-1.6-5 | Secure Disposal | Enforced | Secure file deletion, disk wipe; Sandgorgon NIST 800-88 decommission |

### Formal Opinion 477R — Specific Requirements

| Control | Title | Status | Vigo Implementation |
|---------|-------|--------|---------------------|
| ABA-477R-1 | Threat Assessment | Partial | CVE, rootkit, and malware scanning; formal risk assessment is organizational |
| ABA-477R-2 | Matter Sensitivity Classification | Partial | Compliance tagging and risk scoring; data classification policy is organizational |
| ABA-477R-3 | Secure Communication Channels | Enforced | TLS enforcement, SSH hardening, firewall rules |
| ABA-477R-4 | Endpoint Security | Enforced | Hardening, screen lock, disk encryption, USB restrictions, CIS benchmarks |
| ABA-477R-5 | Mobile Device Security | Partial | Hardening where agent is deployed; BYOD/MDM is external |
| ABA-477R-6 | Patch Management | Enforced | Package updates, vulnerability scanning, CVE alerting |
| ABA-477R-7 | Firewall and Network Security | Enforced | Firewall rules, network segmentation, port allowlisting |
| ABA-477R-8 | Anti-Malware Protection | Enforced | ClamAV/Defender enforcement, scheduled scanning, alerting |
| ABA-477R-9 | Audit Logging | Enforced | Tamper-evident audit trail, system audit logging (auditd/syslog) |
| ABA-477R-10 | Backup and Recovery | Enforced | Backup agent enforcement; tested recovery via Connwaer |
| ABA-477R-11 | Incident Response | Partial | Automated detection and alerting; written IRP is organizational |
| ABA-477R-12 | Vendor Oversight | Partial | Convergence monitoring of vendor infrastructure; contract review is organizational |

## Example Module Assignment

```yaml
# stockpile/roles.vgo
roles:
  aba-cyber:
    modules:
      - aba-endpoint-hardening
      - aba-network-security
      - aba-audit-logging
      - aba-malware-protection
      - aba-patch-management
      - aba-backup
```

```yaml
# stockpile/nodes.vgo
envoys:
  - match: "*.lawfirm.com"
    roles: [aba-cyber]
```

## Generating Reports

```bash
# JSON report
vigocli report abacyber

# Printable HTML report for audit file
vigocli report abacyber --format html --output aba-compliance-report.html
```

## What Vigo Cannot Prove

Several ABA requirements are organizational processes that no software can enforce:

- **Attorney training** (Rule 1.1) — Vigo manages infrastructure, not people
- **Data classification** (Opinion 477R) — Matter sensitivity is a legal judgment
- **Written policies** — Vigo enforces policies expressed as modules, but the policy documents themselves are organizational
- **Vendor contracts** — Vigo monitors vendor-managed infrastructure but cannot review contracts
- **Incident response plans** — Vigo detects and alerts; the human response plan is organizational

These controls are marked as "partial" in reports. Your compliance report honestly states what Vigo enforces vs. what requires organizational processes.

## Related

- [Compliance Reporting](reporting.md) — JSON, HTML, and OSCAL report formats
- [Security Scanning](security-scanning.md) — CVE, rootkit, and malware detection
- [Tamper-Evident Audit Trail](tamper-evident-audit.md) — SHA-256 hash chain
- [NY DFS Cybersecurity](ny-dfs-cyber.md) — Additional requirements for firms handling financial data

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
