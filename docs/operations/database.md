
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

Configure automatic pruning of old run/task/workflow records:

```yaml
database:
  retention: "90d"    # prune records older than 90 days
```

Accepted formats: `90d` (days), `2160h` (hours). When empty, no pruning occurs.

Pruning removes:
- Old runs and their results
- Old task runs and their target results
- Old workflow runs and their step results
- Old compliance history snapshots

Envoy records, approved patterns, and webhooks are never pruned automatically.

## Connection Tuning

For SQLite, the server uses a single connection with WAL mode and appropriate busy timeouts. No connection pool tuning is needed.

For PostgreSQL, the Go `database/sql` package manages connection pooling. The DSN can include standard PostgreSQL connection parameters like `pool_max_conns`.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
