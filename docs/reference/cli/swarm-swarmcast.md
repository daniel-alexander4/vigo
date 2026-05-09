
# vigocli payload

Administrator-pushed file distribution on the [swarm](../../engineering/subsystems/swarm.md) substrate. Distribute payloads across the fleet via peer-to-peer transfer and monitor progress.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `distribute` | Seed a file for P2P distribution to targeted envoys |
| `status` | Show per-envoy distribution progress |
| `list` | List all payload payloads and their distribution status |
| `revoke` | Revoke a payload fleet-wide; agents evict on next check-in |
| `broken` | List broken seed manifests across envoys |

## Usage

```bash
vigocli payload [subcommand] [flags]
```

## distribute

Place a file in the server's seed directory for P2P distribution. The server registers the seed immediately and gossips the manifest entry to streaming agents.

```bash
vigocli payload distribute <file> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `*` | Comma-separated target spec: hostname globs, trait filters, `-` prefix to exclude. See examples below |
| `--label` | filename | Human-readable label for the payload |
| `--retention` | `7d` | Auto-evict from agents after this duration (e.g. `7d`, `168h`). `0` = keep forever |

### Examples

```bash
# Distribute to all envoys
vigocli payload distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers with a custom label
vigocli payload distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Comma-separated patterns with exclusion
vigocli payload distribute image.iso --target '*lap*,-rasp*'

# Exclusion-only (all envoys except raspberries)
vigocli payload distribute update.tar.gz --target '-rasp*'

# Distribute by trait filter
vigocli payload distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'

# Keep forever (no auto-eviction)
vigocli payload distribute golden-image.tar.gz --retention 0
```

```
Seeding app-v2.tar.gz (47.2 MB)...
  SHA256: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
  Size:   47.2 MB

  Seeded — distribution begins on next check-in cycle (12 envoys matched).
  Track progress: vigocli payload status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

If the target spec matches zero envoys, a warning is printed:

```
  Warning: target "nonexistent*" matched 0 envoys — no distribution will occur.
```

### Operability

`distribute` is the lone admin-write that rides the swarmcast substrate, so it is gated by the substrate's [kill switches](../../engineering/subsystems/swarm.md#operability--kill-switches). When the operator has set `swarm.enabled: false` or `swarm.swarmcast.enabled: false` in `server.yaml` and republished, the server responds with HTTP **403** and a message naming the offending knob. The denial is recorded as a `swarm.swarmcast.denied` entry in the [tamper-evident audit chain](../../compliance/tamper-evident-audit.md) under the calling user's identity.

`status`, `list`, `match`, and `broken` (read-only views) are not gated — operators can still inspect the substrate's state while it's halted. `revoke` is also allowed when disabled, since cleanup needs to work on a halted substrate.

## status

Show per-envoy distribution progress. Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli payload status <sha256-or-prefix>
```

### Example

```bash
# Full hash
vigocli payload status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd

# Short prefix (must be unique)
vigocli payload status a1b2c3
```

```
Payload: app-v2
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
| `★`  | `source`      | This envoy originated the payload. Has every chunk by construction — never "downloads" its own seed. Distinct from `complete` so operators can tell seeders apart from downloaders at a glance. Not included in the total count. |
| `✓`  | `complete`    | Envoy has downloaded every chunk from its peers. |
| `✓`  | `cached`      | Envoy already had the payload from a previous distribution — no download occurred. Displayed in cyan (CLI) or blue (web UI) to distinguish from a fresh download. |
| `▶`  | `downloading` | Envoy has a partial chunk set and is still fetching. |
| (no icon) | `pending` | Envoy matches the target pattern but hasn't reported any chunks yet. |
| `⚠`  | `skipped`     | Envoy rejected the payload (e.g., authorization, disk, policy). |

If the payload has been revoked, the summary layout changes — source/complete/downloading/pending/skipped counts are replaced with revoking/revoked counts, and each envoy row shows its per-host eviction progress:

```
Payload: app-v2
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
| `revoking` | The payload has been revoked server-side, but the envoy still has it cached. Agents evict on the next check-in cycle (typically ≤30s after the revoke). |
| `revoked` | The envoy's most recent trait report no longer lists the hash in `swarm.blobs` or `swarm.downloading` — the payload has been evicted from its local cache. This is a one-way transition; once an envoy reports eviction, it stays `revoked` until the seed entry is pruned. |

`skipped` entries (envoys that never downloaded the payload) are dropped from the per-envoy list once the seed is revoked — there's nothing to evict and nothing for the operator to act on.

## list

List all payload payloads with distribution progress.

```bash
vigocli payload list [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--source` | | Filter by source hostname |
| `--status` | | Filter by distribution status: `active` (pending/downloading) or `complete` |
| `--revoked` | `false` | Include revoked payloads (operator-revoked or retention-expired) |

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
| Name | Payload label (original filename) |
| SHA256 | First 12 characters of the content hash |
| Size | Human-readable size |
| Source | Hostname of the seeding envoy |
| Retention | Auto-delete duration, or `-` if keep forever |
| Progress | Complete/total target envoys (excludes source). Shows `(N cached)` if any envoys had the payload from a previous distribution, `(N dl)` if any are still downloading |

## revoke

Revoke a payload fleet-wide. Marks the seed as revoked in the server tracker, records a revocation in the manifest CRDT, and propagates the revocation to all envoys via gossip. Each envoy evicts the payload from its cache upon receiving the revocation; the original seeder additionally removes the source file from `seed/` and moves the cached payload to `graveyard/` for a 7-day grace window (a same-SHA reseed within that window resurrects the content without re-downloading).

On a LAN, all copies are removed within seconds via multicast gossip. Cross-subnet envoys receive the revocation on their next check-in or peer connection.

Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli payload revoke <sha256-or-prefix>
```

### Example

```bash
vigocli payload revoke a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

```
Revoked: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

Repeat revokes are idempotent:

```
Already revoked: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

To re-distribute the same content after deletion, run `vigocli swarm swarmcast distribute` again — the fresh manifest entry overrides the revocation.

## broken

List seed files with invalid or missing `.swarm` manifests across all envoys. Reports errors detected during the agent's seed trait collection. The on-disk sidecar extension remains `.swarm` — that names the substrate's manifest format, not the content class.

```bash
vigocli payload broken
```

### Example

```
ENVOY                          FILENAME                       ERROR
------------------------------------------------------------------------------------------
danlap                         firmware.bin                   missing .swarm manifest
plex                           backup.tar.gz                  invalid manifest: expected value at line 1
```

## Related

- [Swarm Substrate](../../engineering/subsystems/swarm.md) — The envoy P2P transport payload rides on
- [Swarm Operations](../../operations/swarm.md) — Configuration, monitoring, troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
