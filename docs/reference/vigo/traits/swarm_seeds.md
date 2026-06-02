---
title: swarm_seeds
---

# swarm_seeds

Reports the seed files this envoy makes available for envoy-originated swarm distribution. Scans `/var/lib/vigo/swarm/seed/` for files that have a matching `.swarm` JSON manifest, computes each file's SHA-256, and symlinks it into the blob cache so the peer server can serve it. The server uses this to know which envoys can originate which blobs.

Files without a matching manifest, or with an unreadable/invalid one, are reported separately under `broken_seeds` so the operator can see a misconfigured seed instead of it silently never distributing. Revoked seeds (present in the graveyard and not re-distributed since) are skipped. Returns `null` when the seed directory doesn't exist or holds nothing reportable.

## Fields

The payload carries whichever of these is non-empty (there is no `updated_at`):

| Field | Type | Description |
|---|---|---|
| `seeds` | object[] | One entry per distributable seed. Omitted when empty. See below. |
| `broken_seeds` | object[] | One entry per seed with a missing/invalid `.swarm` manifest. Omitted when empty. Each: `filename`, `error`. |

### `seeds[]`

| Field | Type | Description |
|---|---|---|
| `sha256` | string | SHA-256 of the seed file (the blob id peers fetch by). Cached by `(filename, size, mtime)` so multi-GiB files aren't rehashed every cycle. |
| `label` | string | The seed's filename. |
| `size` | int | File size in bytes. |
| `target` | string | Hostname/pattern the seed is targeted at, from the `.swarm` manifest. |
| `retention` | string | Retention policy from the manifest. |

## When the trait is absent

`null` when `/var/lib/vigo/swarm/seed/` doesn't exist, can't be read, or every file was either revoked or has no manifest *and* there were no broken seeds to report.

## See also

- [`swarm`](../../../howto/swarm/set-up-swarm.md) — the substrate seeds distribute over.
- [`vigocli swarm filecast`](../../vigocli/swarm-filecast.md) — admin-pushed file distribution that rides the same substrate.
