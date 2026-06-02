# Vigo

**60,000 envoys. A 1-second check-in cadence. One server.**

Vigo is a distributed state enforcement engine: agents on your managed nodes (envoys) pull desired state from a central server over gRPC/mTLS, apply idempotent resource changes, and report results back — at a tight cadence, at fleet scale, on commodity hardware. When one server isn't enough, **spanner** federates write-equal peers and the ceiling goes away.

But scale is only half of it. Vigo is **far more than configuration management** — it bundles the platform an operator usually has to assemble from a half-dozen tools: enforcement *and* compliance scoring *and* GRC evidence *and* reverse remote access *and* a peer-to-peer content fabric *and* an AI assistant *and* HA — under one mTLS-signed control plane. Vigo uniquely pairs elite tight-cadence scale with that full bundle.

---

## What Vigo does

**State enforcement & convergence.** Describe desired state as **configcrates** — reusable resource sets across 74 idempotent resource types (`file`, `package`, `service`, `user`, `exec`, `container`, `vm`, repositories, …). Resources form a `depends_on` DAG with `notify`/`subscribes` triggers; `when:` conditionals and Go-template `content:` adapt per host. The agent checks before it acts, self-heals from external failures, and **converges offline** from its cached bundle when the server is unreachable. Convergence is classified on two orthogonal axes — failure (Converged/Degraded/Failed) × drift (None/Changed/Diverged) — plus reachability.

**Multi-axis configuration.** One YAML language ties together configcrates, roles, hostcrates (hostname-pattern → role/vars, first match wins), per-user **usercrates**, and per-environment overrides, with a clear var-precedence chain. The source of truth is files only: edit the stockpile, `vigocli config publish`, the server validates and reloads.

**Compliance & GRC.** Per-framework coverage (HIPAA, SOC 2, PCI DSS, ISO 27001, and more): a control counts as covered when a configcrate claims it (`provides:` capabilities or `bundle:` framework cuts), a **Connwaer** active check escalates it to satisfied, or a waiver applies. Push per-control pass/fail evidence to Vanta, Drata, Secureframe, ServiceNow GRC, and others on a schedule; export cyber-insurance / audit PDFs. Convergence and compliance are never conflated.

**Reverse remote access (Scrier).** Admin SSH / RDP / VNC to any envoy with **no inbound ports** on the node — sessions reverse out through the agent's existing stream, land in the right OS user's `authorized_keys` transiently, and never expose lateral movement. RDP/VNC ride a `guacd` sidecar in the browser.

**Peer-to-peer swarm.** A shared mTLS substrate carries six content subsystems — **filecast** (admin file push), **gitback** (git over swarm), **longdrawer** (LAN plaintext sync), **lockbox** (encrypted sync), **curator** (artifact registry + Docker/S3 shims), **poolq** (ordered-log queue) — all rooted in the per-user **puddle** identity. Fleet-distributed binaries, container images, and agent self-updates move node-to-node instead of hammering the server.

**Orchestration.** Dispatch ad-hoc tasks and live queries across the fleet; chain them into workflows with conditional branching and abort; roll changes out in health-checked batches.

**AI assistant.** A built-in, **strictly read-only** assistant answers questions about your fleet through ~14 tools (traits, runs, compliance, risk, CVE, docs), streamed, with the docs corpus in-context.

**High availability & DR.** Run peer replicas with one-command failover; or partition the fleet across spanner peers. SQLite with optional Litestream WAL replication to S3 keeps state durable.

**Security by construction.** Every byte is mTLS; every agent request is ed25519-signed against the stored pubkey; one-time enrollment tokens are hashed at rest; RBAC spans admin / viewer / compliance; a SHA-256 hash chain makes the audit log tamper-evident; startup hardening fail-closes on a bad binary signature; secrets resolve from a pluggable backend and never appear in config, logs, DB, wire, or run results.

**Observability & integrations.** Dashboards for convergence, compliance, risk (0–100 scoring), agent health, CVE impact, attack surface, and inventory — every number drilling down to its evidence. Prometheus on `/metrics`; outbound webhooks to Slack, PagerDuty, ServiceNow, ConnectWise, Splunk, Datadog, Loki, Opsgenie, Sentry, NetBox, and more.

**The agent.** A single Rust binary for Linux, macOS, and the BSDs / illumos (Windows supported; new subsystems ship Linux + macOS first). 74 resource types, 29 trait collectors, custom executors over JSON, libvirt VM management, first-class containers, and signed self-update.

---

## Start here

| | |
|---|---|
| **New to Vigo?** | [Tutorials](start/) — stand up the server, enroll an envoy, ship your first configcrate, run your first compliance report |
| **Need to do a thing?** | [How-to guides](howto/) — configcrates, swarm, auth, monitoring, compliance, backup/DR, migrate from another CM |
| **Looking something up?** | [Reference](reference/) — vigocli, agent resources, agent traits, REST API, server.yaml, the configcrate language |
| **Want the mental model?** | [Concepts](concepts/) — architecture, convergence vs compliance, the swarm, puddle identity, the security model |
| **What's a term mean?** | [Glossary](glossary.md) |

---
*Confidential — Alexander4, LLC. Not for redistribution. See [legal/license.md](legal/license.md).*
