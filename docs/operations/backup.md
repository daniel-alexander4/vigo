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
