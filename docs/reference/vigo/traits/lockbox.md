---
title: lockbox
---

# lockbox

Reports each envoy's local view of the [lockbox](../../../howto/set-up-lockbox.md) per-user encrypted directory sync — the ciphertext file set, tombstones, and known recipients in every enrolled user's `~/lockbox/`. The server's `lockboxmesh` aggregator joins these across the fleet to render per-user cross-envoy consistency, tombstone propagation, and recipient drift on the `/swarm/lockbox` page.

Lockbox holds **ciphertext at rest** on every envoy; this trait reports metadata only — file names, sizes, hashes of the encrypted blobs, and recipient pubkeys, never plaintext. Returns `null` when no users are enrolled.

> Puddle session-unlock state is **not** reported here. It's a puddle fact — see the [`puddle`](puddle.md) trait, its sole reporter.

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `users` | object[] | One entry per enrolled user. See below. |

### `users[]`

| Field | Type | Description |
|---|---|---|
| `user` | string | Unix username whose `~/lockbox/` this describes. |
| `file_count` | int | Total ciphertext files in the synced corpus (excludes `.failed` artifacts). |
| `failed_file_count` | int | ADR-012 `.failed` artifacts — files whose encrypt failed permanently and sit in plaintext, never syncing. Surfaced so the mesh can expose them instead of leaving the failure journal-only. |
| `total_bytes` | int | Sum of ciphertext file sizes. |
| `files` | object[] | Recent files, newest first, capped at 20 (`RECENT_FILE_CAP`). Each: `name`, `size`, `mtime` (unix seconds), `sha256` of the ciphertext. |
| `files_digest` | string | 16-byte hex digest over the sorted full file set — lets the aggregator detect cross-envoy divergence without carrying every file. |
| `tombstone_count` | int | Number of deletion tombstones. |
| `tombstones` | object[] | Each: `name`, `deletion_time` (unix seconds), `source_hostname` (the envoy that originated the delete). |
| `peers` | object[] | Known recipients for this user from the lockbox discovery table. Each: `hostname`, `pubkey` (age recipient), `sign_pubkey`, `last_seen` (unix seconds). The aggregator flags **recipient drift** when two envoys report different peer sets. |

## When the trait is absent

`null` when no user on this envoy has an enrolled lockbox identity.

## See also

- [`vigocli swarm lockbox`](../../vigocli/swarm-lockbox.md) — the fleet view this trait feeds.
- [Set up lockbox](../../../howto/set-up-lockbox.md).
- [`puddle`](puddle.md) — owns the identity + session-unlock state lockbox builds on.
- [`longdrawer`](longdrawer.md) — lockbox's plaintext LAN sibling.
