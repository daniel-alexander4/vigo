# Hobnob Documentation

Hobnob is a distributed state enforcement engine for servers and network devices. Agents on managed nodes (thralls) pull desired state from a central server, apply idempotent changes, and report results back. 72 built-in resource types across 8 platforms. No DSL — plain YAML.

## Overview

- [What is Hobnob](overview/overview.md) — Architecture, design principles, how it works
- [Fact Sheet](overview/fact-sheet.md) — Quick reference of capabilities
- [Differentiators](overview/differentiators.md) — What makes Hobnob different
- [Technical Comparison](overview/technical-comparison.md) — Detailed comparison with Puppet, Ansible, Chef, Salt, CFEngine

## Getting Started

- [Installation](getting-started/installation.md) — Docker setup, first boot
- [Server Configuration](getting-started/server-config.md) — Full server.yaml reference
- [Bootstrap](getting-started/bootstrap.md) — Agent enrollment on all platforms
- [Quickstart](getting-started/quickstart.md) — 10-minute first-thrall walkthrough
- [First Module](getting-started/first-module.md) — Write your first .hnb module

## Supported Platforms

- [Linux](platforms/linux.md) — Debian, RHEL, Fedora, SUSE, Arch, Alpine (systemd, apt/dnf/yum)
- [Windows](platforms/windows.md) — 22 Windows executors (IIS, registry, ACL, Defender, WSUS, and more)
- [macOS](platforms/macos.md) — launchd, Homebrew, dscl user management
- [FreeBSD](platforms/freebsd.md) — rc.d, pkg, pf firewall, kernel modules
- [OpenBSD](platforms/openbsd.md) — rcctl, pkg_add, pf
- [NetBSD](platforms/netbsd.md) — rc.d, pkgin
- [illumos](platforms/illumos.md) — SMF, IPS (OmniOS, OpenIndiana, SmartOS)
- [Network Devices](platforms/network-devices.md) — Cisco IOS/NX-OS/IOS-XR, Arista EOS, Juniper Junos via SSH gateway

## Concepts

- [Architecture](concepts/architecture.md) — Three binaries, ports, data flows
- [Config Format](concepts/config-format.md) — Modules, roles, thralls, vars
- [Composition Patterns](concepts/composition-patterns.md) — Five layers of config reuse
- [Resource Language](concepts/resource-language.md) — foreach, case/match, conditional_block, defaults
- [Resource DAG](concepts/resource-dag.md) — depends_on, notify, subscribes
- [When Expressions](concepts/when-expressions.md) — Boolean logic and builtin functions
- [Templates](concepts/templates.md) — Go templates in content: attributes
- [Convergence Walkthrough](concepts/convergence-walkthrough.md) — End-to-end with timing and traffic
- [Check-in Lifecycle](concepts/check-in-lifecycle.md) — Pull loop, fingerprint, delta transfer
- [Compiled Promises](concepts/compiled-promises.md) — Bundle signing, TTL, offline convergence
- [Compliance](concepts/compliance.md) — Per-resource to fleet-wide status
- [Secrets](concepts/secrets.md) — Two backends and the secret: prefix
- [Guardrails](concepts/guardrails.md) — Self-protection for agent and server paths
- [Custom Executors](concepts/custom-executors.md) — JSON stdin/stdout protocol
- [Custom Traits](concepts/custom-traits.md) — Custom trait collector scripts
- [Network Devices](concepts/network-devices.md) — Gateway proxy architecture
- [Spanner](concepts/spanner.md) — Hub-spoke multi-server topology
- [P2P Spanner](concepts/p2p-spanner.md) — Future peer-to-peer architecture (design doc)

## Use Cases & Performance

- [Core Use Cases](use-cases/use-cases.md) — Configuration management, security, network, DR/IR, observability — tiered by interval
- [Performance: 1-Minute](use-cases/performance-1m.md) — Baseline projections (theoretical)
- [Performance: 30-Second](use-cases/performance-30s.md) — Recommended for responsive fleets (theoretical)
- [Performance: 15-Second](use-cases/performance-15s.md) — Near-real-time state enforcement (theoretical)

## AI Assistant

- [Providers](ai/providers.md) — Ollama, Claude, OpenAI
- [Tools](ai/tools.md) — LLM tools reference
- [Context Modes](ai/context-modes.md) — tools, prefetch, auto

## Operations

- [Authentication](operations/authentication.md) — Basic auth (default), OIDC, isowebauth, localhost trust
- [Config Publishing](operations/config-publish.md) — Validate, sync, and reload
- [Change Approval](operations/change-approval.md) — Two-person approval gate for config publish
- [Backup & Recovery](operations/backup.md) — Litestream replication, snapshots, web UI download
- [Disaster Recovery](operations/disaster-recovery.md) — Five failure scenarios with runbooks
- [Email Notifications](operations/smtp.md) — SMTP events and digest
- [Prometheus Metrics](operations/metrics.md) — 36 metrics reference
- [Grafana](operations/grafana.md) — Dashboard setup and queries
- [Database](operations/database.md) — SQLite, PostgreSQL, schema, retention
- [FleetIndex](operations/fleet-index.md) — In-memory index internals
- [Rate Limiting](operations/rate-limiting.md) — Token-bucket rate limiting
- [Maintenance Windows](operations/maintenance-windows.md) — Per-thrall and global change freeze
- [Drift Alerting](operations/drift-alerting.md) — Fleet compliance threshold monitoring
- [Audit Log](operations/audit.md) — Unified audit trail
- [Tamper-Evident Audit](operations/tamper-evident-audit.md) — SHA-256 hash chain for tamper detection
- [Compliance Export](operations/compliance-export.md) — SIEM, CMDB, OSCAL feeds
- [Peer Replication](operations/peer-replication.md) — Primary/peer file sync and automatic failover
- [Tuning](operations/tuning.md) — Runtime tuning parameters
- [Troubleshooting](operations/troubleshooting.md) — Common issues and diagnostics

## Reference

- [Executors](executors/_index.md) — 72 resource types across all platforms
- [Trait Collectors](traits/_index.md) — System fact collectors
- [Agent CLI](cli/agent.md) — hobnob agent commands (daemon, status, traits, run)
- [CLI Reference](cli/_index.md) — hobnobcli admin commands
- [API Reference](api/_index.md) — gRPC and REST APIs

## Web UI

- [Dashboard](ui/dashboard.md) — Fleet overview
- [Thralls](ui/thralls.md) — Managed node list and detail
- [Compliance](ui/compliance.md) — Compliance gauge and breakdown
- [Runs](ui/runs.md) — Convergence run history
- [Tasks](ui/tasks.md) — Ad-hoc task execution
- [Workflows](ui/workflows.md) — Multi-step orchestration
- [Query](ui/query.md) — Live fleet queries
- [Spanner](ui/spanner.md) — Hub-spoke dashboard
- [Security](ui/security.md) — AI chat and compliance

## Examples

- [Nginx Module](examples/nginx-module.md) — Full nginx walkthrough
- [Multi-Environment](examples/multi-env.md) — Production/staging with environment_overrides
- [Docker Stack](examples/docker-stack.md) — Container management
- [Security Hardening](examples/security-hardening.md) — Firewall, fail2ban, SSH, sudo
- [Monitoring Stack](examples/monitoring-stack.md) — Prometheus, node_exporter, Grafana
- [Database Cluster](examples/database-cluster.md) — PostgreSQL with vars_from
- [Kubernetes Nodes](examples/kubernetes-nodes.md) — OS-level k8s node config

## Migration

- [Overview](migration/_index.md) — Comparison table and guide index
- [From Puppet](migration/puppet.md) — Pull-based, catalog, resources, Facter
- [From Ansible](migration/ansible.md) — Push-to-pull shift, handlers to notify
- [From Chef](migration/chef.md) — Ruby DSL to YAML, cookbooks to modules
- [From Salt](migration/salt.md) — Both YAML, Pillar to vars, Grains to Traits
- [From CFEngine](migration/cfengine.md) — Promise theory, classes to when:
