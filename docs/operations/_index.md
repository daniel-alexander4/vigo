# Operations Guide

This section covers the operational aspects of running a Hobnob server in production.

## Topics

- [Backup & Recovery](backup.md) -- Continuous SQLite WAL replication via Litestream
- [Disaster Recovery](disaster-recovery.md) -- Full DR playbook for server failure scenarios
- [Email Notifications](smtp.md) -- SMTP-based alerting for server events
- [Prometheus Metrics](metrics.md) -- Monitoring and observability
- [Grafana Dashboards](grafana.md) -- Pre-built dashboards and alert rules
- [Database](database.md) -- SQLite and Postgres configuration, schema, and retention
- [FleetIndex](fleet-index.md) -- In-memory index for zero-DB check-in performance
- [Tuning Parameters](tuning.md) -- All configurable tuning knobs
- [Config Publish Pipeline](config-publish.md) -- Staging validation before live config sync
- [Rate Limiting](rate-limiting.md) -- Token-bucket rate limiting on check-in RPCs
- [Maintenance Windows](maintenance-windows.md) -- Per-thrall and global change freeze
- [Drift Alerting](drift-alerting.md) -- Fleet compliance threshold monitoring
- [Audit Log](audit.md) -- Unified audit trail for config, tasks, and workflows
- [Troubleshooting](troubleshooting.md) -- Common issues and diagnostics
- [Performance: 1-Minute Check-ins](performance-analysis.md) -- Scaling projections on 4 vCPU / 8 GB
- [Performance: 30-Second Check-ins](performance-30s.md) -- Recommended for responsive fleets
- [Performance: 15-Second Check-ins](performance-15s.md) -- Near-real-time state enforcement
