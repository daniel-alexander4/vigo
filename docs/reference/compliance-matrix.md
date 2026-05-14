
# Compliance matrix

This is the canonical reference for **which Vigo modules and Connwaer trait checks satisfy which controls in which frameworks**. Use it to answer "what do I tag this module with?" and "what's left to satisfy framework X?"

For *how* to tag modules and run reports, see [howto/compliance-reporting.md](../howto/compliance-reporting.md). For per-framework regulatory checklists (HIPAA, SOC 2, PCI-DSS, NY DFS Cyber, decommissioning, incident response), see the appendix on that same page.

## How to read this

Every row is a Vigo capability — a thing one or more modules can do, or a thing Connwaer can verify. Columns are frameworks. A cell holds the control IDs that capability satisfies in that framework. Empty cell means the capability doesn't map to that framework (or hasn't been mapped yet — file an issue).

Frameworks tracked:

- **HIPAA** — Security Rule (45 CFR §164.302–318)
- **SOC 2** — Trust Services Criteria 2017 (Security, Availability, Confidentiality)
- **PCI-DSS** — v4.0
- **NIST 800-53** — Rev. 5
- **ISO 27001** — :2022 Annex A
- **CIS Ubuntu 22.04** — Benchmark v1.0.0
- **HITRUST CSF** — v11.x
- **GDPR** — technical & organizational measures
- **NERC CIP** — v6 (electric power utilities)
- **IEC 62443** — industrial control systems
- **Cyber Essentials Plus** — UK government scheme
- **NY DFS Cyber** — 23 NYCRR 500
- **ABA Cybersecurity** — banking-sector self-assessment

For frameworks not listed (FedRAMP, CMMC, etc.), map them yourself using the underlying capability column — the capabilities are framework-agnostic.

## Capability × framework table

| Capability | HIPAA | SOC 2 | PCI-DSS | NIST 800-53 | ISO 27001 | CIS-Ubuntu | HITRUST | GDPR |
|---|---|---|---|---|---|---|---|---|
| **Identity & access** | | | | | | | | |
| Centralized user management (`user` resource + usercrates) | 164.308(a)(4), 164.312(a)(1) | CC6.1, CC6.2 | 7.1, 8.1 | AC-2, AC-2(1), IA-2 | A.5.16, A.5.17 | 5.4, 5.4.1 | 01.b, 01.q | Art.32(1)(b) |
| Sudo-policy management (`sudoers` resource) | 164.312(a)(2)(ii) | CC6.1 | 7.2.1, 8.2.2 | AC-6, AC-6(1), AC-6(2) | A.8.2 | 5.2.x | 01.c | |
| Password-policy enforcement (`pam_pwquality` module) | 164.308(a)(5)(ii)(D) | CC6.1 | 8.3, 8.3.1 | IA-5, IA-5(1) | A.5.17 | 5.4.x | 01.d | Art.32(1)(b) |
| SSH-pubkey-only auth (`ssh-hardening` module) | 164.312(d) | CC6.6, CC6.7 | 8.3.1, 8.3.4 | IA-2(1), IA-5(2) | A.5.17 | 5.2.x | 01.q | Art.32(1)(b) |
| Session timeout (`/etc/profile.d/timeout` module) | 164.312(a)(2)(iii) | CC6.7 | 8.2.8 | AC-12 | A.8.2 | 5.5.x | 01.h | |
| **Cryptography & confidentiality** | | | | | | | | |
| TLS-only transport (Vigo's mTLS is enforced by construction) | 164.312(e)(1) | CC6.7 | 4.1, 4.2.1 | SC-8, SC-8(1), SC-13 | A.8.20, A.8.24 | | 09.s, 10.f | Art.32(1)(a) |
| Encryption at rest (`luks-disk` / `zfs-encryption` modules) | 164.312(a)(2)(iv) | CC6.1 | 3.4, 3.5 | SC-28, SC-28(1) | A.8.24 | | 10.f | Art.32(1)(a) |
| Database encryption (SQLite encrypted-volume mount; `db-encryption` module) | 164.312(a)(2)(iv) | CC6.7, C1.1 | 3.4 | SC-28, SC-28(1) | A.8.24 | | 06.d, 10.f | Art.32(1)(a) |
| Secrets management (`secret:` prefix; never in DB/config/logs) | 164.312(e)(1) | CC6.1, CC6.7 | 8.6 | IA-5(7), SC-12 | A.8.24 | | 01.q, 09.s | Art.32(1)(a) |
| WORM verification (Connwaer trait: `connwaer.worm`) | 164.312(c)(1) | A1.3 | 9.4.4, 10.5.5 | AU-9, AU-9(2) | A.5.33 | | 09.s, 10.k | Art.32(1)(b) |
| **Audit & integrity** | | | | | | | | |
| Hash-chained audit log (Vigo built-in) | 164.312(b), 164.312(c)(1) | CC4.1, CC7.2 | 10.2, 10.3, 10.5 | AU-2, AU-3, AU-9, AU-10, AU-12 | A.8.15, A.8.16 | 4.x | 09.aa, 09.ab | Art.5(2), Art.32(1)(b) |
| File-integrity monitoring (`aide` module; Connwaer `connwaer.fim`) | 164.312(c)(1) | CC6.7, CC7.1 | 11.5, 11.6 | SI-7, SI-7(1), SI-7(7) | A.8.34 | 6.1 | 10.k | |
| Time synchronization (`chrony` / `ntp` modules) | 164.312(b) | CC4.1 | 10.4, 10.4.1, 10.4.3 | AU-8, AU-8(1) | A.12.4.4 | 2.1.1 | 09.aa | |
| SIEM/log shipping integration (Splunk / Datadog / generic syslog) | 164.312(b) | CC4.1, CC7.2 | 10.5.3, 10.5.4 | AU-6, AU-6(1), AU-6(3) | A.8.15, A.8.16 | 4.2 | 09.aa, 09.ad | Art.32(1)(d) |
| **System hardening** | | | | | | | | |
| OS-baseline hardening (`security-hardening` module set) | 164.308(a)(1)(ii)(B), 164.312(a)(1) | CC6.6, CC6.8 | 1.1.x, 2.1, 2.2 | CM-2, CM-6, CM-7 | A.8.9, A.8.32 | 1.x, 2.x | 09.a, 10.h | Art.32(1)(b) |
| Service-state enforcement (`service` resource, idempotent) | 164.308(a)(1)(ii)(B) | CC6.6 | 1.4, 2.2.1 | CM-7, CM-7(1) | A.8.9 | 2.2.x | 09.j | Art.32(1)(b) |
| Package-state enforcement (`package` resource; CVE-scan integration) | 164.308(a)(5)(ii)(B) | CC6.6, CC7.1 | 6.3, 6.3.3 | SI-2, SI-2(2) | A.8.8 | 1.6.x | 10.h, 10.k | Art.32(1)(b) |
| Patch / dist-upgrade orchestration (`dist-upgrade` module; rolling exec) | 164.308(a)(5)(ii)(B) | CC7.1 | 6.3, 6.3.3 | SI-2, SI-2(2), SI-2(5) | A.8.8 | 1.6.x | 10.h | Art.32(1)(b) |
| Self-protection guardrails (built-in: blocks targeting agent/server/config) | 164.312(c)(1) | CC6.7 | 6.4.1, 6.4.2 | CM-5, CM-5(1) | A.8.32 | | 09.a | Art.32(1)(b) |
| **Network** | | | | | | | | |
| Host firewall (`firewalld` / `nftables` / `ufw` modules) | 164.308(a)(4) | CC6.1, CC6.6 | 1.2.x, 1.4 | SC-7, SC-7(5), SC-7(8) | A.8.20, A.8.22 | 3.5.x | 09.m | |
| Network-segmentation verification (Connwaer trait) | 164.308(a)(4) | CC6.1 | 1.2.5, 1.3 | SC-7 | A.8.22 | | 09.m | |
| Inbound-port-free remote access (Scrier) | 164.312(e)(1) | CC6.6, CC6.7 | 1.2.x, 8.2 | AC-17, AC-17(1), AC-17(2) | A.6.7 | | 01.j | Art.32(1)(a) |
| DNS-resolution policy (`unbound` / `systemd-resolved` modules) | | CC6.1 | 1.4.4 | SC-20, SC-21 | A.8.20 | 3.4.x | | |
| **Operations** | | | | | | | | |
| Backup with WAL replication (Litestream → S3) | 164.308(a)(7)(ii)(A) | A1.2 | 9.4, 12.10.1 | CP-9, CP-9(1), CP-9(5) | A.8.13 | | 09.l, 12.b | Art.32(1)(c) |
| Disaster recovery (server-migration playbook) | 164.308(a)(7) | A1.3 | 12.10 | CP-2, CP-10 | A.5.29, A.5.30 | | 12.b, 12.c | Art.32(1)(c) |
| Continuous monitoring (Prometheus + 5 dashboards) | 164.308(a)(1)(ii)(D) | CC4.1, CC7.2 | 10.7, 11.4 | CA-7, SI-4 | A.8.16 | | 09.ad | |
| Convergence-rollback monitoring (auto-rollback below threshold) | 164.308(a)(5)(ii)(C) | CC7.1 | 6.5.x | CM-3, CM-3(2), SI-2(5) | A.5.30 | | 10.h | Art.32(1)(b) |
| Configuration-change audit (`vigocli config publish` audit chain) | 164.308(a)(5)(ii)(C) | CC4.1, CC8.1 | 6.5.x | CM-3, CM-9 | A.8.32 | | 09.a, 10.h | Art.32(1)(b) |
| Risk scoring (`server/risk/`; 0-100 score per envoy; fleet rollup) | 164.308(a)(1)(ii)(A) | CC3.2, CC3.4 | 12.3.x | RA-3, RA-5 | A.5.7, A.5.36 | | 03.a, 03.b | Art.32(1) |
| CVE-impact search (per-finding affected-fleet count + severity) | 164.308(a)(5)(ii)(B) | CC7.1 | 11.3.1 | RA-5, RA-5(2) | A.8.8 | | 10.h | Art.32(1)(b) |
| **Documented / process controls** | | | | | | | | |
| Documented-scope artifact upload (envelope-encrypted) | 164.308(a)(8) | CC1.x, CC2.x | 12.x | PM-x | A.5.1–5.6 | | 02.x | Art.30 |
| Compliance reporting (PDF/JSON/CSV) | 164.308(a)(8) | CC4.1 | 12.4.x | CA-2, CA-2(1) | A.5.36 | | 06.a | Art.5(2) |
| Waivers (declared in `waivers.vgo`, expiry-tracked) | 164.308(a)(8) | CC3.x | 12.5 | RA-5(5), PM-9 | A.5.36 | | 06.a | |

## Connwaer trait additions

Connwaer (separate product, see [Connwaer docs](https://alexander4.io/connwaer) — formerly bundled in this repo, ejected 2026-05-13) adds **trait-driven escalation**: a control that a Vigo module can't directly enforce gets satisfied if Connwaer's runtime check confirms it. Examples:

- `connwaer.worm` — verifies WORM storage (immutable backups, tamper-evident archives). Satisfies HIPAA 164.312(c)(1), NIST AU-9.
- `connwaer.fim` — runtime file-integrity monitoring beyond `aide`. Satisfies PCI-DSS 11.5, NIST SI-7.
- `connwaer.rng` — validates FIPS-approved RNG sources. Satisfies FIPS 140-3, NIST SP 800-90B.
- `connwaer.hsm-lifecycle` — verifies HSM key-lifecycle handling. Satisfies CCSS-9.1, PCI-DSS 3.5.
- `connwaer.kill-switch` — verifies sub-millisecond trading-system kill switch. Satisfies MiFID II RTS 24.

Connwaer's trait outputs feed Vigo's compliance index via the same `compliance:` mechanism modules use — to an auditor reading the framework report, a control satisfied by a Vigo module and a control satisfied by a Connwaer trait check are indistinguishable.

## What's deliberately not claimed

Vigo's reports state what enforcement and trait collection can prove. The following classes of control are **not** claimed by Vigo and need external evidence:

- **Process controls** — business-associate agreements, vendor due-diligence, third-party risk management, incident-response playbooks (Vigo's `notify` actions support the IRP, they don't replace it), security awareness training, BCP exercises.
- **Physical-security controls** — DC physical access, environmental controls, media destruction. Map to your DC provider's SOC 2 or to your physical-security policy.
- **Organizational controls** — code-of-conduct, conflict-of-interest, segregation of duties at the org level.
- **Sub-millisecond enforcement** — controls requiring sub-millisecond response (financial-services kill switches) need Connwaer or a dedicated runtime; Vigo's check-in interval bottom is ~15 seconds.
- **WORM archival of arbitrary data** — Vigo's audit log is hash-chained and tamper-evident, but it isn't a WORM storage system. Pair with Connwaer's WORM verification or an external WORM store.

Honest reporting is a feature. Auditors prefer "Vigo enforces X, we attest Y separately" over "Vigo claims everything."

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
