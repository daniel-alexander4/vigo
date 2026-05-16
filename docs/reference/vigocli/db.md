
# vigocli db

Inspect and maintain the server's SQLite database.

## Subcommands

### stats

Show database file size, WAL size, page count, and per-table row counts.

```bash
vigocli db stats
```

### tables

List all tables in the database.

```bash
vigocli db tables
```

### integrity

Run SQLite integrity checks (`PRAGMA integrity_check` and `PRAGMA foreign_key_check`).

```bash
vigocli db integrity
```

### vacuum

Rewrite the database file to reclaim unused space freed by `db prune` or normal deletes. Streams progress every 5 seconds (`elapsed time | bytes written / estimated total | pct estimate`); the percentage is approximated from the process's write counter against an expected ~2× DB-size budget. On a multi-GB DB this can take many minutes — the streaming heartbeat lets you see it isn't stuck. Prompts for confirmation (use `--force` to skip).

```bash
vigocli db vacuum
vigocli db vacuum --force
```

### prune

Delete history rows older than a chosen age — runs (cascades run_results), push_runs, workflow_runs, file_snapshots, convergence_history, compliance_history. Same query path as the daily background pruner. Use this when you want to reclaim space immediately — typically after lowering `database.retention` from a higher value, or on a DB whose accumulated history predates the current window. Pair with `vigocli db vacuum` to return reclaimed pages to the OS.

```bash
vigocli db prune --older-than 30d
vigocli db prune --older-than 720h --force
```

### analyze

Refresh the query planner's table statistics. Runs `ANALYZE`, which updates `sqlite_stat1` so SQLite's planner picks the best query plan. Worth running once after adding or dropping an index, after a large `db prune`, or whenever `EXPLAIN QUERY PLAN` seems to ignore an index that should be used. Typically seconds even on multi-GB databases.

```bash
vigocli db analyze
```

### checkpoint

Force a WAL checkpoint to flush pending writes to the main database file.

```bash
vigocli db checkpoint
```

### query

Execute a read-only SQL query against the database. Write operations (INSERT, UPDATE, DELETE, DROP) are blocked server-side.

```bash
vigocli db query "SELECT hostname, compliance_status FROM envoys"
vigocli db query "SELECT COUNT(*) FROM runs WHERE status = 'failure'"
```

### reset

Permanently delete ALL data and recreate the schema. Requires typing "yes" to confirm (use `--force` to skip).

```bash
vigocli db reset
vigocli db reset --force
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
