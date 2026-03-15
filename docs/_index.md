# Hobnob Documentation

Hobnob is a configuration management system. Agents on managed nodes (thralls) pull desired state from a central server, apply idempotent changes, and report results back.

## Getting Started

- [Installation](getting-started/installation.md) — Docker setup, first boot, directory layout
- [Quickstart](getting-started/quickstart.md) — 10-minute first-thrall walkthrough
- [Bootstrap](getting-started/bootstrap.md) — Agent enrollment flow
- [First Module](getting-started/first-module.md) — Write your first .hnb module
- [Server Configuration](getting-started/server-config.md) — Full server.yaml reference

## Concepts

- [Architecture](concepts/architecture.md) — Three binaries, ports, data flows
- [Config Format](concepts/config-format.md) — Modules, roles, nodes.hnb, vars
- [Resource DAG](concepts/resource-dag.md) — depends_on, notify, subscribes
- [Resource Language](concepts/resource-language.md) — defaults, foreach, case/match, conditional_block
- [When Expressions](concepts/when-expressions.md) — Boolean logic and 15 builtin functions
- [Templates](concepts/templates.md) — Go templates in content: attributes
- [Convergence Walkthrough](concepts/convergence-walkthrough.md) — End-to-end example with timing and traffic flow
- [Check-in Lifecycle](concepts/check-in-lifecycle.md) — Pull loop, fingerprint, delta transfer
- [Compiled Promises](concepts/compiled-promises.md) — Bundle signing, TTL, offline convergence
- [Compliance](concepts/compliance.md) — Per-resource to fleet-wide compliance
- [Secrets](concepts/secrets.md) — Two backends and the secret: prefix
- [Guardrails](concepts/guardrails.md) — Self-protection for agent and server paths
- [Custom Executors](concepts/custom-executors.md) — JSON stdin/stdout protocol
- [Custom Traits](concepts/custom-traits.md) — Custom trait collector scripts

## Reference

- [Executors](executors/_index.md) — All resource executors
- [Trait Collectors](traits/_index.md) — All trait collectors
- [CLI Reference](cli/_index.md) — hobnobcli commands
- [API Reference](api/_index.md) — gRPC and REST APIs

## Operations

- [Config Publishing](operations/config-publish.md) — Validate, sync, and reload
- [Authentication](operations/authentication.md) — Basic, OIDC, isowebauth
- [Backup & Recovery](operations/backup.md) — Litestream WAL replication
- [Email Notifications](operations/smtp.md) — SMTP events and digest
- [Prometheus Metrics](operations/metrics.md) — Metrics reference
- [Database](operations/database.md) — SQLite, Postgres, schema, retention
- [FleetIndex](operations/fleet-index.md) — In-memory index internals
- [Rate Limiting](operations/rate-limiting.md) — Token-bucket rate limiting
- [Maintenance Windows](operations/maintenance-windows.md) — Per-thrall and global change freeze
- [Drift Alerting](operations/drift-alerting.md) — Fleet compliance threshold monitoring
- [Audit Log](operations/audit.md) — Unified audit trail
- [Compliance Export](operations/compliance-export.md) — SIEM, CMDB, OSCAL feeds
- [Tuning](operations/tuning.md) — Tuning parameters reference
- [Troubleshooting](operations/troubleshooting.md) — Common issues and diagnostics

## Scaling

- [Federation](concepts/federation.md) — Hub-spoke scaling
- [License](concepts/license.md) — Free tier, pricing, and enforcement stages

## Web UI

- [Dashboard](ui/dashboard.md) — Fleet overview
- [Thralls](ui/thralls.md) — Managed node list and detail
- [Compliance](ui/compliance.md) — Compliance gauge and breakdown
- [Runs](ui/runs.md) — Convergence run history
- [Tasks](ui/tasks.md) — Ad-hoc task execution
- [Workflows](ui/workflows.md) — Multi-step orchestration
- [Query](ui/query.md) — Live fleet queries
- [Federation](ui/federation.md) — Hub-spoke dashboard
- [License](ui/license.md) — License status and usage
- [Security](ui/security.md) — AI chat and compliance

## AI Assistant

- [Providers](ai/providers.md) — Ollama, Claude, OpenAI
- [Tools](ai/tools.md) — LLM tools reference
- [Context Modes](ai/context-modes.md) — tools, prefetch, auto

## Examples

- [Nginx Module](examples/nginx-module.md) — Full nginx walkthrough
- [Multi-Environment](examples/multi-env.md) — Production/staging with environment_overrides
- [Docker Stack](examples/docker-stack.md) — Container management
- [Security Hardening](examples/security-hardening.md) — Firewall, fail2ban, SSH, sudo
- [Monitoring Stack](examples/monitoring-stack.md) — Prometheus, node_exporter, Grafana
- [Database Cluster](examples/database-cluster.md) — PostgreSQL with vars_from
- [Kubernetes Nodes](examples/kubernetes-nodes.md) — OS-level k8s node config and rolling reboot workflow

## Migration

- [Overview](migration/_index.md) — Comparison table and guide index
- [From Puppet](migration/puppet.md) — Pull-based, catalog, resources, Facter
- [From Ansible](migration/ansible.md) — Push-to-pull shift, handlers to notify
- [From Chef](migration/chef.md) — Ruby DSL to YAML, cookbooks to modules
- [From Salt](migration/salt.md) — Both YAML, Pillar to vars, Grains to Traits
- [From CFEngine](migration/cfengine.md) — Promise theory, classes to when:
