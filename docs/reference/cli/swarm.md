
# vigocli swarm

Read-only admin view of the [swarm substrate](../../engineering/subsystems/swarm.md) — the envoy peer-to-peer network that all four content subsystems ride on.

> **This is the substrate, not content.** For admin-pushed payloads (formerly `vigocli swarm distribute`), use [`vigocli swarm payload`](swarm-payload.md). For personal-DR mirrors, [`vigocli swarm gitmirror`](swarm-gitmirror.md). For per-user sync meshes, [`vigocli swarm filesync`](swarm-filesync.md) and [`vigocli swarm cryptsync`](swarm-cryptsync.md).

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Fleet-wide substrate totals — envoys online, substrate-active, seeders, manifest counts, total footprint |
| `peers` | One row per envoy with substrate-participation counts |
| `cleanup` | Fleet-wide blob-cache hygiene sweep — removes dangling `files/<label>` symlinks and `.partial/` dirs whose final blob is already assembled |

`status` and `peers` support `--json` for machine-readable output.

## status

```bash
vigocli swarm status
```

```
Swarm substrate — generated 2026-04-23 18:24:11

Envoys online     7 / 7
Substrate active  5
Seeding envoys    2
Fleet footprint   482 MB
Manifest entries  12 active · 3 revoked
Active payloads   8 payload · 4 gitmirror

Seeding allowlist
  vigoserver  (server.hostname — implicit)
  *.ci.example.com
```

Counts reflect the rollup of server-side state (`PeerTracker` + `ManifestStore` + `FleetIndex`); no agent-side changes required.

- **Substrate active** — envoys with at least one peer record in the tracker or acting as a seeder for at least one payload
- **Fleet footprint** — sum of cached blob sizes across all envoys (excludes in-flight partials)
- **Active payloads** — split by content subsystem; filesync/cryptsync are per-user and don't surface here

## peers

```bash
vigocli swarm peers [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--sort` | `footprint` | Sort key: `footprint` \| `blobs` \| `hostname` |
| `--limit` | `50` | Max rows to return; `0` for all |

### Example

```
Swarm peers — generated 2026-04-23 18:24:12 · sorted by footprint

HOSTNAME        STATUS   SUBSTRATE  SEEDING  CACHED  DOWNLOADING  FOOTPRINT  LAST SEEN
danlap          online   active     2        14      0            312 MB     2026-04-23 18:24
homeserver      online   active     0        9       0            118 MB     2026-04-23 18:23
plex            online   active     0        6       0            52 MB      2026-04-23 18:22
vigoserver      online   active     3        0       0            0 B        2026-04-23 18:24
annlap          offline  idle       0        0       0            0 B        2026-04-22 21:17
```

| Column | Description |
|--------|-------------|
| HOSTNAME | Envoy hostname (links to the envoy detail page in the web UI) |
| STATUS | `online` (within 2.5× check-in interval) or `offline` |
| SUBSTRATE | `active` (has peer records or is seeding) or `idle` |
| SEEDING | Number of payloads this envoy is originating |
| CACHED | Count of complete blobs the envoy holds |
| DOWNLOADING | Count of partial blobs in progress |
| FOOTPRINT | Sum of cached blob sizes |
| LAST SEEN | Last successful check-in |

## cleanup

```bash
vigocli swarm cleanup [--target <pat>]
```

Dispatches the hidden `vigo swarm _apply-cleanup` verb to every targeted envoy via task dispatch. Each envoy walks its own blob cache and removes:

- **dangling `files/<label>` symlinks** — pointers whose target blob has been removed (e.g. an interrupted revoke or a label collision left the symlink behind).
- **stale `.partial/` directories** — in-progress download dirs whose corresponding final blob has already been assembled (download completed but the assemble step skipped the cleanup, e.g. on a pre-0.34 assembler or after a crash).

Idempotent — re-running yields zero changes. The agent prints a one-line summary on stdout (`swarm cleanup: dangling_symlinks=N stale_partials=N errors=N`) which lands in the per-envoy task-run record.

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | `*` | Hostname pattern (`filepath.Match` glob) scoping the sweep. Default `*` = whole fleet. |

The CLI returns a one-line dispatch summary plus the task-run ID for the cleanup dispatch — per-envoy results live under `vigocli task` (push runs are separate from convergence runs in `vigocli runs`):

```
$ vigocli swarm cleanup
Dispatched 'vigo swarm _apply-cleanup' to 5 envoy(s) matching "*".
Per-envoy results:
  vigocli task status <id>
```

`vigocli task list` shows recent cleanup dispatches across runs.

**Out of scope.** This verb does **not** clean revoked-blob eviction (the manifest-gossip path on the agent already handles that), retention-expired blobs (`RetentionPruner` server-side), or graveyard entries (age-based, separately).

### Tag-pinned colors in the swarm visualization

Adjacent operator knob from the same release: a `viz.color=<name|hex>` tag on an envoy pins it to a fixed hue in the `/swarm` and `/swarm/payload/<sha>` visualizations regardless of fleet churn. Set via `vigocli envoys tag <hostname> viz.color=green`. Recognized names: `yellow`, `red`, `green`, `blue`, `peach`, `mauve`, `teal`, `pink`. A literal hex code (`#rrggbb` or `#rgb`) is accepted as a passthrough. Bad values fall through to the default hash-based slot assignment.

## Related

- [Swarm Substrate](../../engineering/subsystems/swarm.md) — How the peer-to-peer network works
- [Swarm page](../ui/swarm.md) — `/swarm` in the web UI
- [`vigocli swarm payload`](swarm-payload.md) — Admin-pushed payloads on the substrate
- [`vigocli swarm gitmirror`](swarm-gitmirror.md), [`vigocli swarm filesync`](swarm-filesync.md), [`vigocli swarm cryptsync`](swarm-cryptsync.md) — Other content subsystems on the substrate
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
