
# db_backup

Database backup using native dump tools. Runs the appropriate dump command for the database type, stores the output in dated snapshot directories with the same naming scheme as the [backup](backup.md) executor.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `db_type` | Yes | -- | Database type: `postgres`, `mysql`, `mariadb`, `sqlite`, `mongodb`. |
| `source_db` | Yes | -- | Database to dump — logical name (postgres, mysql, mongodb) or file path (sqlite). |
| `target_path` | Yes | -- | Base directory for dated snapshots. |
| `db_user` | No | -- | Database user for authentication. |
| `db_host` | No | `localhost` | Database host. |
| `db_port` | No | *(per db_type)* | Database port. Defaults: postgres=5432, mysql/mariadb=3306, mongodb=27017. |
| `db_dump_command` | No | -- | Override the built-in dump command. Use `{output}` as placeholder for the output file path. |
| `min_free_space` | No | `1G` | Refuse to run if the target filesystem has less than this much free space (e.g., `1G`, `500M`). |
| `retain` | No | `30` | Days of snapshots to keep. |
| `schedule` | No | `daily` | Dedup window: `daily`, `hourly`, or `weekly`. |
| `revert` | No | `false` | When `true`, run `on_revert` to undo this resource instead of applying. See [Reversal](#reversal). |
| `on_revert` | No | -- | Shell command run locally to reverse this resource. Required when `revert: true`. |

## Reversal

`db_backup` performs an action with no inferable inverse, so reversal is **operator-declared**: pair the resource with an `on_revert:` command (e.g. to prune the dumps it created).

- `on_revert:` — a shell command, run locally on the agent, that undoes the resource. Inert until triggered.
- `revert: true` runs `on_revert:` once, then reports settled on subsequent runs (idempotent). `revert: true` with no `on_revert:` is rejected at `config publish`. Set `on_revert: none` to declare the action has **no inverse** — it is then omitted from configcrate retraction (`state: absent`) rather than reverted, and `revert: true` + `on_revert: none` is rejected as a contradiction.
- A normal (non-revert) apply clears a spent revert, re-arming it.

Removing the resource from config does **not** undo it — that only stops enforcement. Use `revert: true` with a declared `on_revert:` to actively undo.

## Built-in Dump Commands

| db_type | Tool | Format | Flags |
|---------|------|--------|-------|
| `postgres` | `pg_dump` | Custom format (`.pgfc`) | `-Fc` (compressed, restorable with `pg_restore`) |
| `mysql` | `mysqldump` | SQL (`.sql`) | `--single-transaction` (consistent snapshot) |
| `mariadb` | `mysqldump` | SQL (`.sql`) | `--single-transaction` |
| `sqlite` | `sqlite3` | Binary copy (`.db`) | `.backup` command (safe with concurrent readers) |
| `mongodb` | `mongodump` | Archive (`.archive`) | `--archive` (single-file output) |

The executor verifies the dump tool is installed before running. If not found, it reports which package to install.

## Custom Dump Commands

For databases not in the built-in list, use `db_dump_command` with `{output}` as the output file placeholder:

```yaml
resources:
  - name: cockroach-backup
    type: db_backup
    db_type: custom
    source_db: mydb
    target_path: /var/backups/crdb
    db_dump_command: "cockroach dump mydb --insecure > {output}"
    retain: "14"
```

## Idempotency

Same as the backup executor — skips if a snapshot already exists in the current schedule window. The dump only runs once per window.

## Verification

After the dump completes, the executor verifies:
1. The dump command exited with status 0.
2. The output file exists and is non-empty.

If either check fails, the snapshot directory is removed and an error is reported.

## Examples

### Postgres daily backup

```yaml
resources:
  - name: pg-backup
    type: db_backup
    db_type: postgres
    source_db: myapp
    db_user: postgres
    target_path: /var/backups/pg
    retain: "30"
```

### MySQL with custom port

```yaml
resources:
  - name: mysql-backup
    type: db_backup
    db_type: mysql
    source_db: production
    db_user: backup_user
    db_port: "3307"
    target_path: /var/backups/mysql
    retain: "14"
```

### SQLite safe copy

```yaml
resources:
  - name: app-db
    type: db_backup
    db_type: sqlite
    source_db: /opt/app/data.db
    target_path: /var/backups/app-db
    retain: "90"
    schedule: hourly
```

### MongoDB with weekly retention

```yaml
resources:
  - name: mongo-backup
    type: db_backup
    db_type: mongodb
    source_db: analytics
    target_path: /var/backups/mongo
    retain: "90"
    schedule: weekly
```

## Platform

Linux, macOS, FreeBSD, Windows. Requires the appropriate dump tool to be installed on the envoy.

## Notes

- Database credentials (passwords) should be handled via `.pgpass`, `.my.cnf`, or environment variables — not in the configcrate YAML. Use Vigo secrets to manage credential files.
- The dump runs as the agent's user (root). For Postgres, use `db_user: postgres` to run as the postgres system user via `pg_dump -U`.
- Snapshot directories use the same `YYYY.JJJ.HH.MM` naming as the backup executor.
- Unlike the backup executor, db_backup does not use hardlinks between snapshots — each dump is a fresh file. The snapshot directory structure provides dated organization and retention.
