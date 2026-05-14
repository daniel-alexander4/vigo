
# Backup & Recovery

Vigo uses [Litestream](https://litestream.io) for continuous SQLite WAL replication. When configured, the server manages a Litestream subprocess that streams WAL changes to a remote replica in near-real-time.

Backup is only applicable when `database.driver` is `sqlite` (the default).

## Configuration

Add a `backup:` section to `server.yaml`:

```yaml
backup:
  url: s3://my-bucket/vigo/backup
  access_key_id: "secret:vigo/backup/aws_access_key"
  secret_access_key: "secret:vigo/backup/aws_secret_key"
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
  url: s3://vigo-backup/data
  endpoint: https://minio.internal:9000
  force_path_style: true
  access_key_id: "secret:vigo/backup/access_key"
  secret_access_key: "secret:vigo/backup/secret_key"
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

1. Stop the Vigo server.

2. Remove or rename the existing database file.

3. Run the restore command:
   ```bash
   litestream restore -config /path/to/litestream.yml -o /path/to/vigo.db /path/to/vigo.db
   ```

   Or let the Vigo server handle it -- the `Replicator.Restore()` method performs the same operation programmatically.

4. Start the Vigo server. The FleetIndex will rehydrate from the restored database.

## Monitoring

Check the Litestream subprocess output in the server logs. Litestream logs replication status, snapshot creation, and any errors to stdout/stderr, which the server captures.

If the Litestream binary is not found in PATH, the server logs a warning but continues running without backup.

---

## Manual Snapshots

For point-in-time backups before risky changes, hardware migrations, or disaster recovery, use the CLI backup commands. These create a single tar.gz archive containing the database, configs, secrets, TLS certs, and server.yaml.

No server API is needed — these commands operate directly on the local filesystem.

### Create a Backup

```bash
vigocli backup create /tmp/vigo-backup.tar.gz
```

The command reads `server.yaml` to find the database path, then:

1. Snapshots the SQLite database using the sqlite3 backup API (safe with concurrent WAL writers)
2. Copies `stockpile/` (config source of truth)
3. Copies `secrets/` (already AES-256-GCM encrypted at rest)
4. Copies `tls/` (CA + server certs)
5. Copies `server.yaml`
6. Writes a `manifest.json` with metadata

Every backup automatically generates a SHA-256 checksum sidecar file (`.sha256`) for integrity verification.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--server-yaml` | `/srv/vigo/server.yaml` | Path to server.yaml |

| `--server-root` | `/srv/vigo` | Root directory containing stockpile/, secrets/, tls/ |
| `--encrypt` | `false` | Encrypt the archive with AES-256-GCM (prompts for password) |

### Encrypted Backups

For HIPAA-compliant environments, encrypt backups at rest:

```bash
# Create an encrypted backup (prompts for password)
vigocli backup create /tmp/vigo-backup.tar.gz --encrypt

# Restore an encrypted backup (prompts for password)
vigocli backup restore /tmp/vigo-backup.tar.gz --decrypt
```

Encryption uses AES-256-GCM with an Argon2id-derived key. The password is never stored — keep it in a secure location (e.g., a password manager or HSM).

### Inspect a Backup

```bash
vigocli backup inspect /tmp/vigo-backup.tar.gz
```

Prints the manifest JSON without extracting the full archive.

### Restore from a Backup

```bash
# Stop the server first
systemctl stop vigosrv

# Preview what will be restored
vigocli backup restore /tmp/vigo-backup.tar.gz --dry-run

# Restore everything
vigocli backup restore /tmp/vigo-backup.tar.gz

# Publish configs and restart
vigocli config publish
systemctl start vigosrv
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `/srv/vigo` | Root directory to restore into |
| `--db-only` | `false` | Restore only the database, skip config/secrets/tls |
| `--dry-run` | `false` | Show what would be restored without writing |

### Archive Layout

```
vigo-backup-2026-03-13T14:30:00Z.tar.gz
├── manifest.json
├── db/
│   └── vigo.db
├── stockpile/
│   ├── modules/
│   ├── roles/
│   ├── envoys/
│   └── vars/
├── secrets/
│   └── (encrypted files, directory structure preserved)
├── tls/
│   └── (cert, key, ca, ca_key PEM files)
└── server.yaml
```

### Discovery and Restore

Snapshots live on disk under `paths.snapshots` (default `/srv/vigo/snapshots`). Listing, download, and restore are all CLI operations via `vigocli backup`. Restore requires stopping the server first.

### Module-Based Snapshots

The recommended approach is the `vigo-server-backup` module, which creates incremental hardlink-based snapshots. Assign it to the node running the Vigo server:

```yaml
- match: "vigoserver.example.com"
  modules: [vigo-server-backup]
  vars:
    backup_retain: "90"
```

The module uses the agent's `backup` executor to create dated snapshot directories with hardlink dedup. Unchanged files share inodes with the previous snapshot, consuming zero additional disk. Snapshots run nightly (configurable) and appear automatically in the web UI.

See `example-configs/stockpile/modules/system/vigo-server-backup.vgo.example` for the full module and all configurable vars.

For one-off backups before risky changes, `vigocli backup create` is still available. See [Disaster Recovery](disaster-recovery.md) for full server failure recovery procedures.

## Built-in Filesystem Backup

Vigo ships a `backups` module that uses the native `backup` executor to create daily hardlink-based incremental snapshots of `/srv/vigo`. Unchanged files share inodes with the previous snapshot, consuming zero additional disk. Only changed or new files are copied.

### How it works

When the server host is enrolled as an envoy and the `backups` module is assigned to its hostname in your envoy mappings, the agent on the server host will:

1. Create dated snapshot directories under `target_path` (format: `YYYY.JJJ.HH.MM`)
2. Hardlink unchanged files from the previous snapshot (zero-copy dedup)
3. Copy only new or modified files
4. Prune snapshots older than `retain` days

The schedule check is built into the executor — if a snapshot already exists for the current day, the backup is skipped.

### Setup

Add the `backups` module to your server's envoy mapping:

```yaml
envoys:
  - match: "my-vigo-server"
    modules: [backups]
```

The `backups` module backs up `/srv/vigo` to `/var/backups/vigo` daily with 14-day retention. Databases are backed up separately using the `db_backup` executor, which dumps via `sqlite3 .dump` so the output is a deterministic SQL file that hardlinks effectively:

```yaml
name: backups
resources:
  - name: srv-vigo-backup
    type: backup
    source_dir: /srv/vigo
    target_path: /var/backups/vigo
    retain: 14
    schedule: daily

  - name: vigo-db-backup
    type: db_backup
    db_type: sqlite
    source_db: /srv/vigo/db/vigo.db
    target_path: /var/backups/vigo/vigo-db
    retain: 14
    schedule: daily

  - name: sandgorgon-db-backup
    type: db_backup
    db_type: sqlite
    source_db: /srv/vigo/sandgorgon/sandgorgon.db
    target_path: /var/backups/vigo/vigo-sandgorgon-db
    retain: 14
    schedule: daily
```

**Why separate db_backup resources?** SQLite database files change on every transaction, defeating hardlink dedup in the `backup` executor. The `db_backup` executor dumps to a compressed SQL file that only changes when data changes, making hardlink snapshots effective.

| Parameter | Default | Description |
|-----------|---------|-------------|
| `source_dir` | — | Directory to back up (required) |
| `target_path` | — | Snapshot destination (required, must be on the same filesystem for hardlinks) |
| `retain` | `30` | Days to keep snapshots before pruning |
| `schedule` | `daily` | Schedule window: `daily`, `hourly`, or `weekly` |
| `exclude` | — | Comma-separated glob patterns to skip |
| `cross_fs` | `fail` | Cross-filesystem behavior: `fail`, `copy`, or `warn` |
| `min_free_space` | `1G` | Minimum free space required before running |
| `pre_command` | — | Shell command to run before backup (e.g., flush logs) |
| `post_command` | — | Shell command to run after backup (e.g., restart service) |

### Security

Hardlink snapshots are space-efficient but share inodes — deleting a file from one snapshot doesn't free disk unless all hardlinks are removed. For tamper resistance, consider mounting the backup filesystem read-only between runs or using a separate volume.

## Additional Backup Recommendations

Litestream covers the SQLite database with near-real-time replication. The built-in `backups` module covers the full filesystem. For maximum protection:

| Path | Contents | Criticality |
|------|----------|-------------|
| `stockpile/` | Modules, roles, envoy mappings, vars | High — your entire policy |
| `secrets/` | AES-256-GCM encrypted files + `.master.key` | Critical — unrecoverable without master key |
| `tls/` | CA cert/key, server cert/key | High — agents authenticate against these |
| `server.yaml` | Server configuration | Medium — can be recreated |
| `db/vigo.db` | SQLite database | Covered by Litestream if configured |

1. **Keep `stockpile/` in git.** The config source of truth is plain YAML. Version control gives you history, diffs, and instant recovery regardless of backup freshness.

2. **Use the `backups` module** for daily hardlink snapshots to `/var/backups/vigo/`. Use `db_backup` resources for databases — raw DB files change every transaction, defeating hardlink dedup. The `db_backup` executor dumps to compressed SQL that hardlinks effectively.

3. **Configure Litestream for near-zero database RPO.** The daily rsync provides a recovery point but Litestream replicates WAL changes within seconds.

4. **Protect the master key separately.** `secrets/.master.key` is the single most critical file. Store a copy in a password manager, HSM, or separate secure location. Without it, all encrypted secrets are unrecoverable.

5. **Test restores periodically.** See [Disaster Recovery](disaster-recovery.md) for the full restore walkthrough.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
