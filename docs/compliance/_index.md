
---
title: Compliance
---

# Compliance

Guides for deploying Vigo in regulated environments.

## Envoy compliance vs. Vigo product compliance

Two separate concepts — keep them straight.

- **Envoy compliance** is what the framework reports under `/api/v1/report/<slug>` measure. They answer: "do my managed nodes satisfy the framework?" A control counts as covered for an envoy only when (a) one of the envoy's assigned modules is tagged with that control via `compliance:`, (b) a Connwaer trait check on the envoy itself escalates the control to satisfied, or (c) a waiver applies. Vigo's own server-side architecture does **not** inflate per-envoy or fleet aggregates — a freshly enrolled envoy with no modules assigned reports 0 covered, 0 waived, 0 partial, every catalog control missing. That is the honest number.
- **Vigo product compliance** is what the Vigo server and agent satisfy by construction (mTLS transport, RBAC, password policy, audit hash chain, secret encryption, etc.). It lives in `server/compliance/AppControls` and is surfaced separately on the `/platform/compliance` page (under Platform → Data in the nav). Useful for vendor questionnaires and customer audits; never folded into framework scoring.

The motivation: prior to 0.32.0 the framework reports baked Vigo's own architecture into per-envoy scores, so the same control could be marked satisfied for every envoy in the fleet because the server hardens its own gRPC traffic. That conflated two different audiences (fleet operators vs. customers auditing Vigo itself) and inflated the numbers operators care about. The 0.32.0 split keeps each surface honest.

## Control scope

Every catalog control carries a **scope** that classifies who is responsible for satisfying it (introduced 0.32.3):

- **enforced** — Vigo modules apply the technical control. Coverage flips when one of the envoy's assigned modules carries the matching `compliance:` tag.
- **attested** — Vigo collects evidence but doesn't enforce. Drift detection, the security_scan trait, audit-chain integrity, and Connwaer fleet checks all feed this lane.
- **documented** — the operator satisfies the control by uploading an artifact (BAA, incident-response plan, risk assessment, training records, signed contract). Documentation upload + retention ships in a follow-up; until then, documented controls render as "missing — requires uploaded documentation".
- **not_applicable** — the control is out of scope for this deployment (e.g., HIPAA `164.308(a)(4)(ii)(A)` "Isolating Health Care Clearinghouse Functions" for a non-clearinghouse). Excluded from the framework denominator.

Per-envoy detail pages render Scope alongside Status so a missing control tells the operator whether they need to assign a module or upload a document.

## Quick Start

Vigo ships with pre-built compliance roles for 22 frameworks. Copy the roles file and the modules you need:

```bash
# 1. Copy the compliance roles
cp example-configs/stockpile/compliance-roles.vgo.example /srv/vigo/worksite/stockpile/compliance-roles.vgo

# 2. Copy framework modules (example: HIPAA + CIS Ubuntu)
cp example-configs/stockpile/modules/compliance/hipaa/*.vgo.example /srv/vigo/worksite/stockpile/modules/
cp example-configs/stockpile/modules/compliance/cis-ubuntu/*.vgo.example /srv/vigo/worksite/stockpile/modules/
for f in /srv/vigo/worksite/stockpile/modules/*.vgo.example; do mv "$f" "${f%.example}"; done

# 3. Assign roles in your match blocks
cat >> /srv/vigo/worksite/scaffolding/envoys.vgo <<'EOF'
envoys:
  - match: "*.example.com"
    roles:
      - hipaa
      - name: cis-ubuntu
        when: "distro('ubuntu')"
EOF

# 4. Publish
vigocli config publish
```

For CIS benchmarks on mixed-OS fleets, use conditional role references to auto-select the right modules per OS:

```yaml
roles:
  - name: cis-ubuntu
    when: "distro('ubuntu')"
  - name: cis-rhel
    when: "distro('rhel') || distro('centos') || distro('rocky') || distro('alma')"
  - name: cis-windows
    when: "os_family('windows')"
```

See `example-configs/stockpile/compliance-roles.vgo.example` for the full list of available roles.

## Topics

- [Compliance Reporting](reporting.md) -- HIPAA reports in JSON, HTML, and OSCAL formats
- [HITRUST CSF](hitrust.md) -- HITRUST CSF control mapping across 8 domains
- [SOC 2](soc2.md) -- Trust Services Criteria across CC1-CC9, A1, C1, PI1, P1-P8 (60 controls)
- [PCI DSS v4.0](pci-dss.md) -- ~245 sub-requirements across Reqs 1-12
- [CIS Benchmarks](cis-benchmarks.md) -- Ubuntu, RHEL, Windows Level 1 + Level 2 enforcement modules
- [NIST 800-53](nist-800-53.md) -- 287 Moderate-baseline controls across 20 families (Federal/FedRAMP)
- [ISO 27001](iso-27001.md) -- 93 Annex A controls (2022 revision)
- [GDPR](gdpr.md) -- 46 articles covering technical and organizational measures, data subject rights, breach handling, DPO
- [NERC CIP](nerc-cip.md) -- 18 requirements across 8 CIP standards (energy sector)
- [Cyber Essentials+](cyber-essentials-plus.md) -- 29 controls across 5 themes (UK Government)
- [IEC 62443](iec-62443.md) -- 37 system requirements across 7 foundational requirements (Industrial Cybersecurity)
- [ABA Cybersecurity](aba-cybersecurity.md) -- ABA 477R + Model Rules 1.1/1.6 for law firms (18 controls)
- [NY DFS Cybersecurity](ny-dfs-cyber.md) -- 23 NYCRR 500 for firms handling financial institution data (15 controls)
- [Database Encryption at Rest](database-encryption.md) -- LUKS/dm-crypt for SQLite, TDE for PostgreSQL
- [HIPAA Compliance Checklist](hipaa-checklist.md) -- 164.312 requirement mapping and deployment verification
- [BAA Template](baa-template.md) -- Business Associate Agreement template for ePHI environments
- [Incident Response Plan](incident-response.md) -- Security incident detection, containment, and recovery
- [Emergency Access Procedure](emergency-access.md) -- Break-glass procedure (HIPAA 164.312(a)(2)(ii))
- [Decommissioning Procedures](decommissioning.md) -- Secure node and server retirement
- [Tamper-Evident Audit Trail](tamper-evident-audit.md) -- SHA-256 hash chain for tamper detection
- [Compliance Export](compliance-export.md) -- SIEM, CMDB, OSCAL feeds
- [Security Scanning](security-scanning.md) -- CVE scanning, hardening audits, rootkit and malware detection
- [Authentication](../operations/authentication.md) -- Session timeout, password policy, audit logging
- [Backup & Recovery](../operations/backup.md) -- Encrypted backups with SHA-256 integrity verification
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
