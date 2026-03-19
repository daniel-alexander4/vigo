# backup

Create and restore full server backups. These commands operate directly on the local filesystem — no server API required. Run them on the server host.

## Subcommands

### create

Create a tar.gz backup archive containing the database, configs, secrets, TLS certs, and server.yaml.

```bash
hobnobcli backup create <path>
```

The SQLite database is snapshotted using `VACUUM INTO`, which is safe with concurrent WAL writers.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--server-yaml` | `/srv/hobnob/server.yaml` | Path to server.yaml (used to find DB settings) |
| `--server-root` | `/srv/hobnob` | Root directory containing policy/, secrets/, tls/ |
| `--encrypt` | `false` | Encrypt with AES-256-GCM (prompts for password) |

**Examples:**

```bash
hobnobcli backup create /tmp/hobnob-backup.tar.gz
hobnobcli backup create /backup/hobnob-$(date +%Y%m%d).tar.gz --server-yaml /opt/hobnob/server.yaml

# Encrypted backup (HIPAA compliant)
hobnobcli backup create /tmp/hobnob-backup.tar.gz --encrypt
```

A SHA-256 checksum sidecar file (`.sha256`) is written alongside the archive for integrity verification.

### restore

Restore server state from a backup archive. Stop the server before restoring.

```bash
hobnobcli backup restore <path>
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `/srv/hobnob` | Root directory to restore into |
| `--db-only` | `false` | Restore only the database, skip config/secrets/tls |
| `--dry-run` | `false` | Show what would be restored without writing |
| `--decrypt` | `false` | Decrypt the archive before restoring (prompts for password) |

**Examples:**

```bash
# Preview restore
hobnobcli backup restore /tmp/backup.tar.gz --dry-run

# Full restore
hobnobcli backup restore /tmp/backup.tar.gz

# Database only
hobnobcli backup restore /tmp/backup.tar.gz --db-only

# Restore to alternate location
hobnobcli backup restore /tmp/backup.tar.gz --target /opt/hobnob-server
```

### inspect

Show the manifest from a backup archive without extracting.

```bash
hobnobcli backup inspect <path>
```

**Example output:**

```json
{
  "version": 1,
  "created_at": "2026-03-13T14:30:00Z",
  "hobnob_version": "dev",
  "db_driver": "sqlite",
  "db_dsn": "/srv/hobnob/db/hobnob.db",
  "files": [
    "db/hobnob.db",
    "policy/stage/modules/nginx.hnb",
    "secrets/db_pass",
    "tls/cert",
    "server.yaml"
  ]
}
```
