
# Envoy Swarm Load

**Location:** Tab on the envoy detail page (`/envoys/{id}`) — the **Swarm** button in the tab bar between Attack Surface and Sessions. HTMX-loads into the detail page's content area like every other tab (Drift, Runs, Traits, Config, Trace, Compliance, Attack Surface, Sessions); there is no standalone route.

Per-envoy disk footprint across the four swarm subsystems — [payload](swarm-swarmcast.md) (admin-distributed swarmcasts), [swarmgit](../../engineering/subsystems/swarmgit.md) (personal-DR bare repos), [longdrawer](../../engineering/subsystems/longdrawer.md) (LAN plaintext sync), and [lockbox](../../engineering/subsystems/lockbox.md) (encrypted sync).

The motivating use case is debugging full-disk incidents on a specific envoy: every row is a real on-disk artifact, every section is sorted largest first, and totals roll up per subsystem so the biggest consumer surfaces immediately.

## Summary cards

Five cards at the top:

- **Total Swarm Footprint** — sum of all four subsystems.
- **Payload**, **Swarmgit**, **Longdrawer**, **Lockbox** — per-subsystem totals.

A grossly unbalanced row (e.g. Swarmgit at 40 GB while the others sit at tens of MB) tells you which section to drill into first.

## Sections

Each section renders the same five-column table (Name / Size / Status / Progress / When), biggest first. The **Name** column is user-friendly — real filenames and filesystem paths, never internal labels or sha256 hashes.

### Swarmcast

Swarm blobs cached on this envoy. Covers both admin-distributed swarmcasts (`vigocli swarm distribute`) and cached swarmgit bundle content sitting in `/var/lib/vigo/swarm/.blobs/` before it's unpacked into a bare repo.

- **Name** — the blob's label. For admin payloads this is the filename. For swarmgit-labeled blobs (`swarmgit:<seeder>:<user>:<project>`) it's rendered as `swarmgit: <user>/<project> (from <seeder>)` — the internal label is only surfaced in the link target.
- **Status** — `source` (this envoy seeded the blob), `complete` (fully cached), `downloading` (partial, in-flight), `revoking` (seed revoked, eviction pending).
- **Progress** — `N/M chunks` for downloading rows, `—` otherwise.
- **Link** — deep-links to `/payload/{sha}` for the fleet-wide distribution view.

Cache size here is independent of the materialized swarmgit mirror size below — the same label can contribute to both sections because the blob (git bundle) and the extracted bare repo live as separate bytes on disk.

### Swarmgit

Materialized receiver-side mirrors under `/var/lib/vigo/swarm/swarmgit/mirrors/<seeder>/<user>/<project>.git`. One row per bare repo.

- **Name** — the absolute bare repo path.
- **Detail line** — `<user>/<project> — from <seeder> · git <short-sha>` so the operator can recognize the repo without reading the label.
- **Size** — from the `size_bytes` field of the [swarmgit trait](../traits/swarmgit.md). Walked by the agent at trait-write time.
- **Status** — `live` for fresh mirrors, `stale` for mirrors past the 30-day refresh threshold.
- **Link** — deep-links to `/swarmgit/{label}` for the fleet-wide mesh view.

### Longdrawer

Per-user LAN plaintext sync files under `~<user>/longdrawer/`. One row per file across every user with longdrawer state on this envoy.

- **Name** — `~<user>/longdrawer/<filename>` — the real path on disk.
- **Status** — always `live`; longdrawer tombstones are reported separately and don't consume disk.

### Lockbox

Per-user encrypted sync files under `~<user>/lockbox/`. One row per ciphertext file.

- **Name** — `~<user>/lockbox/<filename>`.
- **Size** — the ciphertext size on disk (not the plaintext size).
- **Status** — always `live`.

## Data source

Everything on the page comes from data the server already holds — no extra agent round-trip. Blob presence comes from the swarm `PeerTracker`; swarmgit/longdrawer/lockbox come from each envoy's most recent check-in trait.

Swarmgit sizes are the one field added specifically for this page: receiver-side `mirrors[]` entries now carry `size_bytes` (see [swarmgit trait](../traits/swarmgit.md)), computed by the agent during its trait projection.

## Related

- [Payload page](swarm-swarmcast.md) — fleet-wide view of one blob's distribution.
- [Swarmgit subsystem](../../engineering/subsystems/swarmgit.md) — how the personal-DR mesh works.
- [Longdrawer subsystem](../../engineering/subsystems/longdrawer.md) — LAN plaintext sync.
- [Lockbox subsystem](../../engineering/subsystems/lockbox.md) — encrypted per-user sync.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
