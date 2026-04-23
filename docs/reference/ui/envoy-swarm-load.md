
# Envoy Swarm Load

**Location:** Tab on the envoy detail page (`/envoys/{id}`) — the **Swarm Load** button in the tab bar between Attack Surface and Sessions. HTMX-loads into the detail page's content area like every other tab (Drift, Runs, Traits, Config, Trace, Compliance, Attack Surface, Sessions); there is no standalone route.

Per-envoy disk footprint across the four swarm subsystems — [lode](lode.md) (admin-distributed payloads), [cairn](../../engineering/subsystems/cairn.md) (personal-DR bare repos), [echo](../../engineering/subsystems/echo.md) (LAN plaintext sync), and [sigil](../../engineering/subsystems/sigil.md) (encrypted sync).

The motivating use case is debugging full-disk incidents on a specific envoy: every row is a real on-disk artifact, every section is sorted largest first, and totals roll up per subsystem so the biggest consumer surfaces immediately.

## Summary cards

Five cards at the top:

- **Total Swarm Footprint** — sum of all four subsystems.
- **Lode**, **Cairn**, **Echo**, **Sigil** — per-subsystem totals.

A grossly unbalanced row (e.g. Cairn at 40 GB while the others sit at tens of MB) tells you which section to drill into first.

## Sections

Each section renders the same five-column table (Name / Size / Status / Progress / When), biggest first. The **Name** column is user-friendly — real filenames and filesystem paths, never internal labels or sha256 hashes.

### Lode

Swarm blobs cached on this envoy. Covers both admin-distributed payloads (`vigocli swarm distribute`) and cached cairn bundle content sitting in `/var/lib/vigo/swarm/.blobs/` before it's unpacked into a bare repo.

- **Name** — the blob's label. For admin payloads this is the filename. For cairn-labeled blobs (`cairn:<seeder>:<user>:<project>`) it's rendered as `cairn: <user>/<project> (from <seeder>)` — the internal label is only surfaced in the link target.
- **Status** — `source` (this envoy seeded the blob), `complete` (fully cached), `downloading` (partial, in-flight), `revoking` (seed revoked, eviction pending).
- **Progress** — `N/M chunks` for downloading rows, `—` otherwise.
- **Link** — deep-links to `/lode/{sha}` for the fleet-wide distribution view.

Cache size here is independent of the materialized cairn mirror size below — the same label can contribute to both sections because the blob (git bundle) and the extracted bare repo live as separate bytes on disk.

### Cairn

Materialized receiver-side mirrors under `/var/lib/vigo/cairn/mirrors/<seeder>/<user>/<project>.git`. One row per bare repo.

- **Name** — the absolute bare repo path.
- **Detail line** — `<user>/<project> — from <seeder> · git <short-sha>` so the operator can recognize the repo without reading the label.
- **Size** — from the `size_bytes` field of the [cairn trait](../traits/cairn.md). Walked by the agent at trait-write time.
- **Status** — `live` for fresh mirrors, `stale` for mirrors past the 30-day refresh threshold.
- **Link** — deep-links to `/cairn/{label}` for the fleet-wide mesh view.

### Echo

Per-user LAN plaintext sync files under `~<user>/echo/`. One row per file across every user with echo state on this envoy.

- **Name** — `~<user>/echo/<filename>` — the real path on disk.
- **Status** — always `live`; echo tombstones are reported separately and don't consume disk.

### Sigil

Per-user encrypted sync files under `~<user>/sigil/`. One row per ciphertext file.

- **Name** — `~<user>/sigil/<filename>`.
- **Size** — the ciphertext size on disk (not the plaintext size).
- **Status** — always `live`.

## Data source

Everything on the page comes from data the server already holds — no extra agent round-trip. Blob presence comes from the swarm `PeerTracker`; cairn/echo/sigil come from each envoy's most recent check-in trait.

Cairn sizes are the one field added specifically for this page: receiver-side `mirrors[]` entries now carry `size_bytes` (see [cairn trait](../traits/cairn.md)), computed by the agent during its trait projection.

## Related

- [Lode page](lode.md) — fleet-wide view of one blob's distribution.
- [Cairn subsystem](../../engineering/subsystems/cairn.md) — how the personal-DR mesh works.
- [Echo subsystem](../../engineering/subsystems/echo.md) — LAN plaintext sync.
- [Sigil subsystem](../../engineering/subsystems/sigil.md) — encrypted per-user sync.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
