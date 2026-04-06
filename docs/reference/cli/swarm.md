
# vigocli swarm

Distribute files across the fleet via peer-to-peer transfer and monitor progress.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `distribute` | Upload a file and assign it to targeted envoys |
| `status` | Show per-envoy distribution progress for a blob |
| `list` | List all blobs in the blob store |
| `delete` | Remove a blob from the store |

## Usage

```bash
vigocli swarm [subcommand] [flags]
```

## distribute

Upload a file to the server's blob store and assign it to envoys matching a target spec.

```bash
vigocli swarm distribute <file> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `*` | Target spec: hostname glob (`*.web.*`), trait filter (`os.distro=ubuntu`), or `*` for all |
| `--label` | filename | Human-readable label for the blob |

### Examples

```bash
# Distribute to all envoys
vigocli swarm distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers with a custom label
vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Distribute by trait filter
vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'
```

```
Hashing app-v2.tar.gz (47.2 MB)...
Uploading to server...
  SHA256: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
  Size:   47.2 MB (47 chunks)

  Queued for 12 envoy(s) — agents will download on next check-in.
  Track progress: vigocli swarm status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

## status

Show per-envoy distribution progress for a blob. Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli swarm status <sha256-or-prefix>
```

### Example

```bash
# Full hash
vigocli swarm status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd

# Short prefix (must be unique)
vigocli swarm status a1b2c3
```

```
Blob: a1b2c3d4...  (app-v2)

Summary: 8 complete, 2 downloading, 1 pending, 1 skipped

HOSTNAME                 STATUS        CHUNKS     SKIP REASON
web-01.example.com       complete      47/47
web-02.example.com       complete      47/47
web-03.example.com       downloading   31/47
web-04.example.com       pending       0/47
db-01.example.com        skipped       0/47       insufficient disk space
```

## list

List all swarm files — both server-stored blobs and envoy-seeded files — with distribution progress.

```bash
vigocli swarm list
```

### Example

```
NAME                         SHA256           SIZE      SOURCE   RETENTION  PROGRESS
-----------------------------------------------------------------------------------------------
app-v2.tar.gz                a1b2c3d4e5f6     47.2 MB   server   -          12/12
firmware.bin                 b2c3d4e5f678     12.1 MB   seed     7d         8/10 (1 dl)
tar_errors.log               9faa44b9cb01    135.4 KB   server   -          5/5
```

| Column | Description |
|--------|-------------|
| Name | Blob label (original filename) |
| SHA256 | First 12 characters of the content hash |
| Size | Human-readable file size |
| Source | `server` (uploaded via CLI) or `seed` (envoy-originated) |
| Retention | Auto-delete age from `.swarm` manifest, or `-` if none |
| Progress | Complete/total envoys, with downloading count if active |

## delete

Remove a blob from the server's blob store. Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli swarm delete <sha256-or-prefix>
```

### Example

```bash
vigocli swarm delete a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

```
Deleted blob a1b2c3d4...
```

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How peer-to-peer distribution works
- [Swarm Operations](../../operations/swarm.md) — Configuration, monitoring, troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
