# Regulated Environments Gap Analysis

This document assesses Vigo's readiness for regulated environments — healthcare (HIPAA, FDA), financial services (SOX, PCI-DSS), government (FedRAMP, NIST 800-53), and elections (EAC). It identifies what Vigo already provides, what's missing, and what would be needed to close each gap.

## What Vigo Provides Today

### Strong foundations

| Capability | Status | Detail |
|-----------|--------|--------|
| Encrypted transport | Complete | mTLS on all gRPC traffic, TLS 1.3 minimum, no plaintext mode |
| Request authentication | Complete | ED25519 signature verification on every check-in, per-request |
| Secrets management | Complete | AES-256-GCM encrypted at rest, `secret:` prefix, never plaintext in config/logs/DB/wire |
| Idempotent enforcement | Complete | Check-before-act on all 72 resource types, no side effects on no-op |
| Continuous convergence | Complete | 15-second check-in intervals, sub-30s drift correction |
| Offline resilience | Complete | Signed policy bundles cached in LMDB, local convergence, pending results queue |
| Agent source availability | Complete | ELv2 — readable and auditable by customers (critical for "what runs as root on our machines") |
| Compliance tracking | Complete | Per-resource to fleet-wide compliance status, real-time dashboard |
| Compliance export | Complete | SIEM, CMDB, and OSCAL endpoints (config-gated) |
| Blast radius protection | Complete | Auto-rollback on compliance drop after config publish |
| Binary integrity | Complete | SHA256 checksum verification in bootstrap, ED25519 signing infrastructure |
| Enrollment control | Complete | One-time bcrypt tokens, approved patterns, hostname uniqueness, revocation |
| RBAC | Complete | Admin/viewer roles, session management, API tokens with bcrypt |
| Audit log | Complete | SHA-256 hash chain — each entry includes hash of previous entry; tamper-evident |

### What this covers

These capabilities satisfy the *technical controls* portion of many compliance frameworks. An auditor looking at "is data encrypted in transit?" or "are endpoints authenticated?" would find solid answers. The architecture is sound for managing IT infrastructure (servers, workstations, network equipment) in regulated organizations.

## Gap Analysis by Domain

### Healthcare (HIPAA, FDA 21 CFR Part 820, IEC 62304)

**For IT infrastructure management (workstations, servers, network equipment):**

| Requirement | Vigo Status | Gap |
|-------------|---------------|-----|
| Access controls (HIPAA 164.312(a)) | Met — RBAC, session management, API tokens | None |
| Audit controls (HIPAA 164.312(b)) | Met — SHA-256 hash chain, tamper-evident audit trail | None |
| Integrity controls (HIPAA 164.312(c)) | Met — ED25519 signatures, mTLS, secret encryption | None |
| Transmission security (HIPAA 164.312(e)) | Met — TLS 1.3 minimum, mTLS on all traffic | None |
| Person or entity authentication (HIPAA 164.312(d)) | Met — OIDC, basic auth, isowebauth, API tokens | None |
| Configuration management (NIST 800-53 CM-2, CM-6) | Met — desired state enforcement, drift detection | None |
| Change control | Met — two-person approval workflow with `publish.approval_required` | None |
| Audit trail retention | Partial — hash-chained entries persist in DB, no configurable retention policy | Needs retention policies |

**For medical device management (infusion pumps, monitors, diagnostic instruments):**

| Requirement | Vigo Status | Gap |
|-------------|---------------|-----|
| Software validation (FDA 21 CFR 820.70(i)) | Not met | No formal validation process, no test evidence packages |
| Software lifecycle (IEC 62304) | Not met | No documented SDLC, no hazard analysis, no traceability matrix |
| Risk management (ISO 14971) | Not met | No risk analysis of executor behaviors |
| Change control (21 CFR 820.70(b)) | Not met | No multi-person approval, no design review gates |
| Device history record | Not met | Audit trail doesn't meet DHR requirements |
| Cybersecurity premarket (FDA guidance) | Partial | mTLS and signing are strong, but no SBOM, no vulnerability management process |
| Production track record | Not met | Zero deployments, zero incident history |

**Bottom line:** Vigo can manage hospital IT infrastructure today (same role as Puppet/Ansible in healthcare). Managing medical devices requires FDA software validation and IEC 62304 compliance — years of process work, not just code changes.

### Financial Services (SOX, PCI-DSS)

| Requirement | Vigo Status | Gap |
|-------------|---------------|-----|
| PCI-DSS 1.x: Network security | Met — firewall executors, mTLS, network segmentation enforcement | None |
| PCI-DSS 2.x: Secure configurations | Met — desired state enforcement, drift detection and correction | None |
| PCI-DSS 5.x: Malware protection | Partial — can enforce antivirus config, can't act as antivirus | Not a gap — different tool category |
| PCI-DSS 6.x: Secure development | Not assessed | Would need secure SDLC documentation |
| PCI-DSS 10.x: Logging and monitoring | Met — SHA-256 hash chain, tamper-evident audit trail | None |
| PCI-DSS 10.3: Audit trail content | Met — logs who/what/when with cryptographic hash chain | None |
| PCI-DSS 10.5: Secure audit trails | Met — SHA-256 hash chain detects any modification or deletion | None |
| SOX IT general controls | Met — publish/rollback with two-person approval workflow | None |
| Segregation of duties | Met — approval_required enforces maker/checker (different user must approve) | None |

**Bottom line:** Vigo meets all PCI-DSS and SOX technical control requirements for configuration management. The tamper-evident audit trail satisfies PCI-DSS 10.x, and the two-person approval workflow satisfies SOX segregation of duties.

### Government (FedRAMP, NIST 800-53, FISMA)

| Requirement | Vigo Status | Gap |
|-------------|---------------|-----|
| AC (Access Control) | Met — RBAC, session management, mTLS, OIDC | None |
| AU (Audit and Accountability) | Met — SHA-256 hash chain, tamper-evident audit trail | Retention policy not yet configurable |
| CM (Configuration Management) | Met — baseline enforcement, drift detection, rollback | None for CM-2, CM-6, CM-7 |
| IA (Identification and Authentication) | Met — ED25519, mTLS, OIDC, API tokens | None |
| SC (System and Communications Protection) | Met — TLS 1.3, mTLS, encrypted secrets | None |
| SI (System and Information Integrity) | Partial — drift detection covers SI-7, no FIPS 140-2 validation | Needs FIPS-validated crypto |
| CA (Security Assessment) | Not assessed | Would need third-party assessment |
| SA (System and Services Acquisition) | Not met | No SBOM, no supply chain risk management documentation |

**FIPS 140-2 gap:** The agent uses ring (Rust) for cryptography and Go's stdlib for the server. Neither is FIPS 140-2 validated. FedRAMP Moderate and above requires FIPS-validated cryptographic modules. This would require swapping to FIPS-validated libraries (e.g., Go BoringCrypto, aws-lc-rs for Rust).

**Bottom line:** Vigo supports NIST 800-53 Low baseline for configuration management and audit controls. Moderate and above still need FIPS-validated crypto and SBOM.

### Elections (EAC, VVSG 2.0)

| Requirement | Vigo Status | Gap |
|-------------|---------------|-----|
| Software independence | N/A — Vigo manages infrastructure, not vote counting | Not applicable |
| Configuration management | Met — enforces and monitors desired state | None |
| Audit logging | Met — SHA-256 hash chain, tamper-evident audit trail | None |
| Access control | Met — RBAC, OIDC, API tokens | None |
| Cyber resilience | Strong — offline convergence, mTLS, signed bundles | None |
| Testing and certification | Not met | No EAC certification, no VSTL testing |

**Bottom line:** Vigo could manage election IT infrastructure (workstations, tabulation servers, network equipment) but not voting machines themselves. EAC certification for voting-system components requires VSTL testing — a multi-year, multi-million-dollar process.

## Summary of Gaps

### Buildable (on the pending roadmap)

| Gap | Pending Item | Impact |
|-----|-------------|--------|
| ~~Tamper-evident audit trail~~ | Complete | ~~Unlocks SOX, PCI-DSS 10.x, HIPAA audit controls, NIST AU~~ |
| ~~Change approval workflow~~ | Complete | ~~Unlocks SOX segregation of duties, FDA change control, PCI-DSS change management~~ |
| Agent-side resource restrictions | #10 | Defense against compromised server, required for high-trust environments |
| Agent transparency command | #11 | Auditable by machine owner, supports trust model in shared environments |
| Task dispatch allowlists | #12 | Prevents arbitrary command execution, required for least-privilege |
| Platform code signing | #13 | Required for regulated software distribution (FDA, FedRAMP) |

### Not buildable (process/certification, not code)

| Gap | What's needed | Timeframe |
|-----|--------------|-----------|
| FDA software validation | Documented SDLC, hazard analysis, design history file, test evidence packages | 1-2 years |
| IEC 62304 compliance | Software lifecycle documentation, risk management per ISO 14971 | 1-2 years |
| FIPS 140-2 crypto validation | Swap to FIPS-validated libraries, submit for CMVP certification | 6-18 months |
| FedRAMP authorization | Third-party assessment (3PAO), continuous monitoring, POA&M | 12-18 months |
| EAC certification | VSTL testing, federal certification process | 2-4 years |
| Production track record | Deployments, incident history, customer references | Ongoing |
| SBOM and supply chain docs | CycloneDX/SPDX generation, dependency auditing process | 1-3 months |

## Where Vigo Fits Today

Vigo can manage IT infrastructure in regulated organizations right now — the same role Puppet, Ansible, and Chef fill in hospitals, banks, and government agencies today. None of those tools have FDA validation or FIPS certification either. They're used for servers and workstations, not medical devices or voting machines.

The competitive advantage in regulated environments is:
- **Continuous enforcement** at 15-30 second intervals (vs. Puppet's 30-minute default)
- **Offline resilience** for air-gapped or unreliable networks
- **mTLS + ED25519 on every request** (stronger than most CM tools' default security)
- **Source-available agent** (auditable by compliance teams)
- **Compliance export** (SIEM, CMDB, OSCAL endpoints)

The tamper-evident audit trail and change approval workflow are both complete. For IT infrastructure management, Vigo now meets PCI-DSS 10.x, HIPAA audit controls, NIST AU, SOX IT general controls, and segregation of duties requirements. The remaining buildable gaps are agent-side resource restrictions, task dispatch allowlists, and platform code signing.
