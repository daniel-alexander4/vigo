
# Operations Guide

This section covers the operational aspects of running a Vigo server in production.

## Topics

- [Backup & Recovery](backup.md) -- Continuous SQLite WAL replication via Litestream
- [Disaster Recovery](disaster-recovery.md) -- Full DR playbook for server failure scenarios
- [Server Migration](server-migration.md) -- Moving the server to a new machine without re-enrolling agents
- [Email Notifications](smtp.md) -- SMTP-based alerting for server events
- [Prometheus Metrics](metrics.md) -- Monitoring and observability
- [Grafana Dashboards](grafana.md) -- Pre-built dashboards and alert rules
- [Database](database.md) -- SQLite and Postgres configuration, schema, and retention
- [FleetIndex](fleet-index.md) -- In-memory index for zero-DB check-in performance
- [Tuning Parameters](tuning.md) -- All configurable tuning knobs
- [Config Publish Pipeline](config-publish.md) -- Staging validation before live config sync
- [Change Approval](change-approval.md) -- Two-person rule for config changes
- [Blast Radius Limits](blast-radius.md) -- Auto-rollback on compliance drop after config publish
- [Rate Limiting](rate-limiting.md) -- Token-bucket rate limiting on check-in RPCs
- [Maintenance Windows](maintenance-windows.md) -- Per-envoy and global change freeze
- [Drift Alerting](drift-alerting.md) -- Fleet compliance threshold monitoring
- [Audit Log](audit.md) -- Unified audit trail for config, tasks, and workflows
- [Peer Replication](peer-replication.md) -- Primary/peer file sync and automatic failover
- [Regression Tests](regression-tests.md) -- Automated tests for doc drift, missing docs, ignored errors
- [Testing](testing.md) -- CLI integration tests and unit tests for verifying server health
- [Geo-Distributed Deployments](geo-distributed.md) -- Multi-region fleets across US, Europe, and beyond
- [Troubleshooting](troubleshooting.md) -- Common issues and diagnostics

## AI Assistant

- [AI Providers](ai-providers.md) -- Ollama, Claude, OpenAI, OpenAI-compat
- [AI Tools](ai-tools.md) -- All 14 tools available to the assistant
- [AI Context Modes](ai-context-modes.md) -- tools, prefetch, auto
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
