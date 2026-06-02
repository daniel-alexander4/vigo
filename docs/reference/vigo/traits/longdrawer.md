---
title: longdrawer
---

# longdrawer

Reports each envoy's local view of the [longdrawer](../../../howto/swarm/set-up-longdrawer.md) per-user LAN directory sync — the file set and tombstones in every enrolled user's `~/longdrawer/`. The server's `longdrawermesh` aggregator joins these across the fleet to render per-user cross-envoy consistency on the `/swarm/longdrawer` page.

The longdrawer reactor persists `UserState { files, tombstones }` per user at `/var/lib/vigo/longdrawer/<user>.json` on each reconcile; this collector reads every state file and ships it verbatim — no separate snapshot writer. Returns `null` when no per-user state files exist (an envoy that never had a `~/longdrawer/` directory).

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `users` | object[] | One entry per user with longdrawer state on this envoy. See below. |

### `users[]`

| Field | Type | Description |
|---|---|---|
| `user` | string | Unix username whose `~/longdrawer/` this describes. |
| `file_count` | int | Total files in the user's longdrawer (not bounded by the `files` cap). |
| `total_bytes` | int | Sum of all file sizes. |
| `files` | object[] | Recent files, newest first, capped at 20 (`RECENT_FILE_CAP`). Each: `name`, `size`, `mtime` (unix seconds), `sha256`. |
| `files_digest` | string | 16-byte hex digest over the sorted full file set (`name|sha256|size`). Lets the aggregator detect cross-envoy divergence without carrying every file on every trait. |
| `tombstone_count` | int | Number of deletion tombstones. |
| `tombstones` | object[] | Each: `name`, `deletion_time` (unix seconds). Newest first. |

## When the trait is absent

`null` when `/var/lib/vigo/longdrawer/` doesn't exist or holds no `<user>.json` state files — an envoy with no longdrawer participation contributes nothing.

## See also

- [`vigocli swarm longdrawer`](../../vigocli/swarm-longdrawer.md) — the fleet view this trait feeds.
- [Set up longdrawer](../../../howto/swarm/set-up-longdrawer.md).
- [`lockbox`](lockbox.md) — longdrawer's encryption-first sibling.
