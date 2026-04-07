
# db_backup

Database backup using native dump tools. Runs the appropriate dump command for the database type, stores the output in dated snapshot directories with the same naming scheme as the [backup](backup.md) executor.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `db_type` | Yes | -- | Database type: `postgres`, `mysql`, `mariadb`, `sqlite`, `mongodb`. |
| `database` | Yes | -- | Database to dump — logical name (postgres, mysql, mongodb) or file path (sqlite). |
| `backup_dir` | Yes | -- | Base directory for dated snapshots. |
| `db_user` | No | -- | Database user for authentication. |
| `db_host` | No | `localhost` | Database host. |
| `db_port` | No | *(per db_type)* | Database port. Defaults: postgres=5432, mysql/mariadb=3306, mongodb=27017. |
| `db_dump_command` | No | -- | Override the built-in dump command. Use `{output}` as placeholder for the output file path. |
| `retain` | No | `30` | Days of snapshots to keep. |
| `schedule` | No | `daily` | Dedup window: `daily`, `hourly`, or `weekly`. |

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
    database: mydb
    backup_dir: /var/backups/crdb
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
    database: myapp
    db_user: postgres
    backup_dir: /var/backups/pg
    retain: "30"
```

### MySQL with custom port

```yaml
resources:
  - name: mysql-backup
    type: db_backup
    db_type: mysql
    database: production
    db_user: backup_user
    db_port: "3307"
    backup_dir: /var/backups/mysql
    retain: "14"
```

### SQLite safe copy

```yaml
resources:
  - name: app-db
    type: db_backup
    db_type: sqlite
    database: /opt/app/data.db
    backup_dir: /var/backups/app-db
    retain: "90"
    schedule: hourly
```

### MongoDB with weekly retention

```yaml
resources:
  - name: mongo-backup
    type: db_backup
    db_type: mongodb
    database: analytics
    backup_dir: /var/backups/mongo
    retain: "90"
    schedule: weekly
```

## Platform

Linux, macOS, FreeBSD, Windows. Requires the appropriate dump tool to be installed on the envoy.

## Notes

- Database credentials (passwords) should be handled via `.pgpass`, `.my.cnf`, or environment variables — not in the module YAML. Use Vigo secrets to manage credential files.
- The dump runs as the agent's user (root). For Postgres, use `db_user: postgres` to run as the postgres system user via `pg_dump -U`.
- Snapshot directories use the same `YYYY.JJJ.HH.MM` naming as the backup executor.
- Unlike the backup executor, db_backup does not use hardlinks between snapshots — each dump is a fresh file. The snapshot directory structure provides dated organization and retention.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
