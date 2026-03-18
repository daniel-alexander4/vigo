# Backup & Recovery

Hobnob uses [Litestream](https://litestream.io) for continuous SQLite WAL replication. When configured, the server manages a Litestream subprocess that streams WAL changes to a remote replica in near-real-time.

Backup is only applicable when `database.driver` is `sqlite` (the default).

## Configuration

Add a `backup:` section to `server.yaml`:

```yaml
backup:
  url: s3://my-bucket/hobnob/backup
  access_key_id: "secret:hobnob/backup/aws_access_key"
  secret_access_key: "secret:hobnob/backup/aws_secret_key"
  region: us-east-1
  retention: "720h"       # 30 days (default)
  sync_interval: "1s"     # WAL sync frequency (default)
```

## Supported Backends

| Backend | URL Format | Notes |
|---------|-----------|-------|
| Amazon S3 | `s3://bucket/path` | Set `region`, optionally `endpoint` for S3-compatible |
| Google Cloud Storage | `gcs://bucket/path` | Uses Application Default Credentials or explicit keys |
| SFTP | `sftp://host/path` | SSH-based file transfer |
| Local file | `file:///path/to/dir` | Useful for NFS mounts or testing |

### S3-Compatible Storage (MinIO, Backblaze)

For S3-compatible endpoints, set `endpoint` and `force_path_style`:

```yaml
backup:
  url: s3://hobnob-backup/data
  endpoint: https://minio.internal:9000
  force_path_style: true
  access_key_id: "secret:hobnob/backup/access_key"
  secret_access_key: "secret:hobnob/backup/secret_key"
```

## Configuration Options

| Field | Default | Description |
|-------|---------|-------------|
| `url` | (required) | Replica destination URL |
| `access_key_id` | | S3/GCS access key (supports `secret:` prefix) |
| `secret_access_key` | | S3/GCS secret key (supports `secret:` prefix) |
| `region` | provider default | AWS region |
| `endpoint` | | S3-compatible endpoint |
| `force_path_style` | false | Use path-style URLs (required for MinIO) |
| `retention` | `720h` (30 days) | Snapshot retention period |
| `sync_interval` | `1s` | WAL sync frequency |

## How It Works

1. On startup, if `backup.url` is set, the server generates a Litestream configuration file and starts `litestream replicate` as a subprocess.
2. Litestream continuously monitors the SQLite WAL and replicates changes to the configured backend.
3. Periodic snapshots are created and old ones are pruned according to the `retention` setting.
4. On server shutdown, Litestream is stopped gracefully, ensuring all pending WAL data is flushed.

## Prerequisites

The `litestream` binary must be in the server's PATH. Install it from https://litestream.io/install/.

## Restore Procedure

To restore from a backup:

1. Stop the Hobnob server.

2. Remove or rename the existing database file.

3. Run the restore command:
   ```bash
   litestream restore -config /path/to/litestream.yml -o /path/to/hobnob.db /path/to/hobnob.db
   ```

   Or let the Hobnob server handle it -- the `Replicator.Restore()` method performs the same operation programmatically.

4. Start the Hobnob server. The FleetIndex will rehydrate from the restored database.

## Monitoring

Check the Litestream subprocess output in the server logs. Litestream logs replication status, snapshot creation, and any errors to stdout/stderr, which the server captures.

If the Litestream binary is not found in PATH, the server logs a warning but continues running without backup.

---

## Manual Snapshots

For point-in-time backups before risky changes, hardware migrations, or disaster recovery, use the CLI backup commands. These create a single tar.gz archive containing the database, configs, secrets, TLS certs, and server.yaml.

No server API is needed — these commands operate directly on the local filesystem.

### Create a Backup

```bash
hobnobcli backup create /tmp/hobnob-backup.tar.gz
```

The command reads `server.yaml` to find the database path, then:

1. Snapshots the SQLite database using the sqlite3 backup API (safe with concurrent WAL writers)
2. Copies `hnb/proposed/` (config source of truth)
3. Copies `secrets/` (already AES-256-GCM encrypted at rest)
4. Copies `tls/` (CA + server certs)
5. Copies `server.yaml`
6. Writes a `manifest.json` with metadata

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--server-yaml` | `/srv/hobnob/server.yaml` | Path to server.yaml |
| `--server-root` | `/srv/hobnob` | Root directory containing hnb/, secrets/, tls/ |

### Inspect a Backup

```bash
hobnobcli backup inspect /tmp/hobnob-backup.tar.gz
```

Prints the manifest JSON without extracting the full archive.

### Restore from a Backup

```bash
# Stop the server first
systemctl stop hobd

# Preview what will be restored
hobnobcli backup restore /tmp/hobnob-backup.tar.gz --dry-run

# Restore everything
hobnobcli backup restore /tmp/hobnob-backup.tar.gz

# Publish configs and restart
hobnobcli config publish
systemctl start hobd
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `/srv/hobnob` | Root directory to restore into |
| `--db-only` | `false` | Restore only the database, skip config/secrets/tls |
| `--dry-run` | `false` | Show what would be restored without writing |

### Archive Layout

```
hobnob-backup-2026-03-13T14:30:00Z.tar.gz
├── manifest.json
├── db/
│   └── hobnob.db
├── hnb/
│   └── proposed/
│       ├── modules/
│       ├── roles/
│       ├── thralls/
│       └── vars/
├── secrets/
│   └── (encrypted files, directory structure preserved)
├── tls/
│   └── (cert, key, ca, ca_key PEM files)
└── server.yaml
```

### Web UI

The **Admin > Backup** page provides a visual overview of backup configuration and manual controls:

- **Backup status** — shows whether Litestream replication is configured and running.
- **Create Snapshot** — triggers an on-demand backup via `POST /api/v1/backup/snapshot`. Equivalent to `hobnobcli backup create`.
- **Download Backup** — downloads the latest snapshot as a `.tar.gz` archive via `GET /api/v1/backup/download`.

Both actions require admin authentication. Restore remains CLI-only (`hobnobcli backup restore`) because it requires stopping the server first.

### Scheduled Snapshots

Use cron for periodic snapshots. Litestream handles continuous replication; manual snapshots are for explicit recovery points. See [Disaster Recovery](disaster-recovery.md) for full server failure recovery procedures.

```bash
# Daily backup at 2 AM, retain 7 days
0 2 * * * /usr/local/bin/hobnobcli backup create /backup/hobnob-$(date +\%Y\%m\%d).tar.gz && find /backup -name 'hobnob-*.tar.gz' -mtime +7 -delete
```

## Built-in Filesystem Backup

Hobnob ships a `server-backup` module that automatically backs up the entire `/srv/hobnob` directory to `/srv/backups.hobnob/` daily via rsync. This is the recommended baseline for filesystem protection.

### How it works

The server auto-enrolls itself as its own thrall on startup (self-managing server). When the `server-backup` module is assigned to the server's hostname in your thrall mappings, it:

1. Creates `/srv/backups.hobnob/` with mode `0555` (read-only)
2. Installs `/usr/local/bin/hobnob-backup.sh` which:
   - Snapshots the SQLite database using `VACUUM INTO` (consistent, safe with concurrent writers)
   - Unlocks the backup directory (`chmod 755`)
   - Rsyncs `/srv/hobnob/` with `--delete`, excluding the live DB files (the snapshot replaces them)
   - Locks the backup directory back to `0555`
3. Creates a daily cron job (`@daily`) to run the script

### Setup

Add the `server-backup` module to your server's thrall mapping:

```yaml
thralls:
  - match: "my-hobnob-server"
    modules: [server-backup]
```

The server's hostname (from `os.Hostname()`) is used for matching. The module uses sensible defaults but all paths are configurable via vars:

| Variable | Default | Description |
|----------|---------|-------------|
| `backup_dest` | `/srv/backups.hobnob` | Backup destination directory |
| `backup_db_src` | `/srv/hobnob/db/hobnob.db` | SQLite database path |
| `backup_src` | `/srv/hobnob` | Source directory to back up |

### Security

The backup directory is non-writable (`0555`) except during the brief backup window. This prevents accidental or malicious modification of backup data between runs. The backup script runs as root.

## Additional Backup Recommendations

Litestream covers the SQLite database with near-real-time replication. The built-in `server-backup` module covers the full filesystem. For maximum protection:

| Path | Contents | Criticality |
|------|----------|-------------|
| `hnb/proposed/` | Modules, roles, thrall mappings, vars | High — your entire policy |
| `secrets/` | AES-256-GCM encrypted files + `.master.key` | Critical — unrecoverable without master key |
| `tls/` | CA cert/key, server cert/key | High — agents authenticate against these |
| `server.yaml` | Server configuration | Medium — can be recreated |
| `db/hobnob.db` | SQLite database | Covered by Litestream if configured |

1. **Keep `hnb/proposed/` in git.** The config source of truth is plain YAML. Version control gives you history, diffs, and instant recovery regardless of backup freshness.

2. **Use the built-in `server-backup` module** for daily rsync to `/srv/backups.hobnob/`. This covers all critical files with a consistent DB snapshot.

3. **Configure Litestream for near-zero database RPO.** The daily rsync provides a recovery point but Litestream replicates WAL changes within seconds.

4. **Protect the master key separately.** `secrets/.master.key` is the single most critical file. Store a copy in a password manager, HSM, or separate secure location. Without it, all encrypted secrets are unrecoverable.

5. **Test restores periodically.** See [Disaster Recovery](disaster-recovery.md) for the full restore walkthrough.
