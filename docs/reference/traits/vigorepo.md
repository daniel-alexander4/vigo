---
title: vigorepo
---

# vigorepo

Reports vigorepo publishing and receiver state: active and paused source counts, per-source swarm labels with current git/blob hashes, discovery errors, publish errors, and stale receiver-side mirrors. The server uses this trait to drive the `/vigorepo` UI, to populate its ClaimTable, and to surface operator-actionable errors (invalid sidecars, oversize bundles, stale mirrors) without requiring anyone to grep agent logs.

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the vigorepo reactor wrote the most recent snapshot. Use this to detect a stuck reactor. |
| `active_sources` | int | Number of non-paused vigorepo sources discovered on this envoy. |
| `paused_sources` | int | Number of sources whose sidecar has `paused: true`. Paused sources are not re-bundled. |
| `sources` | object[] | One entry per validated discovered source. See below. |
| `source_errors` | object[] | Discovery errors — a `~/vigorepo/<project>/` directory that has a sidecar but failed validation. See below. |
| `publish_errors` | object[] | Publish errors — a source that validated but whose most recent bundle attempt failed. See below. |
| `stale_mirrors` | string[] | Receiver-side mirrors whose `last_refreshed` timestamp is more than 30 days old. List of full labels (`vigorepo:<seeder>:<user>:<project>`), not a count. Empty on publish-only envoys. Stale mirrors are NEVER auto-evicted — removal is strictly manual via `vigo repo prune`. See the concepts doc for the rationale. |

### `sources[]`

| Field | Type | Description |
|---|---|---|
| `label` | string | Swarm label: `vigorepo:<hostname>:<user>:<project>`. Matches the seed filename the agent writes into `default_seeds_dir()`. |
| `paused` | bool | `true` if the sidecar has `paused: true`. |
| `max_mirrors` | int | `max_mirrors` from the validated sidecar, already clamped into `[3, 10]`. |
| `tags` | string[] | `tags` from the validated sidecar (may be empty). |
| `description` | string | `description` from the validated sidecar (may be empty). |
| `include_envoys` | string[] | Optional `include_envoys` hostname-glob whitelist. Omitted from the trait payload when empty. |
| `exclude_envoys` | string[] | Optional `exclude_envoys` hostname-glob blacklist. Omitted when empty. |
| `when` | string | Optional `when:` expression string. Omitted when empty. |
| `git_sha` | string | HEAD sha at the time of the most recent successful bundle. Absent if the source has never successfully published (e.g. first-tick failure on a new source). |
| `blob_sha` | string | SHA256 of the most recent successful bundle file. Absent in the same never-published case. |

All sidecar fields (`max_mirrors`, `tags`, `description`, and the three optional filter fields) are how the server's vigorepo ClaimTable gets sidecar data. Rather than building a server-side swarm-blob download path to read the meta seed directly, the validated sidecar is routed through the agent's normal trait-reporting pipeline. The agent is the single source of truth for sidecar validation; the server consumes pre-validated data and runs per-candidate filtering against the live fleet before deterministic mirror selection.

### `source_errors[]`

| Field | Type | Description |
|---|---|---|
| `path` | string | Absolute path of the directory that was rejected. |
| `reason` | string | Human-readable reason (e.g. `"invalid sidecar: missing field 'owner'"`, `"sidecar owner matches self but .git/ is missing"`). |

### `publish_errors[]`

| Field | Type | Description |
|---|---|---|
| `label` | string | Swarm label of the source whose publish attempt failed. |
| `reason` | string | Human-readable reason — path re-check failure, `git rev-parse HEAD` failure, bundle-create failure, oversize bundle (> 20 GiB), or I/O error. |

## Example

```json
{
  "updated_at": 1744560000,
  "active_sources": 2,
  "paused_sources": 1,
  "sources": [
    {
      "label": "vigorepo:danlap:dan:dotfiles",
      "paused": false,
      "max_mirrors": 5,
      "tags": ["personal", "dotfiles"],
      "description": "shell config + editor setup",
      "git_sha": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      "blob_sha": "deadbeefcafef00d1122334455667788aabbccddeeff00112233445566778899"
    },
    {
      "label": "vigorepo:danlap:dan:notes",
      "paused": false,
      "max_mirrors": 3,
      "tags": [],
      "description": "",
      "git_sha": "11223344556677889900aabbccddeeff00112233",
      "blob_sha": "99887766554433221100ffeeddccbbaa998877665544332211009988776655"
    },
    {
      "label": "vigorepo:danlap:dan:archive",
      "paused": true,
      "max_mirrors": 3,
      "tags": ["cold"],
      "description": "old projects"
    }
  ],
  "source_errors": [
    {
      "path": "/home/dan/vigorepo/broken/.vigorepo.yaml",
      "reason": "invalid sidecar: unknown field `retention_days`"
    }
  ],
  "publish_errors": [
    {
      "label": "vigorepo:danlap:dan:huge-repo",
      "reason": "bundle for vigorepo:danlap:dan:huge-repo is 23622320128 bytes, exceeds 21474836480 byte cap"
    }
  ],
  "stale_mirrors": [
    "vigorepo:decommissioned-laptop:dan:old-project"
  ]
}
```

## Collection

Unlike most trait collectors, this one does not do filesystem scanning at collect time. Instead it reads a snapshot file (`/var/lib/vigo/vigorepo/traits.json`) that the vigorepo reactor writes at the end of every 5-second pass. This keeps the check-in hot path cheap and ensures trait data reflects an entire consistent reactor tick rather than an in-progress one.

If the reactor has never ticked — for example, because `git --version` on the host is below the MVP floor of 2.35 and the reactor refused to start, or because the agent is still in its first 5-second window — the snapshot file does not exist and the collector returns `null`, matching the pattern used by `swarm_seeds` on empty state.

Corrupt snapshots (mismatched JSON, wrong schema) are treated the same as a missing file: the collector logs a warning and returns `null`, so a bad snapshot cannot block trait reporting.

## Notes

- **Snapshot version is 1.** Schema changes will bump `TRAITS_SNAPSHOT_VERSION` in `agent/src/vigorepo/state.rs`. Because Vigo has zero users (see `CLAUDE.md`), versioning exists only as an early warning — there are no backward-compatibility shims.
- **Snapshot file permissions are `0600`.** The reactor runs as root and the trait collector also runs as root, so no world/group access is needed.
- **The collector is classified volatile** (runs every check-in cycle) because source state can change as fast as operators commit to their repos. The snapshot read is cheap — a single file read plus JSON parse — so the volatile classification is essentially free.
- **Edge-triggered logging in the reactor** means a persistent publish failure logs once, not every 5 seconds. The trait payload still shows the current failure on every check-in until it clears, so server-side alerting does not miss anything even though the agent log is quiet.
- **Staleness warnings are rate-limited** to once per 24 hours per label on the agent side, but the `stale_mirrors` trait payload reflects the current state on every check-in — server-side alerting sees stale mirrors as soon as they cross the 30-day threshold.
