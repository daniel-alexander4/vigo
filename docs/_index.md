
# Vigo Documentation

Vigo is a distributed state enforcement engine for servers and network devices. Agents on managed nodes (envoys) pull desired state from a central server, apply idempotent changes, and report results back. 70 built-in resource types across 8 platforms. No DSL — plain YAML.

## Getting Started

- [What is Vigo](getting-started/overview.md) — Architecture, design principles, how it works
- [Installation](getting-started/installation.md) — Docker setup, first boot
- [Server Configuration](getting-started/server-config.md) — Full server.yaml reference
- [Bootstrap](getting-started/bootstrap.md) — Agent enrollment on all platforms
- [Quickstart](getting-started/quickstart.md) — 10-minute first-envoy walkthrough
- [First Module](getting-started/first-module.md) — Write your first .vgo module

## Engineering

See the full [Engineering Guide](engineering/_index.md). Highlights:

- Architecture: [Overview](engineering/architecture/overview.md), [Check-in Lifecycle](engineering/architecture/check-in-lifecycle.md), [Convergence Walkthrough](engineering/architecture/convergence-walkthrough.md), [Resource DAG](engineering/architecture/resource-dag.md), [Offline Bundles](engineering/architecture/offline-bundles.md)
- Writing modules: [Resource Types](engineering/writing-modules/resource-types.md), [When Expressions](engineering/writing-modules/when-expressions.md), [Lookups](engineering/writing-modules/lookups.md), [Templates](engineering/writing-modules/templates.md), [Multi-Axis Config](engineering/writing-modules/multi-axis-config.md), [Composition Patterns](engineering/writing-modules/composition-patterns.md), [Retraction](engineering/writing-modules/retraction.md), [Stream-Edit](engineering/writing-modules/stream-edit.md), [Config Format](engineering/writing-modules/config-format.md), [Usercrates](engineering/writing-modules/usercrates.md)
- Subsystems: [Swarm](engineering/subsystems/swarm.md), [Spanner](engineering/subsystems/spanner.md), [P2P Spanner](engineering/subsystems/p2p-spanner.md), [Scrier](engineering/subsystems/scrier.md), [Cairn](engineering/subsystems/cairn.md), [Echo](engineering/subsystems/echo.md), [Sigil](engineering/subsystems/sigil.md), [Network Devices](engineering/subsystems/network-devices.md)
- Extending: [Custom Executors](engineering/extending/custom-executors.md), [Custom Traits](engineering/extending/custom-traits.md)

## Operations

- [Authentication](operations/authentication.md) — Basic auth (default), OIDC, isowebauth, localhost trust
- [Licensing](operations/licensing.md) — License tiers, grace periods, enrollment limits
- [Database](operations/database.md) — SQLite configuration, schema, and retention
- [Tuning](operations/tuning.md) — Runtime tuning parameters
- [Integrations](operations/integrations.md) — GRC platforms, webhooks, and external systems
- [Config Publishing](operations/config-publish.md) — Validate, sync, and reload
- [Change Approval](operations/change-approval.md) — Two-person approval gate for config publish
- [Blast Radius Limits](operations/blast-radius.md) — Auto-rollback on compliance drop
- [Maintenance Windows](operations/maintenance-windows.md) — Per-envoy and global change freeze
- [Prometheus Metrics](operations/metrics.md) — 50+ metrics reference
- [Grafana](operations/grafana.md) — Dashboard setup and queries
- [Drift Alerting](operations/drift-alerting.md) — Fleet compliance threshold monitoring
- [Audit Log](operations/audit.md) — Unified audit trail
- [FleetIndex](operations/fleet-index.md) — In-memory index internals
- [Rate Limiting](operations/rate-limiting.md) — Token-bucket rate limiting
- [Peer Replication](operations/peer-replication.md) — Primary/peer file sync and automatic failover
- [Geo-Distributed Deployments](operations/geo-distributed.md) — Multi-region fleets
- [Swarm](operations/swarm.md) — envoy P2P substrate operations (lode, echo, cairn, sigil)
- [Backup & Recovery](operations/backup.md) — Litestream replication, snapshots, web UI download
- [Disaster Recovery](operations/disaster-recovery.md) — Five failure scenarios with runbooks
- [Server Migration](operations/server-migration.md) — Moving server without re-enrolling agents
- [Testing](operations/testing.md) — CLI integration tests
- [Regression Tests](operations/regression-tests.md) — Automated doc drift and error detection
- [Troubleshooting](operations/troubleshooting.md) — Common issues and diagnostics
- [AI Providers](operations/ai-providers.md) — Ollama, Claude, OpenAI
- [AI Tools](operations/ai-tools.md) — LLM tools reference
- [AI Context Modes](operations/ai-context-modes.md) — tools, prefetch, auto

## Security

- [Self-Protection Guardrails](security/self-protection.md) — Blocked commands, protected paths, and enforcement layers

## Compliance

- [Compliance Reporting](compliance/reporting.md) — HIPAA reports in JSON, HTML, and OSCAL
- [HITRUST CSF](compliance/hitrust.md) — 31 control references mapped across 8 domains
- [SOC 2](compliance/soc2.md) — 32 Trust Services Criteria across 5 categories
- [PCI DSS v4.0](compliance/pci-dss.md) — 42 requirements + 4 enforcement modules
- [CIS Benchmarks](compliance/cis-benchmarks.md) — Ubuntu, RHEL, Windows Level 1 modules
- [NIST 800-53](compliance/nist-800-53.md) — 41 Federal/FedRAMP controls
- [ISO 27001](compliance/iso-27001.md) — 33 Annex A controls
- [GDPR](compliance/gdpr.md) — 13 EU data protection articles
- [NERC CIP](compliance/nerc-cip.md) — 18 energy sector requirements
- [Cyber Essentials Plus](compliance/cyber-essentials-plus.md) — UK government certification
- [Database Encryption at Rest](compliance/database-encryption.md) — LUKS/dm-crypt for SQLite, TDE for PostgreSQL
- [HIPAA Compliance Checklist](compliance/hipaa-checklist.md) — 164.312 requirement mapping and verification
- [BAA Template](compliance/baa-template.md) — Business Associate Agreement template
- [Incident Response Plan](compliance/incident-response.md) — Detection, containment, and recovery
- [Emergency Access](compliance/emergency-access.md) — Break-glass procedure
- [Decommissioning](compliance/decommissioning.md) — Secure node and server retirement
- [Tamper-Evident Audit](compliance/tamper-evident-audit.md) — SHA-256 hash chain for tamper detection
- [Compliance Export](compliance/compliance-export.md) — SIEM, CMDB, OSCAL feeds
- [Security Scanning](compliance/security-scanning.md) — CVE scanning, hardening audits, rootkit detection

## Reference

- [Executors](reference/executors/_index.md) — 70 resource types across all platforms
- [Trait Collectors](reference/traits/_index.md) — System fact collectors
- [Agent CLI](reference/cli/agent.md) — vigo agent commands (daemon, status, traits, run)
- [CLI Reference](reference/cli/_index.md) — vigocli admin commands
- [API Reference](reference/api/_index.md) — gRPC and REST APIs
- [Server Configuration](reference/server-yaml.md) — server.yaml reference
- [Supported Platforms](reference/platforms/_index.md) — Platform-specific executors, traits, and bootstrap
- [Web UI](reference/ui/_index.md) — Dashboard, envoys, compliance, runs, pushes, workflows, query

## Legal

- [Documentation License](legal/documentation-license.md) -- Proprietary status, permitted use, trade secret protections

## Connwaer

- [Connwaer Documentation](connwaer/_index.md) -- Active compliance verification agent (18 capabilities, scheduling, alerting, trends)

## Sandgorgon

- [Sandgorgon Documentation](sandgorgon/_index.md) -- Bare metal lifecycle: decommission, NIST 800-88 certificates, BMC/Redfish management

## Architecture Decisions

- [ADR Index](adr/_index.md) — Design decisions and their rationale

## Examples

- [Nginx Module](examples/nginx-module.md) — Full nginx walkthrough
- [Multi-Environment](examples/multi-env.md) — Production/staging with `environments.vgo`
- [Docker Stack](examples/docker-stack.md) — Container management
- [Security Hardening](examples/security-hardening.md) — Firewall, fail2ban, SSH, sudo
- [Monitoring Stack](examples/monitoring-stack.md) — Prometheus, node_exporter, Grafana
- [Database Cluster](examples/database-cluster.md) — PostgreSQL with `common.vgo` + `environments.vgo`
- [Kubernetes Nodes](examples/kubernetes-nodes.md) — OS-level k8s node config
- [Migration Guides](examples/migration/_index.md) — Migrating from Puppet, Ansible, Chef, Salt, CFEngine
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](legal/documentation-license.md).*
