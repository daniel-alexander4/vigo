
---
title: GDPR
---

# GDPR Compliance

The General Data Protection Regulation (EU 2016/679) governs the processing of personal data in the European Union. Vigo maps **13 GDPR articles** covering the technical and organizational measures relevant to infrastructure management.

## Coverage Summary

| Article | Title | Status |
|---------|-------|--------|
| Art.5(1)(f) | Integrity and Confidentiality | Satisfied |
| Art.17(1) | Right to Erasure | Satisfied |
| Art.25(1-2) | Data Protection by Design/Default | Satisfied |
| Art.30(1) | Records of Processing Activities | Satisfied |
| Art.32(1)(a-d) | Security of Processing | Satisfied |
| Art.32(2) | Risk Assessment | Partial |
| Art.33(1,3) | Breach Notification | Partial |
| Art.35(1) | Data Protection Impact Assessment | Partial |

"Partial" status indicates that Vigo provides the technical evidence but the organizational process (notification within 72 hours, formal DPIA) must be handled by the data controller.

## Quick Start

```bash
cp envoy-configs/modules/compliance/gdpr/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/gdpr-*.vgo.example; do mv "$f" "${f%.example}"; done
cp envoy-configs/compliance-roles.vgo.example /srv/vigo/stack/stage/compliance-roles.vgo
```

Assign the `gdpr` role to nodes:

```yaml
envoys:
  - match: "*.example.com"
    roles: [gdpr]
```

Then publish and verify: `vigogo config publish && vigogo report gdpr`

## Generating Reports

```bash
vigogo report gdpr
vigogo report gdpr --format html --output gdpr-report.html
```

## Technical Measures (Art. 32)

| Measure | Implementation |
|---------|----------------|
| Encryption at rest | AES-256-GCM for secrets and backups |
| Encryption in transit | mTLS (TLS 1.3) on all traffic |
| Access control | RBAC with 13 permissions, TOTP MFA |
| Audit trail | Tamper-evident SHA-256 hash chain |
| Backup and recovery | Litestream continuous replication, encrypted snapshots |
| Integrity verification | ED25519 signatures, hash chain verification |
| Data minimization | Traits auto-discovered, not user-provided PII |
| Erasure | Cascade delete on decommission, documented procedure |

## Important Note

GDPR compliance is primarily about organizational processes — consent, lawful basis, data subject rights, DPIAs, and breach notification procedures. Vigo provides the technical infrastructure but cannot substitute for legal and organizational compliance measures. Work with your Data Protection Officer to ensure full GDPR compliance.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
