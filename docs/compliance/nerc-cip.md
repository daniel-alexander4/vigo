
---
title: NERC CIP
---

# NERC CIP Compliance

NERC CIP (North American Electric Reliability Corporation Critical Infrastructure Protection) standards protect the Bulk Electric System (BES) from cyber threats. Vigo maps **18 requirements** across 8 CIP standards.

## Coverage Summary

| Standard | Name | Requirements |
|----------|------|-------------|
| CIP-002 | BES Cyber System Categorization | 1 |
| CIP-003 | Security Management Controls | 2 |
| CIP-004 | Personnel and Training | 2 |
| CIP-005 | Electronic Security Perimeters | 2 |
| CIP-007 | Systems Security Management | 5 |
| CIP-009 | Recovery Plans | 2 |
| CIP-010 | Configuration Change Management | 2 |
| CIP-011 | Information Protection | 2 |

## Quick Start

```bash
cp example-configs/modules/compliance/nerc-cip/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/nerc-*.vgo.example; do mv "$f" "${f%.example}"; done
cp example-configs/compliance-roles.vgo.example /srv/vigo/stack/stage/compliance-roles.vgo
```

Assign the `nerc-cip` role to nodes:

```yaml
envoys:
  - match: "*.example.com"
    roles: [nerc-cip]
```

Then publish and verify: `vigocli config publish && vigocli report nerccip`

## Generating Reports

```bash
vigocli report nerccip
vigocli report nerccip --format html --output nerccip-report.html
```

## Key Capabilities for Energy Sector

| NERC CIP Need | Vigo Implementation |
|---------------|----------------------|
| Asset inventory | 25 trait collectors, FleetIndex |
| Configuration baselines | YAML modules = desired state |
| Change management | Stage → publish, approval workflow, audit trail |
| Patch management | Package executor, fleet-wide task dispatch |
| Access control | RBAC, MFA, session timeout |
| Security monitoring | Continuous drift detection, compliance alerts |
| Incident response | Audit trail, emergency access, documented plan |
| Recovery | Encrypted backups, Litestream, config-as-code rebuild |
| FIM | Drift detection + AIDE module |

## Standards Not Covered

| Standard | Title | Why |
|----------|-------|-----|
| CIP-006 | Physical Security | Physical access controls |
| CIP-008 | Incident Reporting | Reporting to NERC (organizational) |
| CIP-012 | Communications Between Control Centers | SCADA/EMS protocols |
| CIP-013 | Supply Chain Risk Management | Vendor management |
| CIP-014 | Physical Security (Transmission) | Substation physical security |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
