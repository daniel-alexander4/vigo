
---
title: Cyber Essentials+
---

# Cyber Essentials+ Compliance

Cyber Essentials is a UK Government-backed cybersecurity certification scheme. Cyber Essentials+ (CE+) adds hands-on technical verification to the self-assessment baseline. Vigo maps **29 controls** across 5 themes plus supplementary monitoring requirements.

## Coverage Summary

| Theme | Name | Controls |
|-------|------|----------|
| FW | Firewalls | 5 |
| SC | Secure Configuration | 6 |
| SU | Security Update Management | 4 |
| AC | User Access Control | 6 |
| MW | Malware Protection | 5 |
| ML | Monitoring and Logging (CE+ supplementary) | 3 |

## Control Mapping

### Theme 1 — Firewalls

| Control | Title | Vigo Implementation | Status |
|---------|-------|---------------------|--------|
| CE-FW-1 | Boundary Firewalls | Firewall executor enforces iptables/nftables rules; mTLS on management traffic | Satisfied |
| CE-FW-2 | Firewall Rule Review | Rules declared in YAML; continuous drift detection reverts unauthorized changes | Satisfied |
| CE-FW-3 | Default Deny | PCI/CIS hardening modules enforce default-deny; INPUT/FORWARD DROP | Satisfied |
| CE-FW-4 | Host-Based Firewalls | Per-host rules via firewall executor; portable across iptables, nftables, pf, Windows Firewall | Satisfied |
| CE-FW-5 | Administrative Interface Protection | Admin UI on TLS; mTLS for agents; no plaintext management interfaces | Satisfied |

### Theme 2 — Secure Configuration

| Control | Title | Vigo Implementation | Status |
|---------|-------|---------------------|--------|
| CE-SC-1 | Remove Unnecessary Software | Package executor enforces absent state; CIS modules remove unused packages | Satisfied |
| CE-SC-2 | Disable Unnecessary Accounts | User executor manages accounts; system-accounts module disables defaults | Satisfied |
| CE-SC-3 | Change Default Passwords | Secrets provider injects unique credentials; no default passwords | Satisfied |
| CE-SC-4 | Disable Autorun | CIS modules disable USB autorun/automount; Windows modules disable AutoPlay | Satisfied |
| CE-SC-5 | Secure Configuration Baselines | YAML modules define desired state; continuous enforcement every check-in | Satisfied |
| CE-SC-6 | Device Lockdown | Screen lock timeout via exec/registry modules; login banner enforcement | Satisfied |

### Theme 3 — Security Update Management

| Control | Title | Vigo Implementation | Status |
|---------|-------|---------------------|--------|
| CE-SU-1 | Software Licensing | Package executor tracks versions; trait collectors report inventory | Satisfied |
| CE-SU-2 | Patch Within 14 Days | Package executor enforces versions; task system for fleet-wide patching | Satisfied |
| CE-SU-3 | Unsupported Software Removal | Package executor can enforce absent state for EOL software | Satisfied |
| CE-SU-4 | Automatic Update Configuration | Unattended-upgrades/dnf-automatic modules; Windows Update executor | Satisfied |

### Theme 4 — User Access Control

| Control | Title | Vigo Implementation | Status |
|---------|-------|---------------------|--------|
| CE-AC-1 | User Account Management | RBAC with admin/viewer + 13 permissions; per-user accounts with UUIDs | Satisfied |
| CE-AC-2 | Least Privilege | Fine-grained RBAC; viewer role read-only; no shared accounts | Satisfied |
| CE-AC-3 | Authentication Requirements | Password complexity via bcrypt; session timeout; TOTP MFA; OIDC/SSO | Satisfied |
| CE-AC-4 | Multi-Factor Authentication | TOTP MFA for web UI; OIDC with MFA delegation; isowebauth SSH key signing | Satisfied |

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| CE-AC-5 | Account Lockout | Brute-force protection via session management; API token rate limiting | Satisfied |
| CE-AC-6 | Privileged Access Management | Admin role gated by RBAC; peer auth for local CLI; all admin actions audited | Satisfied |

### Theme 5 — Malware Protection

| Control | Title | Vigo Implementation | Status |
|---------|-------|---------------------|--------|
| CE-MW-1 | Anti-Malware Software | AV/EDR enforcement via package/service executors; ClamAV, OSSEC modules | Satisfied |
| CE-MW-2 | Anti-Malware Configuration | AV service state enforced continuously; drift detected and corrected | Satisfied |
| CE-MW-3 | Anti-Malware Updates | Package executor keeps AV signatures current; task dispatch for emergency updates | Satisfied |
| CE-MW-4 | Application Allow-Listing | Package executor controls installed software; full whitelisting is OS-level | Partial |
| CE-MW-5 | Sandboxing and Code Execution Prevention | Kernel hardening (ASLR, NX, seccomp); full sandboxing is application-level | Partial |

### Supplementary — Monitoring and Logging (CE+ only)

| Control | Title | Vigo Implementation | Status |
|---------|-------|---------------------|--------|
| CE-ML-1 | Security Event Logging | Tamper-evident audit trail with SHA-256 hash chain | Satisfied |
| CE-ML-2 | Log Retention | Configurable retention; Litestream continuous backup | Satisfied |
| CE-ML-3 | Incident Detection | Continuous compliance monitoring; drift alerts; SMTP/webhook notifications | Satisfied |

## Quick Start

```bash
cp envoy-configs/modules/compliance/cyber-essentials/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/ce-*.vgo.example; do mv "$f" "${f%.example}"; done
cp envoy-configs/compliance-roles.vgo.example /srv/vigo/stack/stage/compliance-roles.vgo
```

Assign the `cyber-essentials` role to nodes:

```yaml
envoys:
  - match: "*.example.com"
    roles: [cyber-essentials]
```

Then publish and verify: `vigocli config publish && vigocli report cyberessentials`

## Generating Reports

```bash
vigocli report cyberessentials
vigocli report cyberessentials --format html --output ce-report.html
```

## Key Capabilities for CE+ Assessment

| CE+ Requirement | Vigo Implementation |
|-----------------|---------------------|
| Boundary firewalls | Firewall executor across iptables, nftables, pf, Windows Firewall |
| Secure configuration | YAML modules = desired state, enforced every check-in |
| Patch management | Package executor + fleet-wide task dispatch within 14-day window |
| Access control | RBAC, MFA, session timeout, unique accounts |
| Malware protection | AV/EDR package + service enforcement, signature updates |
| Vulnerability scanning | Security scanning traits, CVE detection |
| Logging and monitoring | Tamper-evident audit trail, drift alerts, webhook notifications |

## Cross-Reference to Other Frameworks

| CE+ Theme | NIST 800-53 | ISO 27001 | CIS |
|-----------|-------------|-----------|-----|
| Firewalls | SC-7, SC-8 | A.13.1 | 3.x (Network) |
| Secure Configuration | CM-6, CM-7 | A.12.5, A.12.6 | 1.x, 5.x |
| Security Updates | SI-2 | A.12.6 | 3.4 |
| Access Control | AC-2, AC-3, IA-2 | A.9.1, A.9.2, A.9.4 | 4.x, 5.x |
| Malware Protection | SI-3 | A.12.2 | 8.x |

## Controls Not Covered

| Area | Why |
|------|-----|
| Physical security | Physical access controls (locks, CCTV) |
| Mobile device management | BYOD/MDM policies (organizational) |
| Cloud service configuration | Cloud-specific controls (AWS/Azure IAM) — see cloud-specific modules |
| Email security | DMARC/SPF/DKIM (DNS-level, not host config) |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
