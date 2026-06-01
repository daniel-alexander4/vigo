
# vigocli swarm filecast

Administrator-pushed file distribution on the [swarm](../../howto/set-up-swarm.md) substrate. Distribute files across the fleet via peer-to-peer transfer and monitor progress.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `distribute` | Seed a file for P2P distribution to targeted envoys |
| `status` | Show per-envoy distribution progress |
| `list` | List all filecast files and their distribution status |
| `revoke` | Revoke a filecast fleet-wide; agents evict on next check-in |
| `purge` | Drop revoked filecast tombstones from the manifest |
| `broken` | List broken seed manifests across envoys |

## Usage

```bash
vigocli swarm filecast [subcommand] [flags]
```

## distribute

Place a file in the server's seed directory for P2P distribution. The server registers the seed immediately and gossips the manifest entry to streaming agents.

```bash
vigocli swarm filecast distribute <file> --target <spec> [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | *(required)* | Comma-separated target spec: hostname globs, exact hostnames, trait filters (`key=value`), `-` prefix to exclude (exclusions win over inclusions). **No default** — running `distribute` without it (or with an empty value) prints a usage error listing the pattern forms and exits non-zero; it never falls back to a fleet-wide push. See examples below |
| `--label` | filename | Human-readable label for the file |
| `--retention` | `7d` | Auto-evict from agents after this duration (e.g. `7d`, `168h`). `0` = keep forever |

### Examples

```bash
# Distribute to all envoys
vigocli swarm filecast distribute agent-0.8.4-linux-amd64.tar.gz --target '*'

# Distribute to web servers with a custom label
vigocli swarm filecast distribute app-v2.tar.gz --target '*.web.*' --label app-v2

# Comma-separated patterns with exclusion
vigocli swarm filecast distribute image.iso --target '*lap*,-rasp*'

# Exclusion-only (all envoys except raspberries)
vigocli swarm filecast distribute update.tar.gz --target '-rasp*'

# Distribute by trait filter
vigocli swarm filecast distribute firmware.bin --target 'dmi.product_name=PowerEdge R750'

# Keep forever (no auto-eviction)
vigocli swarm filecast distribute golden-image.tar.gz --retention 0
```

```
Seeding app-v2.tar.gz (47.2 MB)...
  SHA256: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
  Size:   47.2 MB

  Seeded — distribution begins on next check-in cycle (12 envoys matched).
  Track progress: vigocli swarm filecast status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

If `--target` is omitted, `distribute` refuses and prints the recipient-pattern forms — it never silently goes fleet-wide:

```
--target is required: name the envoys this file goes to so a push never goes fleet-wide by accident.

  --target '*'                  every envoy
  --target '*,-raspberrypi'     every envoy except raspberrypi
  --target '-rasp*'             every envoy except hostnames matching rasp*
  --target 'danlap,annlap'      just those two
  --target 'os.distro=ubuntu'   envoys whose os.distro trait is "ubuntu"

Patterns: comma-separated; "-" prefix excludes; exclusions win; "host*" globs; "trait=value" matches.
```

If the target spec matches zero envoys, a warning is printed:

```
  Warning: target "nonexistent*" matched 0 envoys — no distribution will occur.
```

### Operability

`distribute` is the lone admin-write that rides the filecast substrate, so it is gated by the substrate's [kill switches](../../howto/set-up-swarm.md#operability--kill-switches). When the operator has set `swarm.enabled: false` or `swarm.filecast.enabled: false` in `server.yaml` and republished, the server responds with HTTP **403** and a message naming the offending knob. The denial is recorded as a `swarm.filecast.denied` entry in the [tamper-evident audit chain](../compliance-matrix.md) under the calling user's identity.

`status`, `list`, `match`, and `broken` (read-only views) are not gated — operators can still inspect the substrate's state while it's halted. `revoke` is also allowed when disabled, since cleanup needs to work on a halted substrate.

## status

Show per-envoy distribution progress. Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli swarm filecast status <sha256-or-prefix>
```

### Example

```bash
# Full hash
vigocli swarm filecast status a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd

# Short prefix (must be unique)
vigocli swarm filecast status a1b2c3
```

```
Filecast: app-v2
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
Filecast: app-v2
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

List all filecast files with distribution progress.

```bash
vigocli swarm filecast list [flags]
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

Revoke a filecast fleet-wide. Marks the seed as revoked in the server tracker, records a revocation in the manifest CRDT, and propagates the revocation to all envoys via gossip. Each envoy evicts the payload from its cache upon receiving the revocation; the original seeder additionally removes the source file from `seed/` and moves the cached payload to `graveyard/` for a 7-day grace window (a same-SHA reseed within that window resurrects the content without re-downloading).

On a LAN, all copies are removed within seconds via multicast gossip. Cross-subnet envoys receive the revocation on their next check-in or peer connection.

Accepts a full SHA256 hash or a unique prefix (minimum 6 characters).

```bash
vigocli swarm filecast revoke <sha256-or-prefix>
```

### Example

```bash
vigocli swarm filecast revoke a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

```
Revoked: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

Repeat revokes are idempotent:

```
Already revoked: a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd
```

To re-distribute the same content after deletion, run `vigocli swarm filecast distribute` again — the fresh manifest entry overrides the revocation.

## purge

`revoke` is a soft-delete — the entry stays in the manifest as a `revoked` tombstone (so the blob can't silently resurrect), and the retention pruner only garbage-collects those records after the retention window. `purge` lets you drop them now, so `list` and the UI stop showing stale revoked rows.

With no flag it purges every revoked entry. `--older-than <dur>` scopes the purge to tombstones past that age (matching what the auto-pruner does on a schedule). Active distributions are never touched.

```bash
vigocli swarm filecast purge                 # drop all revoked tombstones
vigocli swarm filecast purge --older-than 24h # only those revoked >24h ago
```

```
Purged 3 revoked filecast(s).
```

The purge is server-authoritative and cannot be re-poisoned: an agent gossiping its own copy of a tombstone can't re-create a purged entry, because the agent-ingest path only refreshes seeds that already exist as *active* on the server. (Agent-local tombstones clear on each envoy's own retention prune.)

## broken

List seed files with invalid or missing `.swarm` manifests across all envoys. Reports errors detected during the agent's seed trait collection. The on-disk sidecar extension remains `.swarm` — that names the substrate's manifest format, not the content class.

```bash
vigocli swarm filecast broken
```

### Example

```
ENVOY                          FILENAME                       ERROR
------------------------------------------------------------------------------------------
danlap                         firmware.bin                   missing .swarm manifest
plex                           backup.tar.gz                  invalid manifest: expected value at line 1
```

## Related

- [Swarm Substrate](../../howto/set-up-swarm.md) — The envoy P2P transport payload rides on
- [Swarm Operations](../../howto/set-up-swarm.md) — Configuration, monitoring, troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
