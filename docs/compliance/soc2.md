
---
title: SOC 2 Compliance
---

# SOC 2 Compliance

SOC 2 (System and Organization Controls 2) evaluates an organization's controls against the AICPA Trust Services Criteria. Vigo maps its technical controls to **32 SOC 2 criteria** across all five Trust Services categories.

## Coverage Summary

| Category | Criteria Mapped | Status |
|----------|----------------|--------|
| CC1 — Control Environment | 3 | Satisfied / Partial |
| CC2 — Communication and Information | 2 | Satisfied |
| CC3 — Risk Assessment | 3 | Satisfied / Partial |
| CC4 — Monitoring Activities | 2 | Satisfied |
| CC5 — Control Activities | 3 | Satisfied |
| CC6 — Logical and Physical Access | 6 | Satisfied |
| CC7 — System Operations | 5 | Satisfied |
| CC8 — Change Management | 1 | Satisfied |
| CC9 — Risk Mitigation | 1 | Satisfied |
| A1 — Availability | 2 | Satisfied / Partial |
| C1 — Confidentiality | 2 | Satisfied |
| PI1 — Processing Integrity | 2 | Satisfied |

## Quick Start

```bash
# Copy SOC 2 modules and pre-built role
cp envoy-configs/modules/compliance/soc2/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/soc2-*.vgo.example; do mv "$f" "${f%.example}"; done
cp envoy-configs/compliance-roles.vgo.example /srv/vigo/stack/stage/compliance-roles.vgo
```

Assign the `soc2` role to nodes in your node mappings:

```yaml
envoys:
  - match: "*.example.com"
    roles: [soc2]
```

Then publish and verify: `vigogo config publish && vigogo report soc2`

## Control Mapping

### CC1 — Control Environment (COSO Principles)

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC1.1 | Integrity and Ethics | Partial | Tamper-evident audit trail enforces accountability; organizational ethics policy required |
| CC1.2 | Board Oversight | Partial | Compliance dashboards provide oversight visibility; board governance is organizational |
| CC1.3 | Management Structure | Satisfied | RBAC with 13 fine-grained permissions, admin/viewer roles, segregation of duties |

### CC2 — Communication and Information

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC2.1 | Internal Communication of Objectives | Satisfied | Config-as-code documents desired state; drift detection identifies deviations |
| CC2.2 | Internal Communication of Policies | Satisfied | YAML modules are the executable policy; version-controlled, auditable |

### CC3 — Risk Assessment

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC3.1 | Risk Identification and Analysis | Satisfied | Continuous drift detection, 5-level compliance status, fleet dashboards |
| CC3.2 | Fraud Risk Assessment | Partial | Tamper-evident audit trail detects unauthorized changes; fraud risk assessment is organizational |
| CC3.3 | Change Risk Assessment | Satisfied | Blast radius limits, compliance threshold monitoring, auto-rollback on regression |

### CC4 — Monitoring Activities

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC4.1 | Ongoing and Separate Evaluations | Satisfied | Continuous convergence on every agent check-in; compliance monitor every 60s |
| CC4.2 | Communication of Deficiencies | Satisfied | Webhook + SMTP alerts on compliance threshold breaches, drift, stale nodes |

### CC5 — Control Activities

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC5.1 | Selection of Control Activities | Satisfied | 73 idempotent resource executors enforce desired state across all platforms |
| CC5.2 | Technology General Controls | Satisfied | mTLS, ED25519 signatures, AES-256-GCM encryption, bcrypt password hashing |
| CC5.3 | Deployment of Control Activities | Satisfied | Stage → publish workflow with modlint validation, change approval, audit trail |

### CC6 — Logical and Physical Access Controls

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC6.1 | Logical Access Security | Satisfied | Authentication required (basic/OIDC/isowebauth), TOTP MFA, 15-min idle timeout |
| CC6.2 | User Registration and Authorization | Satisfied | Per-user accounts with unique UUIDs, role-based defaults, admin approval required |
| CC6.3 | Role-Based Access and Least Privilege | Satisfied | 13 permissions: fleet.read, config.publish, users.manage, etc. |
| CC6.6 | Restriction of Access to System Assets | Satisfied | File executor enforces permissions/ownership; firewall executor enforces network access |
| CC6.7 | Management of Credentials | Satisfied | 12-char complexity, bcrypt hashing, AES-256-GCM storage, API tokens (vgot_ prefix) |
| CC6.8 | Prevention of Unauthorized Software | Satisfied | Package pinning, repository enforcement prevents unauthorized changes |

### CC7 — System Operations

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC7.1 | Detection of Security Events | Satisfied | Continuous drift detection, compliance monitoring, failed login tracking with IP |
| CC7.2 | Monitoring for Anomalies | Satisfied | Recurring drift detection (3-run threshold), compliance threshold alerts |
| CC7.3 | Evaluation of Security Events | Satisfied | Tamper-evident audit trail with event classification; SIEM export |
| CC7.4 | Response to Security Events | Satisfied | Incident response plan, emergency access procedure, remediation workflow |
| CC7.5 | Recovery from Security Events | Satisfied | Encrypted backups, disaster recovery, config-as-code rapid rebuild |

### CC8 — Change Management

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC8.1 | Change Management Process | Satisfied | Stage → live separation, git-tracked, modlint validation, change approval |

### CC9 — Risk Mitigation

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| CC9.1 | Risk Mitigation Activities | Satisfied | Automated remediation, blast radius limits, auto-rollback on compliance drop |

### A1 — Availability

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| A1.1 | Availability Commitments | Satisfied | Litestream continuous backup, disaster recovery docs, peer replication for HA |
| A1.2 | Environmental Protections | Partial | Agent offline convergence (cached policy); physical protections are organizational |

### C1 — Confidentiality

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| C1.1 | Identification of Confidential Info | Satisfied | `secret:` prefix identifies sensitive values; never in logs, config, or DB plaintext |
| C1.2 | Disposal of Confidential Info | Satisfied | Documented decommissioning with cascade delete and secure media disposal |

### PI1 — Processing Integrity

| Criterion | Title | Status | Evidence |
|-----------|-------|--------|----------|
| PI1.1 | Data Processing Accuracy | Satisfied | Idempotent executors check before acting; ED25519 signatures verify integrity |
| PI1.2 | Monitoring of Processing | Satisfied | SHA-256 hash chain audit trail tracks all processing events |

## Generating SOC 2 Reports

### CLI

```bash
# JSON report
vigogo report soc2

# HTML report (printable, Vigo-branded)
vigogo report soc2 --format html --output soc2-report.html
```

### REST API

```bash
# JSON
curl -s https://vigo:8443/api/v1/report/soc2 | jq .

# HTML
curl -s https://vigo:8443/api/v1/report/soc2.html > soc2-report.html
```

## SOC 2 Type I vs Type II

- **Type I** — Point-in-time assessment of control design. Vigo reports provide the evidence snapshot.
- **Type II** — Assessment over a period (typically 6-12 months). Schedule weekly reports via cron and provide the archive to your auditor:

```bash
# Weekly SOC 2 evidence snapshot
0 2 * * 1 /usr/local/bin/vigogo report soc2 --format html \
  --output /evidence/soc2-$(date +\%Y\%m\%d).html
```

## Cross-Reference

| SOC 2 | HIPAA | HITRUST | Description |
|-------|-------|---------|-------------|
| CC6.1 | 164.312(a)(1) | 01.b | Access control |
| CC6.3 | 164.312(a)(1) | 01.c | Least privilege |
| CC6.7 | 164.308(a)(5)(ii)(D) | 01.d | Password management |
| CC7.1 | 164.312(b) | 06.g | Security event detection |
| CC7.3 | 164.312(b) | 09.q | Audit logging |
| CC8.1 | 164.312(e)(2)(i) | 09.b | Change management |
| CC5.2 | 164.312(a)(2)(iv) | 10.f | Cryptographic controls |
| CC7.5 | 164.312(a)(2)(ii) | 12.c | Recovery procedures |

## Criteria Not Covered

SOC 2 criteria that require organizational policies (not technical controls):

- **CC1.4-CC1.5** — Commitment to competence, accountability (HR policies)
- **CC2.3** — External communication (customer-facing communication)
- **CC6.4-CC6.5** — Physical access controls (data center)
- **P1** — Privacy criteria (consent, notice, data subject rights)
