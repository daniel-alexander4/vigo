
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
| `allow-seed` | Manage hostname patterns that control which envoys can seed |

## Usage

```bash
vigocli swarm [subcommand] [flags]
```

## distribute

Place a file in the server's seed directory for P2P distribution. The server registers the seed immediately and gossips the manifest entry to streaming agents.

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
Seeding app-v2.tar.gz (47.2 MB)...
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
Blob: app-v2
  SHA256: a1b2c3d4e5f6...
  Chunks: 47

  8 complete  2 downloading  0 pending  1 skipped  (11 total)

  ✓ web-01.example.com              complete
  ✓ web-02.example.com              complete
  ▶ web-03.example.com              ██████████████░░░░░░ 65%
  ⚠ db-01.example.com               skipped (insufficient disk space)
```

If the blob has been revoked, status shows:

```
  Status: revoked (pending eviction from envoys)
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
| `--revoked` | `false` | Include revoked seeds (operator-revoked or retention-expired) |

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

## revoke

Revoke a blob fleet-wide. Marks the seed as revoked in the server tracker, records a revocation in the manifest CRDT, and propagates the revocation to all envoys via gossip. Each envoy evicts the blob from its cache upon receiving the revocation; the original seeder additionally removes the source file from `seed/` and moves the cached blob to `graveyard/` for a 7-day grace window (a same-SHA reseed within that window resurrects the content without re-downloading).

On a LAN, all copies are removed within seconds via multicast gossip. Cross-subnet envoys receive the revocation on their next check-in or peer connection.

Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli swarm revoke <sha256-or-prefix>
```

### Example

```bash
vigocli swarm revoke a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

```
Revoked: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

Repeat revokes are idempotent:

```
Already revoked: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

To re-distribute the same content after deletion, run `vigocli swarm distribute` again — the fresh manifest entry overrides the revocation.

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

## allow-seed

Manage hostname patterns that control which envoys can seed swarm files. Allowlisted envoys bypass the `envoy_seeding` mode and `min_envoy_age` gate — they can always seed.

### Subcommands

```bash
vigocli swarm allow-seed list                   # show all patterns
vigocli swarm allow-seed add <pattern>          # add a hostname glob
vigocli swarm allow-seed remove <id>            # remove a pattern by ID
```

### Examples

```bash
# Allow all build servers to seed
$ vigocli swarm allow-seed add "build-*.example.com"
  ✓  Added seeder pattern: build-*.example.com

# List current allowlist
$ vigocli swarm allow-seed list
  ID                                    PATTERN                  CREATED                BY
  a1b2c3d4-e5f6-4789-abcd-ef0123456789  build-*.example.com     2026-04-11T20:30:00Z   admin

# Remove a pattern
$ vigocli swarm allow-seed remove a1b2c3d4-e5f6-4789-abcd-ef0123456789
  ✓  Removed seeder pattern a1b2c3d4-e5f6-4789-abcd-ef0123456789
```

### Seed Authorization Flow

When an envoy reports seeds via the `swarm_seeds` trait, the server checks authorization:

1. **Allowlist match** → auto-approve (bypasses mode + age gate)
2. **`envoy_seeding: disabled`** → reject
3. **Enrolled < `min_envoy_age`** → reject
4. **`envoy_seeding: approval`** → pending queue (phase 2)
5. **`envoy_seeding: open`** → auto-approve

Rejected seeds are moved to `seed/admin_rejected/` on the envoy. The operator can inspect them there.

Configure in `server.yaml`:

```yaml
swarm:
  envoy_seeding: disabled    # disabled (default) | approval | open
  min_envoy_age: 30d         # minimum enrollment age to seed (default 30d)
```

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How peer-to-peer distribution works
- [Swarm Operations](../../operations/swarm.md) — Configuration, monitoring, troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
