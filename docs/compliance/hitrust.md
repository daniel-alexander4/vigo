
---
title: HITRUST CSF Compliance
---

# HITRUST CSF Compliance

HITRUST CSF (Common Security Framework) harmonizes HIPAA, NIST 800-53, ISO 27001, PCI DSS, and other frameworks into a single certifiable standard. Vigo maps its technical controls to HITRUST CSF control references across 8 of the 19 control domains.

## Coverage Summary

Vigo satisfies or provides evidence for **31 HITRUST CSF control references** across the technically enforceable domains. The remaining domains (HR Security, Physical Security, Privacy Practices, etc.) require organizational policies that no configuration management tool can automate.

| Domain | Name | Controls Mapped | Status |
|--------|------|----------------|--------|
| 01 | Access Control | 7 | Satisfied |
| 06 | Compliance | 4 | Satisfied (audit chain dependent) |
| 07 | Asset Management | 1 | Satisfied |
| 09 | Communications and Operations Management | 10 | Satisfied |
| 10 | Systems Acquisition, Development, Maintenance | 6 | Satisfied |
| 11 | Information Security Incident Management | 2 | Satisfied |
| 12 | Business Continuity Management | 1 | Satisfied |
| 16 | Endpoint Protection | 1 | Satisfied |

## Control Mapping

### Domain 01 — Access Control

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 01.b | User Registration | Per-user accounts with unique UUIDs, RBAC roles, 13 fine-grained permissions |
| 01.c | Privilege Management | Fine-grained RBAC: fleet.read, config.publish, users.manage, etc. Role-based defaults |
| 01.d | User Password Management | 12-char minimum, complexity rules, bcrypt hashing, AES-256-GCM encrypted storage |
| 01.i | Policy on Use of Network Services | mTLS enforced on all gRPC/REST traffic, no plaintext transport ever |
| 01.j | User Auth for External Connections | mTLS certificates, ED25519 agent signatures, TOTP MFA |
| 01.n | Network Connection Control | Firewall executor enforces iptables, nftables, pf, Windows Firewall rules |
| 01.v | Information Access Restriction | File executor enforces permissions, ownership, and mode on all managed files |

### Domain 06 — Compliance

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 06.c | Protection of Organizational Records | AES-256-GCM encrypted backups, SHA-256 integrity verification, file snapshots |
| 06.g | Compliance with Security Policies | Continuous drift detection with 5-level compliance status, fleet dashboards |
| 06.h | Technical Compliance Checking | Automated convergence on every check-in, recurring drift detection (3-run threshold) |
| 06.i | Information Systems Audit Controls | SHA-256 hash chain tamper-evident audit trail |

### Domain 07 — Asset Management

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 07.a | Inventory of Assets | 25 trait collectors auto-discover hardware, OS, network, packages; FleetIndex real-time inventory |

### Domain 09 — Communications and Operations Management

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 09.a | Documented Operating Procedures | Config-as-code: YAML modules define desired state, version-controlled |
| 09.b | Change Management | Stage → publish workflow, git history, modlint validation, audit trail |
| 09.c | Segregation of Duties | RBAC with admin/viewer + fine-grained permissions, change approval (two-person rule) |

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
| 09.d | Separation of Environments | environment_overrides, conditional variables per environment |
| 09.i | Protection Against Malicious Code | Package executor enforces AV/EDR, service executor ensures running |
| 09.k | Back-up | Litestream continuous WAL replication, on-demand snapshots, AES-256-GCM encryption |
| 09.l | Network Security Management | Firewall executor + mTLS on all management traffic |
| 09.q | Audit Logging | SHA-256 hash chain: auth, user, token, config, task, workflow, emergency, remediation events |
| 09.s | Protection of Log Information | Audit chain is tamper-evident, immutable once written |
| 09.v | Clock Synchronization | Package/service executors enforce NTP/chrony across fleet |

### Domain 10 — Systems Acquisition, Development, Maintenance

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 10.f | Cryptographic Controls | AES-256-GCM secrets, mTLS (TLS 1.3), ED25519 signatures, Argon2id KDF |
| 10.g | Key Management | Auto-generated master key, secret rotation via watcher, certificate renewal |
| 10.h | Control of Operational Software | Package executor enforces versions, repository executor controls sources |
| 10.k | Change Control Procedures | Stage/live separation, modlint, change approval, blast radius limits, auto-rollback |
| 10.l | Technical Review After OS Changes | Automatic reconvergence detects drift after OS updates |
| 10.m | Restrictions on Software Changes | Package pinning, repository enforcement prevents unauthorized changes |

### Domain 11 — Incident Management

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 11.a | Reporting Security Events | Webhook + SMTP notifications for compliance drift, threshold breaches |
| 11.e | Collection of Evidence | Run history, file snapshots, compliance history, tamper-evident audit chain |

### Domain 12 — Business Continuity

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 12.c | Developing BCPs | Config-as-code enables rapid rebuild; backup + restore; disaster recovery documented |

### Domain 16 — Endpoint Protection

| Ref | Control | Vigo Implementation |
|-----|---------|----------------------|
| 16.a | Endpoint Protection | Package/service executors enforce AV/EDR/firewall on all managed endpoints |

## Quick Start

```bash
cp envoy-configs/modules/compliance/hitrust/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/hitrust-*.vgo.example; do mv "$f" "${f%.example}"; done
cp envoy-configs/compliance-roles.vgo.example /srv/vigo/stack/stage/compliance-roles.vgo
```

Assign the `hitrust` role to nodes:

```yaml
envoys:
  - match: "*.example.com"
    roles: [hitrust]
```

Then publish and verify: `vigogo config publish && vigogo report hitrust`

## Generating HITRUST Reports

### CLI


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
```bash
# JSON report
vigogo report hitrust

# HTML report (printable, Vigo-branded)
vigogo report hitrust --format html --output hitrust-report.html
```

### REST API

```bash
# JSON
curl -s https://vigo:8443/api/v1/report/hitrust | jq .

# HTML
curl -s https://vigo:8443/api/v1/report/hitrust.html > hitrust-report.html
```

## Cross-Reference to Other Frameworks

HITRUST CSF harmonizes multiple standards. The controls Vigo satisfies map across:

| HITRUST Ref | HIPAA | NIST 800-53 | ISO 27001 | PCI DSS |
|-------------|-------|-------------|-----------|---------|
| 01.b | 164.312(a)(2)(i) | IA-2 | A.9.2.1 | 8.1 |
| 01.c | 164.312(a)(1) | AC-6 | A.9.2.3 | 7.1 |
| 01.d | 164.308(a)(5)(ii)(D) | IA-5 | A.9.4.3 | 8.2 |
| 06.g | 164.312(b) | CA-7 | A.18.2.2 | 11.5 |
| 06.h | 164.312(b) | CA-7 | A.18.2.3 | 11.5 |
| 09.b | 164.312(e)(2)(i) | CM-3 | A.12.1.2 | 6.4 |
| 09.q | 164.312(b) | AU-2 | A.12.4.1 | 10.2 |
| 10.f | 164.312(a)(2)(iv) | SC-12 | A.10.1.1 | 3.4 |
| 10.k | 164.312(e)(2)(i) | CM-3 | A.14.2.2 | 6.4 |
| 11.a | 164.308(a)(6) | IR-6 | A.16.1.2 | 12.10 |

## Domains Not Covered (Organizational)

These HITRUST CSF domains require organizational policies and human processes:

| Domain | Name | Why Not Automated |
|--------|------|-------------------|
| 00 | Information Security Management Program | Executive governance |
| 02 | Human Resources Security | Background checks, training |
| 03 | Risk Management | Risk assessment methodology |
| 04 | Security Policy | Policy documentation |
| 05 | Organization of Information Security | Roles and responsibilities |
| 08 | Physical and Environmental Security | Data center access |
| 13 | Privacy Practices | Consent, notice, data subject rights |
| 14 | Cloud Security | Cloud provider contractual controls |
| 15 | Mobile Security | MDM policies |
| 17 | Third Party Assurance | Vendor management |
| 18 | Data Protection and Privacy | Data classification policies |

## HITRUST Assessment Preparation

For a formal HITRUST assessment (r2, i1, or e1), Vigo provides:

1. **Automated evidence** — compliance reports, audit logs, drift detection results
2. **Technical control documentation** — this mapping document + architecture docs
3. **Continuous monitoring** — real-time compliance dashboards, threshold alerts
4. **Change management artifacts** — git history, publish audit trail, approval records

Work with your HITRUST assessor to determine which controls are in scope for your assessment type and map the remaining organizational controls to your policies and procedures.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
