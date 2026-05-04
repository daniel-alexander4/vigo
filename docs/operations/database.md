
# Database

Vigo supports two database backends: SQLite (default) and PostgreSQL.

## SQLite (Default)

SQLite is the default and recommended backend for most deployments. It runs in WAL (Write-Ahead Logging) mode for concurrent read access and uses a single file for the entire database.

```yaml
database:
  driver: sqlite
  dsn: /var/lib/vigo-server/vigo.db
```

### WAL Mode

SQLite WAL mode is enabled automatically. This allows multiple concurrent readers while a single writer operates. The FleetIndex handles most read-heavy operations in memory, so SQLite write contention is minimal.

### Continuous Backup

Use Litestream for continuous WAL replication. See [Backup & Recovery](backup.md).

## PostgreSQL

For large-scale deployments or when you need a managed database:

```yaml
database:
  driver: postgres
  dsn: "secret:vigo/db/dsn"
```

The DSN should be a standard PostgreSQL connection string: `postgres://user:password@host:5432/vigo?sslmode=require`.

## Schema Overview

The database schema is defined in `migrations/000001_initial_schema.up.sql`. Tables are organized into three groups:

- **Core tables** — envoys (including `agent_version` for tracking each envoy's reported binary version), enrollment patterns, convergence runs, run results, and collected traits
- **Audit tables** — dry-run plans, file snapshots, package tracking, compliance history, and secret rotation events
- **Orchestration tables** — task runs, workflow runs, and their per-target/per-step results

## Retention Pruning

Vigo accumulates history aggressively — every check-in produces a `runs` row plus one `run_results` row per applied module. Without retention, the SQLite DB grows without bound and queries that order by `started_at` start to dominate the dashboard render budget.

Configure automatic pruning of old run/task/workflow records:

```yaml
database:
  retention: "30d"    # prune records older than 30 days
```

Accepted formats: `30d` (days shorthand) or Go duration strings like `720h`. When empty, no pruning occurs and the server logs `WARN database retention pruning is disabled and DB is large` at startup once the DB exceeds 1 GiB.

When set, vigosrv runs a background pruner every 24 hours that deletes from:

- `runs` (cascades to `run_results` via `ON DELETE CASCADE`)
- `push_runs`
- `workflow_runs`
- `file_snapshots`
- `convergence_history`
- `compliance_history`

Envoy records, approved patterns, webhooks, and `audit_entries` (the integrity-load-bearing hash chain) are never pruned automatically.

### One-shot cleanup of accumulated history

The daily pruner only catches up gradually. For a server that ran without retention and now has weeks/months of accumulated runs, do the cleanup in one pass:

```bash
# 1. Delete history rows older than the new retention window.
vigocli db prune --older-than 30d

# 2. Reclaim freed pages back to the filesystem.
vigocli db vacuum
```

`db prune` reuses the same query path as the periodic pruner. `db vacuum` rewrites the database file; on a multi-GB DB this can take a few minutes. The server stays available throughout — both operations run while vigosrv keeps serving requests.

## Connection Tuning

For SQLite, the server uses a single connection with WAL mode and appropriate busy timeouts. No connection pool tuning is needed.

For PostgreSQL, the Go `database/sql` package manages connection pooling. The DSN can include standard PostgreSQL connection parameters like `pool_max_conns`.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
