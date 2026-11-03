
# backup

Create and restore full server backups. These commands operate directly on the local filesystem — no server API required. Run them on the server host.

## Subcommands

### create

Create a tar.gz backup archive containing the database, configs, secrets, TLS certs, and server.yaml.

```bash
vigocli backup create <path>
```

The SQLite database is snapshotted using `VACUUM INTO`, which is safe with concurrent WAL writers.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--server-yaml` | `/srv/vigo/server.yaml` | Path to server.yaml (used to find DB settings) |
| `--server-root` | `/srv/vigo` | Root directory containing stacks/, secrets/, tls/ |
| `--encrypt` | `false` | Encrypt with AES-256-GCM (prompts for password) |

**Examples:**

```bash
vigocli backup create /tmp/vigo-backup.tar.gz
vigocli backup create /backup/vigo-$(date +%Y%m%d).tar.gz --server-yaml /opt/vigo/server.yaml

# Encrypted backup (HIPAA compliant)
vigocli backup create /tmp/vigo-backup.tar.gz --encrypt
```

A SHA-256 checksum sidecar file (`.sha256`) is written alongside the archive for integrity verification.

### restore

Restore server state from a backup archive. Stop the server before restoring.

```bash
vigocli backup restore <path>
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `/srv/vigo` | Root directory to restore into |
| `--db-only` | `false` | Restore only the database, skip config/secrets/tls |
| `--dry-run` | `false` | Show what would be restored without writing |
| `--decrypt` | `false` | Decrypt the archive before restoring (prompts for password) |

**Examples:**

```bash
# Preview restore
vigocli backup restore /tmp/backup.tar.gz --dry-run
# Full restore
vigocli backup restore /tmp/backup.tar.gz

# Database only
vigocli backup restore /tmp/backup.tar.gz --db-only

# Restore to alternate location
vigocli backup restore /tmp/backup.tar.gz --target /opt/vigo-server
```

### inspect

Show the manifest from a backup archive without extracting.

```bash
vigocli backup inspect <path>
```

**Example output:**

```json
{
  "version": 1,
  "created_at": "2026-03-13T14:30:00Z",
  "vigo_version": "dev",
  "db_driver": "sqlite",
  "db_dsn": "/srv/vigo/db/vigo.db",
  "files": [
    "db/vigo.db",
    "stacks/configcrates/nginx.vgo",
    "secrets/db_pass",
    "tls/cert",
    "server.yaml"
  ]
}
```
