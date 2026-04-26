---
title: gitmirror
---

# gitmirror

Reports gitmirror publishing and receiver state: active and paused source counts, per-source swarm labels with current git/blob hashes, discovery errors, publish errors, and stale receiver-side mirrors.

As of **0.23.0** (Phase 8 of the P2P-first rearchitecture), the server **no longer consumes this trait** for mirror selection or delivery — gitmirror is fully peer-to-peer, with LAN multicast discovery and explicit cross-subnet `cross_subnet_mirrors:` push doing all the work. The trait is still emitted by every gitmirror-enabled envoy for operator observability: you can look at it via `vigocli traits` or the web UI to see what a given envoy is publishing and whether it has any stale mirrors, but nothing on the server side acts on the values.

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the gitmirror reactor wrote the most recent snapshot. Use this to detect a stuck reactor. |
| `active_sources` | int | Number of non-paused gitmirror sources discovered on this envoy. |
| `paused_sources` | int | Number of sources whose sidecar has `paused: true`. Paused sources are not re-bundled. |
| `sources` | object[] | One entry per validated discovered source. See below. |
| `source_errors` | object[] | Discovery errors — a `~/gitmirror/<project>/` directory that has a sidecar but failed validation. See below. |
| `publish_errors` | object[] | Publish errors — a source that validated but whose most recent bundle attempt failed. See below. |
| `stale_mirrors` | string[] | Receiver-side mirrors whose `last_refreshed` timestamp is more than 30 days old. List of full labels (`gitmirror:<seeder>:<user>:<project>`), not a count. Empty on publish-only envoys. Stale mirrors are NEVER auto-evicted — removal is strictly manual via `vigo swarm gitmirror prune`. See the concepts doc for the rationale. |
| `mirrors` | object[] | Receiver-side materialized mirrors held by this envoy. One entry per bare git repo at `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git`. The server joins this against each publisher's `sources[].selected_mirrors` to render the fleet-wide Mirror Status table on `/gitmirror/{label}`. Empty on publish-only envoys. See below. |

### `sources[]`

| Field | Type | Description |
|---|---|---|
| `label` | string | Swarm label: `gitmirror:<hostname>:<user>:<project>`. Matches the seed filename the agent writes into `default_seeds_dir()`. |
| `paused` | bool | `true` if the sidecar has `paused: true`. |
| `max_mirrors` | int | `max_mirrors` from the validated sidecar, already clamped into `[3, 10]`. |
| `tags` | string[] | `tags` from the validated sidecar (may be empty). |
| `description` | string | `description` from the validated sidecar (may be empty). |
| `include_envoys` | string[] | Optional `include_envoys` hostname-glob whitelist. Omitted from the trait payload when empty. |
| `exclude_envoys` | string[] | Optional `exclude_envoys` hostname-glob blacklist. Omitted when empty. |
| `when` | string | Optional `when:` expression string. Omitted when empty. |
| `git_sha` | string | HEAD sha at the time of the most recent successful bundle. Absent if the source has never successfully published (e.g. first-tick failure on a new source). |
| `blob_sha` | string | SHA256 of the most recent successful bundle file. Absent in the same never-published case. |

All sidecar fields (`max_mirrors`, `tags`, `description`, and the optional filter fields) are surfaced here so operators can see each source envoy's publishing config without reading the `.gitmirror.yaml` on disk. As of 0.23.0 these fields are purely observational — selection happens peer-to-peer on the source envoy itself, not on the server. The agent is the only component that parses and validates sidecars; the trait payload is a read-only snapshot of that validation output.

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

### `mirrors[]`

| Field | Type | Description |
|---|---|---|
| `label` | string | Swarm label this mirror materializes (`gitmirror:<seeder>:<user>:<project>`). |
| `seeder` | string | Hostname of the publisher that owns this repo. |
| `user` | string | Unix user on the seeder whose home holds the source. |
| `project` | string | Repo directory basename. |
| `git_sha` | string | Git HEAD sha currently materialized (populated from the most recently applied bundle). |
| `blob_sha` | string | SHA256 of the bundle that last refreshed this mirror. Matches the publisher's `blob_sha` when fully synced. |
| `last_refreshed` | int | Unix seconds of the most recent successful bundle apply. Used by the server to detect drift and by the agent to compute the `stale` flag. |
| `stale` | bool | Precomputed: `now - last_refreshed > 30 days`. Redundant with the label appearing in `stale_mirrors` but cheaper to render in per-row UI. |
| `next_gc_at` | int | Unix seconds when the mirror becomes eligible for `vigo swarm gitmirror prune` based on retention rules. Advisory only — GC is operator-triggered. |
| `size_bytes` | int | Sum of file bytes under the bare repo path at trait-write time. Walked by the receiver's trait projection with a 50,000-entry cap so pathological loose-object counts don't stall the check-in. Surfaced on the envoy detail page's **Swarm** tab so operators can rank disk consumers. `0` means either the repo is empty (never-materialized) or the walk failed (permission/IO). |

## Example

```json
{
  "updated_at": 1744560000,
  "active_sources": 2,
  "paused_sources": 1,
  "sources": [
    {
      "label": "gitmirror:danlap:dan:dotfiles",
      "paused": false,
      "max_mirrors": 5,
      "tags": ["personal", "dotfiles"],
      "description": "shell config + editor setup",
      "git_sha": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      "blob_sha": "deadbeefcafef00d1122334455667788aabbccddeeff00112233445566778899"
    },
    {
      "label": "gitmirror:danlap:dan:notes",
      "paused": false,
      "max_mirrors": 3,
      "tags": [],
      "description": "",
      "git_sha": "11223344556677889900aabbccddeeff00112233",
      "blob_sha": "99887766554433221100ffeeddccbbaa998877665544332211009988776655"
    },
    {
      "label": "gitmirror:danlap:dan:archive",
      "paused": true,
      "max_mirrors": 3,
      "tags": ["cold"],
      "description": "old projects"
    }
  ],
  "source_errors": [
    {
      "path": "/home/dan/gitmirror/broken/.gitmirror.yaml",
      "reason": "invalid sidecar: unknown field `retention_days`"
    }
  ],
  "publish_errors": [
    {
      "label": "gitmirror:danlap:dan:huge-repo",
      "reason": "bundle for gitmirror:danlap:dan:huge-repo is 23622320128 bytes, exceeds 21474836480 byte cap"
    }
  ],
  "stale_mirrors": [
    "gitmirror:decommissioned-laptop:dan:old-project"
  ],
  "mirrors": [
    {
      "label": "gitmirror:danlap:dan:dotfiles",
      "seeder": "danlap",
      "user": "dan",
      "project": "dotfiles",
      "git_sha": "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
      "blob_sha": "deadbeefcafef00d1122334455667788aabbccddeeff00112233445566778899",
      "last_refreshed": 1744559481,
      "stale": false,
      "next_gc_at": 1747151481,
      "size_bytes": 4096
    }
  ]
}
```

## Collection

Unlike most trait collectors, this one does not do filesystem scanning at collect time. Instead it reads a snapshot file (`/var/lib/vigo/swarm/gitmirror/traits.json`) that the gitmirror reactor writes at the end of every 5-second pass. This keeps the check-in hot path cheap and ensures trait data reflects an entire consistent reactor tick rather than an in-progress one.

If the reactor has never ticked — for example, because `git --version` on the host is below the MVP floor of 2.35 and the reactor refused to start, or because the agent is still in its first 5-second window — the snapshot file does not exist and the collector returns `null`, matching the pattern used by `swarm_seeds` on empty state.

Corrupt snapshots (mismatched JSON, wrong schema) are treated the same as a missing file: the collector logs a warning and returns `null`, so a bad snapshot cannot block trait reporting.

## Notes

- **Snapshot version is 1.** Schema changes will bump `TRAITS_SNAPSHOT_VERSION` in `agent/src/gitmirror/state.rs`. Because Vigo has zero users (see `CLAUDE.md`), versioning exists only as an early warning — there are no backward-compatibility shims.
- **Snapshot file permissions are `0600`.** The reactor runs as root and the trait collector also runs as root, so no world/group access is needed.
- **The collector is classified volatile** (runs every check-in cycle) because source state can change as fast as operators commit to their repos. The snapshot read is cheap — a single file read plus JSON parse — so the volatile classification is essentially free.
- **Edge-triggered logging in the reactor** means a persistent publish failure logs once, not every 5 seconds. The trait payload still shows the current failure on every check-in until it clears, so server-side alerting does not miss anything even though the agent log is quiet.
- **Staleness warnings are rate-limited** to once per 24 hours per label on the agent side, but the `stale_mirrors` trait payload reflects the current state on every check-in — server-side alerting sees stale mirrors as soon as they cross the 30-day threshold.
