
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

  1 source  8 complete  0 cached  2 downloading  0 pending  1 skipped  (11 total)

  ★ seeder-01.example.com           source
  ✓ web-01.example.com              complete
  ✓ web-02.example.com              complete
  ▶ web-03.example.com              ██████████████░░░░░░ 65%
  ⚠ db-01.example.com               skipped (insufficient disk space)
```

The total count excludes the source — it reflects only target envoys (complete + cached + downloading + pending + skipped).

Per-envoy states:

| Icon | Status | Meaning |
|------|--------|---------|
| `★`  | `source`      | This envoy originated the blob. Has every chunk by construction — never "downloads" its own seed. Distinct from `complete` so operators can tell seeders apart from downloaders at a glance. Not included in the total count. |
| `✓`  | `complete`    | Envoy has downloaded every chunk from its peers. |
| `✓`  | `cached`      | Envoy already had the blob from a previous distribution — no download occurred. Displayed in cyan (CLI) or blue (web UI) to distinguish from a fresh download. |
| `▶`  | `downloading` | Envoy has a partial chunk set and is still fetching. |
| (no icon) | `pending` | Envoy matches the target pattern but hasn't reported any chunks yet. |
| `⚠`  | `skipped`     | Envoy rejected the blob (e.g., authorization, disk, policy). |

If the blob has been revoked, the summary layout changes — source/complete/downloading/pending/skipped counts are replaced with revoking/revoked counts, and each envoy row shows its per-host eviction progress:

```
Blob: app-v2
  SHA256: a1b2c3d4e5f6...
  Chunks: 47
  Status: revoked (pending eviction from envoys)

  3 revoking  8 revoked  (11 total)

  ✗ web-01.example.com              revoked
  ✗ web-02.example.com              revoked
  ⟳ web-03.example.com              revoking
```

Per-envoy states during revocation:

| State | Meaning |
|-------|---------|
| `revoking` | The seed has been revoked server-side, but the envoy still has the blob cached. Agents evict on the next check-in cycle (typically ≤30s after the revoke). |
| `revoked` | The envoy's most recent trait report no longer lists the hash in `swarm.blobs` or `swarm.downloading` — the blob has been evicted from its local cache. This is a one-way transition; once an envoy reports eviction, it stays `revoked` until the seed entry is pruned. |

`skipped` entries (envoys that never downloaded the blob) are dropped from the per-envoy list once the seed is revoked — there's nothing to evict and nothing for the operator to act on.

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
app-v2.tar.gz                a1b2c3d4e5f6     47.2 MB   vigoserver     7d         11/11
firmware.bin                 b2c3d4e5f678     12.1 MB   danlap         7d         8/10 (1 dl)
tar_errors.log               9faa44b9cb01    135.4 KB   annlap         7d         5/5 (5 cached)
```

| Column | Description |
|--------|-------------|
| Name | Seed label (original filename) |
| SHA256 | First 12 characters of the content hash |
| Size | Human-readable file size |
| Source | Hostname of the seeding envoy |
| Retention | Auto-delete duration, or `-` if keep forever |
| Progress | Complete/total target envoys (excludes source). Shows `(N cached)` if any envoys had the blob from a previous distribution, `(N dl)` if any are still downloading |

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

## Seed Authorization

An envoy can originate a seed if either:
1. Its hostname equals `server.hostname` in `server.yaml` — the server's own envoy is implicitly allowed. Every server runs a local vigo agent, so this is the canonical "server-side seeding" path (including `vigocli swarm distribute`, which writes directly to the server's seed dir).
2. Its hostname matches any pattern in `swarm.seeding_envoys` in `server.yaml`.

Otherwise the seed is rejected at check-in time. The agent moves the file into the envoy-local `seed/admin_rejected/` directory with a reason (`hostname "web-03.prod" not in swarm.seeding_envoys`) so the operator can act on it.

Patterns use `filepath.Match` semantics — supports `*`, `?`, `[abc]`. Dots are literal; segments between dots are matched independently. So `*.ci.example.com` matches `build-01.ci.example.com` but not `a.b.ci.example.com` (list both if you need multi-segment coverage).

Configure in `server.yaml`:

```yaml
server:
  hostname: "vigo.example.com"        # the server's own envoy — implicitly allowed

swarm:
  seeding_envoys:
    - "build-*.ci.example.com"
    - "vault.ops.example.com"
```

A bare `*` permits every enrolled envoy to seed and logs a WARN at server startup; narrow the glob unless that's intentional.

## Related

- [Swarm Concepts](../../engineering/subsystems/swarm.md) — How peer-to-peer distribution works
- [Swarm Operations](../../operations/swarm.md) — Configuration, monitoring, troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
