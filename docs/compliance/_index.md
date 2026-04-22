
---
title: Compliance
---

# Compliance

Guides for deploying Vigo in regulated environments.

## Quick Start

Vigo ships with pre-built compliance roles for 22 frameworks. Copy the roles file and the modules you need:

```bash
# 1. Copy the compliance roles
cp example-configs/stockpile/compliance-roles.vgo.example /srv/vigo/worksite/stockpile/compliance-roles.vgo

# 2. Copy framework modules (example: HIPAA + CIS Ubuntu)
cp example-configs/stockpile/stones/compliance/hipaa/*.vgo.example /srv/vigo/worksite/stockpile/stones/
cp example-configs/stockpile/stones/compliance/cis-ubuntu/*.vgo.example /srv/vigo/worksite/stockpile/stones/
for f in /srv/vigo/worksite/stockpile/stones/*.vgo.example; do mv "$f" "${f%.example}"; done

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
- [HITRUST CSF](hitrust.md) -- HITRUST CSF control mapping across 8 domains (31 controls)
- [SOC 2](soc2.md) -- Trust Services Criteria mapping (32 criteria across 5 categories)
- [PCI DSS v4.0](pci-dss.md) -- 42 requirements mapped + 4 enforcement modules
- [CIS Benchmarks](cis-benchmarks.md) -- Ubuntu, RHEL, Windows Level 1 enforcement modules
- [NIST 800-53](nist-800-53.md) -- 41 controls across 10 families (Federal/FedRAMP)
- [ISO 27001](iso-27001.md) -- 33 Annex A controls across 11 domains
- [GDPR](gdpr.md) -- 13 articles covering technical measures
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
