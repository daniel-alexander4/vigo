
# vigocli swarm

Distribute files across the fleet via peer-to-peer transfer and monitor progress.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `distribute` | Seed a file for P2P distribution to targeted envoys |
| `status` | Show per-envoy distribution progress |
| `list` | List all swarm seeds and their distribution status |
| `delete` | Remove a seed and stop distribution |
| `broken` | List broken seed manifests across envoys |

## Usage

```bash
vigocli swarm [subcommand] [flags]
```

## distribute

Place a file in the server's seed directory for P2P distribution. The server's agent discovers the seed on the next check-in and auto-distributes to matching envoys.

```bash
vigocli swarm distribute <file> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `*` | Comma-separated target spec: hostname globs, trait filters, `-` prefix to exclude. See examples below |
| `--label` | filename | Human-readable label for the file |
| `--retention` | `7d` | Auto-evict from agents after this duration (e.g. `7d`, `168h`). `0` = keep forever |

### Examples

```bash
# Distribute to all envoys
vigocli swarm distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers with a custom label
vigocli swarm distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Comma-separated patterns with exclusion
vigocli swarm distribute image.iso --target '*lap*,-rasp*'

# Exclusion-only (all envoys except raspberries)
vigocli swarm distribute update.tar.gz --target '-rasp*'

# Distribute by trait filter
vigocli swarm distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'

# Keep forever (no auto-eviction)
vigocli swarm distribute golden-image.tar.gz --retention 0
```

```
Hashing app-v2.tar.gz (47.2 MB)...
Uploading to server...
  SHA256: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
  Size:   47.2 MB

  Seeded — distribution begins on next check-in cycle (12 envoys matched).
  Track progress: vigocli swarm status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

If the target spec matches zero envoys, a warning is printed:

```
  Warning: target "nonexistent*" matched 0 envoys — no distribution will occur.
```

## status

Show per-envoy distribution progress. Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

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

List all swarm seeds with distribution progress.

```bash
vigocli swarm list [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--source` | | Filter by source hostname |
| `--status` | | Filter by distribution status: `active` (pending/downloading) or `complete` |
| `--retired` | `false` | Include retired (expired) seeds |

### Example

```
NAME                         SHA256           SIZE      SOURCE         RETENTION  PROGRESS
-----------------------------------------------------------------------------------------------
app-v2.tar.gz                a1b2c3d4e5f6     47.2 MB   vigoserver     7d         12/12
firmware.bin                 b2c3d4e5f678     12.1 MB   danlap         7d         8/10 (1 dl)
tar_errors.log               9faa44b9cb01    135.4 KB   annlap         7d         5/5
```

| Column | Description |
|--------|-------------|
| Name | Seed label (original filename) |
| SHA256 | First 12 characters of the content hash |
| Size | Human-readable file size |
| Source | Hostname of the seeding envoy |
| Retention | Auto-delete duration, or `-` if keep forever |
| Progress | Complete/total envoys, with downloading count if active |

## delete

Revoke a blob fleet-wide. Retires the seed on the server, records a revocation in the manifest CRDT, and propagates the revocation to all envoys via gossip. Each envoy evicts the blob from its cache upon receiving the revocation.

On a LAN, all copies are removed within seconds via multicast gossip. Cross-subnet envoys receive the revocation on their next check-in or peer connection.

Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli swarm delete <sha256-or-prefix>
```

### Example

```bash
vigocli swarm delete a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

```
Deleted: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

Once revoked, a blob cannot be re-activated by stale trait reports. To re-distribute the same content, create a new seed.

## broken

List seed files with invalid or missing `.swarm` manifests across all envoys. Reports errors detected during the agent's seed trait collection.

```bash
vigocli swarm broken
```

### Example

```
ENVOY                          FILENAME                       ERROR
------------------------------------------------------------------------------------------
danlap                         firmware.bin                   missing .swarm manifest
plex                           backup.tar.gz                  invalid manifest: expected value at line 1
```

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How peer-to-peer distribution works
- [Swarm Operations](../../operations/swarm.md) — Configuration, monitoring, troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
