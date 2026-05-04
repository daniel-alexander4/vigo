
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

Rewrite the database file to reclaim unused space. Prompts for confirmation (use `--force` to skip).

```bash
vigocli db vacuum
vigocli db vacuum --force
```

### prune

Delete history rows older than a chosen age — runs, push_runs, workflow_runs, file_snapshots, convergence_history, compliance_history. Same query path as the daily background pruner. Use this once after enabling `database.retention` on a DB that has accumulated more history than the new retention window allows. Pair with `vigocli db vacuum` to return reclaimed pages to the OS.

```bash
vigocli db prune --older-than 30d
vigocli db prune --older-than 720h --force
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
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
