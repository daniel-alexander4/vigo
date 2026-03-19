# vigogo db

Inspect and maintain the server's SQLite database.

## Subcommands

### stats

Show database file size, WAL size, page count, and per-table row counts.

```bash
vigogo db stats
```

### tables

List all tables in the database.

```bash
vigogo db tables
```

### integrity

Run SQLite integrity checks (`PRAGMA integrity_check` and `PRAGMA foreign_key_check`).

```bash
vigogo db integrity
```

### vacuum

Rewrite the database file to reclaim unused space. Prompts for confirmation (use `--force` to skip).

```bash
vigogo db vacuum
vigogo db vacuum --force
```

### checkpoint

Force a WAL checkpoint to flush pending writes to the main database file.

```bash
vigogo db checkpoint
```

### query

Execute a read-only SQL query against the database. Write operations (INSERT, UPDATE, DELETE, DROP) are blocked server-side.

```bash
vigogo db query "SELECT hostname, compliance_status FROM envoys"
vigogo db query "SELECT COUNT(*) FROM runs WHERE status = 'failure'"
```

### reset

Permanently delete ALL data and recreate the schema. Requires typing "yes" to confirm (use `--force` to skip).

```bash
vigogo db reset
vigogo db reset --force
```
