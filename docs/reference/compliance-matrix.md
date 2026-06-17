
# Compliance matrix

This is the canonical reference for **which Vigo configcrates and Connwaer trait checks satisfy which controls in which frameworks**. Use it to answer "what do I tag this configcrate with?" and "what's left to satisfy framework X?"

For *how* to tag configcrates and run reports, see [howto/compliance-reporting.md](../howto/compliance/compliance-reporting.md). For per-framework regulatory checklists (HIPAA, SOC 2, PCI-DSS, NY DFS Cyber, decommissioning, incident response), see the appendix on that same page.

## How to read this

Every row is a Vigo capability — a thing one or more configcrates can do, or a thing Connwaer can verify. Columns are frameworks. A cell holds the control IDs that capability satisfies in that framework. Empty cell means the capability doesn't map to that framework (or hasn't been mapped yet — file an issue).

Frameworks tracked. The version label is the **currently-mapped baseline** — i.e., the revision Vigo's control IDs are aligned to. The labels below are the authoritative source: they live in `server/compliance/appcontrols.go` and the `/platform/compliance` page renders the same string. If you're auditing against a different revision than what's listed, cross-check the control IDs in your auditor's authoritative copy.

- **HIPAA** — Security Rule (45 CFR §164.302–318)
- **SOC 2** — Trust Services Criteria 2017 (Security, Availability, Confidentiality)
- **PCI DSS** — v4.0
- **NIST 800-53** — Rev. 5
- **ISO/IEC 27001** — :2022 Annex A
- **CIS Ubuntu** — 22.04 Benchmark v1.0.0
- **CIS Windows Server 2022** — v3.0.0 Benchmark (L1 Member Server)
- **CIS RHEL** — 9 Benchmark v2.x
- **HITRUST CSF** — v11.x
- **GDPR** — technical & organizational measures
- **NERC CIP** — v6 (electric power utilities)
- **IEC 62443** — industrial control systems
- **Cyber Essentials Plus** — UK government scheme
- **NY DFS Cyber** — 23 NYCRR 500
- **MiFID II** — RTS 7 (trading-venue organisational requirements) + RTS 25 (clock synchronisation)
- **FINRA** / **SEC 17a-4** — broker-dealer recordkeeping
- **FDA 21 CFR Part 11** — electronic records and signatures
- **SOX** — public-company financial controls
- **UK Gambling Commission** — Remote Technical Standards
- **Nevada Gaming Control Board** — Reg 14 Technical Standards
- **Malta Gaming Authority** — Technical Infrastructure

For frameworks not listed (FedRAMP, CMMC, etc.), map them yourself using the underlying capability column — the capabilities are framework-agnostic. When a framework publishes a new revision, this list updates only after the control mappings are reviewed against the new revision; the label is the version-of-record.

## Capability × framework table

| Capability | HIPAA | SOC 2 | PCI-DSS | NIST 800-53 | ISO 27001 | CIS-Ubuntu | HITRUST | GDPR |
|---|---|---|---|---|---|---|---|---|
| **Identity & access** | | | | | | | | |
| Centralized user management (`user` resource + usercrates) | 164.308(a)(4), 164.312(a)(1) | CC6.1, CC6.2 | 7.1, 8.1 | AC-2, AC-2(1), IA-2 | A.5.16, A.5.17 | 5.4, 5.4.1 | 01.b, 01.q | Art.32(1)(b) |
| Sudo-policy management (`sudoers` resource) | 164.312(a)(2)(ii) | CC6.1 | 7.2.1, 8.2.2 | AC-6, AC-6(1), AC-6(2) | A.8.2 | 5.2.x | 01.c | |
| Password-policy enforcement (`pam_pwquality` configcrate) | 164.308(a)(5)(ii)(D) | CC6.1 | 8.3, 8.3.1 | IA-5, IA-5(1) | A.5.17 | 5.4.x | 01.d | Art.32(1)(b) |
| SSH-pubkey-only auth (`ssh-hardening` configcrate) | 164.312(d) | CC6.6, CC6.7 | 8.3.1, 8.3.4 | IA-2(1), IA-5(2) | A.5.17 | 5.2.x | 01.q | Art.32(1)(b) |
| Session timeout (`/etc/profile.d/timeout` configcrate) | 164.312(a)(2)(iii) | CC6.7 | 8.2.8 | AC-12 | A.8.2 | 5.5.x | 01.h | |
| **Cryptography & confidentiality** | | | | | | | | |
| TLS-only transport (Vigo's mTLS is enforced by construction) | 164.312(e)(1) | CC6.7 | 4.1, 4.2.1 | SC-8, SC-8(1), SC-13 | A.8.20, A.8.24 | | 09.s, 10.f | Art.32(1)(a) |
| Encryption at rest (`luks-disk` / `zfs-encryption` configcrates) | 164.312(a)(2)(iv) | CC6.1 | 3.4, 3.5 | SC-28, SC-28(1) | A.8.24 | | 10.f | Art.32(1)(a) |
| Database encryption (SQLite encrypted-volume mount; `db-encryption` configcrate) | 164.312(a)(2)(iv) | CC6.7, C1.1 | 3.4 | SC-28, SC-28(1) | A.8.24 | | 06.d, 10.f | Art.32(1)(a) |
| Secrets management (`secret:` prefix; never in DB/config/logs) | 164.312(e)(1) | CC6.1, CC6.7 | 8.6 | IA-5(7), SC-12 | A.8.24 | | 01.q, 09.s | Art.32(1)(a) |
| WORM verification (Connwaer trait: `connwaer.worm`) | 164.312(c)(1) | A1.3 | 9.4.4, 10.5.5 | AU-9, AU-9(2) | A.5.33 | | 09.s, 10.k | Art.32(1)(b) |
| **Audit & integrity** | | | | | | | | |
| Hash-chained audit log (Vigo built-in) | 164.312(b), 164.312(c)(1) | CC4.1, CC7.2 | 10.2, 10.3, 10.5 | AU-2, AU-3, AU-9, AU-10, AU-12 | A.8.15, A.8.16 | 4.x | 09.aa, 09.ab | Art.5(2), Art.32(1)(b) |
| File-integrity monitoring (`aide` configcrate; Connwaer `connwaer.fim`) | 164.312(c)(1) | CC6.7, CC7.1 | 11.5, 11.6 | SI-7, SI-7(1), SI-7(7) | A.8.34 | 6.1 | 10.k | |
| Time synchronization (`chrony` / `ntp` configcrates) | 164.312(b) | CC4.1 | 10.4, 10.4.1, 10.4.3 | AU-8, AU-8(1) | A.12.4.4 | 2.1.1 | 09.aa | |
| SIEM/log shipping integration (Splunk / Datadog / generic syslog) | 164.312(b) | CC4.1, CC7.2 | 10.5.3, 10.5.4 | AU-6, AU-6(1), AU-6(3) | A.8.15, A.8.16 | 4.2 | 09.aa, 09.ad | Art.32(1)(d) |
| **System hardening** | | | | | | | | |
| OS-baseline hardening (`security-hardening` configcrate set) | 164.308(a)(1)(ii)(B), 164.312(a)(1) | CC6.6, CC6.8 | 1.1.x, 2.1, 2.2 | CM-2, CM-6, CM-7 | A.8.9, A.8.32 | 1.x, 2.x | 09.a, 10.h | Art.32(1)(b) |
| Service-state enforcement (`service` resource, idempotent) | 164.308(a)(1)(ii)(B) | CC6.6 | 1.4, 2.2.1 | CM-7, CM-7(1) | A.8.9 | 2.2.x | 09.j | Art.32(1)(b) |
| Package-state enforcement (`package` resource; CVE-scan integration) | 164.308(a)(5)(ii)(B) | CC6.6, CC7.1 | 6.3, 6.3.3 | SI-2, SI-2(2) | A.8.8 | 1.6.x | 10.h, 10.k | Art.32(1)(b) |
| Patch / dist-upgrade orchestration (`dist-upgrade` configcrate; rolling exec) | 164.308(a)(5)(ii)(B) | CC7.1 | 6.3, 6.3.3 | SI-2, SI-2(2), SI-2(5) | A.8.8 | 1.6.x | 10.h | Art.32(1)(b) |
| Self-protection guardrails (built-in: blocks targeting agent/server/config) | 164.312(c)(1) | CC6.7 | 6.4.1, 6.4.2 | CM-5, CM-5(1) | A.8.32 | | 09.a | Art.32(1)(b) |
| **Network** | | | | | | | | |
| Host firewall (`firewalld` / `nftables` / `ufw` configcrates) | 164.308(a)(4) | CC6.1, CC6.6 | 1.2.x, 1.4 | SC-7, SC-7(5), SC-7(8) | A.8.20, A.8.22 | 3.5.x | 09.m | |
| Network-segmentation verification (Connwaer trait) | 164.308(a)(4) | CC6.1 | 1.2.5, 1.3 | SC-7 | A.8.22 | | 09.m | |
| Inbound-port-free remote access (Scrier) | 164.312(e)(1) | CC6.6, CC6.7 | 1.2.x, 8.2 | AC-17, AC-17(1), AC-17(2) | A.6.7 | | 01.j | Art.32(1)(a) |
| DNS-resolution policy (`unbound` / `systemd-resolved` configcrates) | | CC6.1 | 1.4.4 | SC-20, SC-21 | A.8.20 | 3.4.x | | |
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

Connwaer is a separate Alexander4 compliance-verification agent. Its docs are currently archived ahead of a dedicated location; once Connwaer ships independently, the canonical reference will live there. Connwaer adds **trait-driven escalation**: a control that a Vigo configcrate can't directly enforce gets satisfied if Connwaer's runtime check confirms it. Examples:

- `connwaer.app-allowlisting` — verifies application-allowlist enforcement (only approved binaries can execute). Satisfies NIST CM-7(5), CIS 2.5.x, HIPAA 164.312(a)(2)(ii).
- `connwaer.backup-restore` — verifies the *restore* half of backup discipline by exercising it on a schedule (a backup that can't be restored isn't a backup). Satisfies NIST CP-9(1) / CP-10, ISO A.8.13, HIPAA 164.308(a)(7)(ii)(B).
- `connwaer.bcp-test` — verifies business-continuity-plan exercises happened on schedule with documented outcomes. Satisfies NIST CP-4, ISO A.5.30, HITRUST 12.b.
- `connwaer.capacity-monitor` — verifies capacity headroom against declared thresholds (disk, memory, file descriptors). Satisfies SOC 2 A1.1, NIST CP-2(2), ISO A.8.6.
- `connwaer.encryption-at-rest` — verifies encryption-at-rest is actually engaged (LUKS, ZFS-native, dm-crypt, BitLocker) and the keys are managed per policy. Satisfies HIPAA 164.312(a)(2)(iv), PCI-DSS 3.4 / 3.5, NIST SC-28 / SC-28(1).
- `connwaer.fim` — runtime file-integrity monitoring beyond `aide`. Satisfies PCI-DSS 11.5, NIST SI-7 / SI-7(1) / SI-7(7).
- `connwaer.hsm-lifecycle` — verifies HSM key-lifecycle handling (generation, rotation, retirement, destruction). Satisfies CCSS-9.1, PCI-DSS 3.5 / 3.6, NIST SC-12 / SC-12(2).
- `connwaer.integrity-monitoring` — broader integrity surface than `connwaer.fim`: kernel modules, boot chain, service unit files. Satisfies NIST SI-7, ISO A.8.34.
- `connwaer.kill-switch` — verifies sub-millisecond trading-system kill switch (financial-services). Satisfies MiFID II RTS 7 Art 14, SEC Rule 15c3-5.
- `connwaer.log-review` — verifies logs are reviewed on a defined cadence with documented outcomes. Satisfies PCI-DSS 10.4, NIST AU-6 / AU-6(1), ISO A.8.15 / A.8.16.
- `connwaer.network-segmentation` — verifies segmentation between fleet partitions (CDE vs non-CDE, prod vs dev, tenant boundaries). Satisfies PCI-DSS 1.2 / 1.3, NIST SC-7, ISO A.8.22.
- `connwaer.password-policy` — verifies password-complexity, history, and lockout policy enforcement. Satisfies NIST IA-5 / IA-5(1), CIS 5.4.x, HIPAA 164.308(a)(5)(ii)(D).
- `connwaer.privilege-escalation` — verifies sudo / SeDebug / setuid policy and auditing. Satisfies NIST AC-6 / AC-6(1) / AC-6(2), PCI-DSS 7.2.1, CIS 5.2.x.
- `connwaer.rng-validation` — validates FIPS-approved RNG sources. Satisfies FIPS 140-3, NIST SP 800-90B.
- `connwaer.sandboxing` — verifies process sandboxing (systemd hardening directives, AppArmor / SELinux profiles in enforce mode, Windows sandbox). Satisfies NIST SC-39, CIS 1.x.
- `connwaer.session-timeout` — verifies idle-session and absolute-session timeouts on interactive shells. Satisfies HIPAA 164.312(a)(2)(iii), PCI-DSS 8.2.8, NIST AC-12.
- `connwaer.snmp-collector` — collects SNMP traits from network devices and tagged hosts for compliance evidence. Satisfies operational visibility requirements under most frameworks.
- `connwaer.time-sync` — verifies NTP/chrony health, peer reachability, drift bounds. Satisfies PCI-DSS 10.4, NIST AU-8 / AU-8(1), ISO A.12.4.4.
- `connwaer.worm-verify` — verifies WORM storage (immutable backups, tamper-evident archives) is actually immutable. Satisfies HIPAA 164.312(c)(1), NIST AU-9 / AU-9(2), FINRA SEC-17a-4.

Connwaer's trait outputs feed Vigo's compliance index via the same `compliance:` mechanism configcrates use — to an auditor reading the framework report, a control satisfied by a Vigo configcrate and a control satisfied by a Connwaer trait check are indistinguishable.

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
