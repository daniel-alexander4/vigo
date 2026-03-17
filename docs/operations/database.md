# Database

Hobnob supports two database backends: SQLite (default) and PostgreSQL.

## SQLite (Default)

SQLite is the default and recommended backend for most deployments. It runs in WAL (Write-Ahead Logging) mode for concurrent read access and uses a single file for the entire database.

```yaml
database:
  driver: sqlite
  dsn: /var/lib/hobnob-server/hobnob.db
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
  dsn: "secret:hobnob/db/dsn"
```

The DSN should be a standard PostgreSQL connection string: `postgres://user:password@host:5432/hobnob?sslmode=require`.

## Schema Overview

The database schema is defined in `migrations/000001_initial_schema.up.sql`. Key tables:

### Core Tables

| Table | Description |
|-------|-------------|
| `thralls` | Enrolled managed nodes: hostname, IP, pubkey, enrollment time, last seen, revocation status |
| `approved_patterns` | Enrollment patterns with bcrypt-hashed one-time tokens |
| `runs` | Convergence run records: status (running/success/failure/partial), module/changed/failed counts |
| `run_results` | Per-resource results within a run: action taken, changed flag, error, duration |
| `thrall_traits` | Collected traits (facts) as JSON blobs with timestamps |

### Audit Tables

| Table | Description |
|-------|-------------|
| `plans` | Dry-run plan records |
| `plan_results` | Per-resource planned actions |
| `file_snapshots` | File content snapshots for rollback |
| `package_audit` | Package version tracking per thrall |
| `compliance_history` | Daily fleet compliance snapshots |
| `secret_rotation_events` | Secret rotation audit trail |
| `env_promotions` | Environment variable promotion history |

### Orchestration Tables

| Table | Description |
|-------|-------------|
| `push_runs` | Ad-hoc push dispatch records. Status: pending/running/complete/failed/cancelled |
| `push_run_targets` | Per-target results for push runs. Status: pending/dispatched/complete/failed/timeout/offline/rejected/skipped |
| `workflow_runs` | Workflow execution records. Status: pending/running/complete/failed/aborted |
| `workflow_step_results` | Per-step results within workflows |

### Webhooks

| Table | Description |
|-------|-------------|
| `webhooks` | Registered webhook endpoints with secrets and event filters |

## Retention Pruning

Configure automatic pruning of old run/push/workflow records:

```yaml
database:
  retention: "90d"    # prune records older than 90 days
```

Accepted formats: `90d` (days), `2160h` (hours). When empty, no pruning occurs.

Pruning removes:
- Old runs and their results
- Old push runs and their target results
- Old workflow runs and their step results
- Old compliance history snapshots

Thrall records, approved patterns, and webhooks are never pruned automatically.

## Connection Tuning

For SQLite, the server uses a single connection with WAL mode and appropriate busy timeouts. No connection pool tuning is needed.

For PostgreSQL, the Go `database/sql` package manages connection pooling. The DSN can include standard PostgreSQL connection parameters like `pool_max_conns`.

## Indexes

The schema includes indexes for common query patterns:

- `idx_thralls_hostname` -- hostname lookups during check-in
- `idx_runs_thrall_id` -- listing runs for a specific thrall
- `idx_run_results_run_id` -- fetching results for a specific run
- `idx_thrall_traits_thrall_id_collected` -- latest traits lookup
- `idx_push_runs_status`, `idx_push_runs_created_at` -- push listing/filtering
- `idx_push_run_targets_push_run_id`, `idx_push_run_targets_thrall_id` -- target result lookups
- `idx_workflow_runs_status` -- workflow listing
- `idx_workflow_step_results_workflow` -- step result lookups

With the FleetIndex in place, most real-time queries bypass the database entirely. SQLite is primarily used for durable storage and history queries.
