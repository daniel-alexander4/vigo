# Hobnob Documentation

Hobnob is a Puppet-style configuration management system. Agents on managed nodes (thralls) pull desired state from a central server, apply idempotent changes, and report results back.

## Getting Started

- [Installation](getting-started/installation.md) — Install the server, CLI, and agent
- [Quickstart](getting-started/quickstart.md) — 10-minute first-thrall walkthrough
- [Bootstrap](getting-started/bootstrap.md) — Agent enrollment flow
- [First Module](getting-started/first-module.md) — Write your first .hnb module
- [Server Configuration](getting-started/server-config.md) — Full server.yaml reference

## Concepts

- [Architecture](concepts/architecture.md) — Three binaries, ports, data flows
- [Config Format](concepts/config-format.md) — Modules, roles, nodes.hnb, vars
- [Resource DAG](concepts/resource-dag.md) — depends_on, notify, subscribes
- [Resource Language](concepts/resource-language.md) — defaults, foreach, case/match, conditional_block
- [When Expressions](concepts/when-expressions.md) — Boolean logic and 13 builtin functions
- [Templates](concepts/templates.md) — Go templates in content: attributes
- [Check-in Lifecycle](concepts/check-in-lifecycle.md) — Pull loop, fingerprint, delta transfer
- [Compiled Promises](concepts/compiled-promises.md) — Bundle signing, TTL, offline convergence
- [Compliance](concepts/compliance.md) — Per-resource to fleet-wide compliance
- [Secrets](concepts/secrets.md) — Three backends and the secret: prefix
- [Federation](concepts/federation.md) — Hub-spoke scaling
- [License](concepts/license.md) — Free tier and enforcement stages
- [Guardrails](concepts/guardrails.md) — Self-protection for agent and server paths
- [Custom Executors](concepts/custom-executors.md) — JSON stdin/stdout protocol
- [Custom Traits](concepts/custom-traits.md) — Custom trait collector scripts

## Reference

- [Executors](executors/_index.md) — All 42 resource executors
- [Trait Collectors](traits/_index.md) — All 38 trait collectors
- [CLI Reference](cli/_index.md) — hobnobcli commands
- [API Reference](api/_index.md) — gRPC and REST APIs

## Operations

- [Backup & Recovery](operations/backup.md) — Litestream WAL replication
- [Email Notifications](operations/smtp.md) — SMTP events and digest
- [Prometheus Metrics](operations/metrics.md) — Metrics reference
- [Database](operations/database.md) — SQLite, Postgres, schema, retention
- [FleetIndex](operations/fleet-index.md) — In-memory index internals
- [Tuning](operations/tuning.md) — Tuning parameters reference
- [Troubleshooting](operations/troubleshooting.md) — Common issues and diagnostics

## Web UI

- [Dashboard](ui/dashboard.md) — Fleet overview
- [Thralls](ui/thralls.md) — Managed node list and detail
- [Compliance](ui/compliance.md) — Compliance gauge and breakdown
- [Runs](ui/runs.md) — Convergence run history
- [Tasks](ui/tasks.md) — Ad-hoc task execution
- [Workflows](ui/workflows.md) — Multi-step orchestration
- [Query](ui/query.md) — Live fleet queries
- [Federation](ui/federation.md) — Hub-spoke dashboard
- [Security](ui/security.md) — AI chat and compliance

## AI Assistant

- [Providers](ai/providers.md) — Ollama, Claude, OpenAI
- [Tools](ai/tools.md) — 14 LLM tools reference
- [Context Modes](ai/context-modes.md) — tools, prefetch, auto

## Examples

- [Nginx Module](examples/nginx-module.md) — Full nginx walkthrough
- [Multi-Environment](examples/multi-env.md) — Production/staging with env_vars
- [Docker Stack](examples/docker-stack.md) — Container management
- [Security Hardening](examples/security-hardening.md) — Firewall, fail2ban, SSH, sudo
- [Monitoring Stack](examples/monitoring-stack.md) — Prometheus, node_exporter, Grafana
- [Database Cluster](examples/database-cluster.md) — PostgreSQL with vars_from
