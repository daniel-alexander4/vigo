
# Swarm Page

**Route:** `/swarm`

Displays all swarm seeds with distribution progress. Auto-refreshes every 5 seconds on the detail view.

## List View

Shows all files in a table with distribution summaries.

| Column | Description |
|--------|-------------|
| Status | Color-coded pill: `complete` (green), `distributing` (yellow), `revoked` (gray) |
| Label | File label (original filename), links to detail view |
| SHA256 | First 12 characters of the content hash |
| Source | Seeder hostname (the server is an envoy too, so CLI distributions show the server's hostname) |
| Size | Human-readable file size |
| Chunks | Number of 1 MiB chunks |
| Created | When the file was uploaded or seeded |
| Distribution | Status pill counts: source / complete / cached / downloading / pending / skipped (active blobs), or revoking / revoked (revoked blobs — in-flight and fully-evicted envoy counts). The total excludes the source — it counts only target envoys. `cached` (blue pill) means the envoy already had the blob from a previous distribution. |

Stat cards at the top show total file count, total size, and active distributions (any file with pending or downloading envoys).

## Detail View

**Route:** `/swarm/{sha256}`

Shows a single blob with per-envoy distribution status.

**Blob metadata:** SHA256, label, source (seeder hostname), retention, size, chunks, created time.

**Per-envoy table** (auto-refreshes via HTMX every 5 seconds):

| Column | Description |
|--------|-------------|
| Hostname | Envoy hostname (clickable — expands per-chunk detail) |
| Status | `source` (this envoy is the seeder — has every chunk by construction), `complete` (downloaded every chunk from peers), `cached` (already had the blob from a previous distribution — blue pill, no download occurred), `downloading`, `pending`, `skipped`, `revoking`, or `revoked` (see [swarm CLI reference](../cli/swarm.md) for the revoke state machine) |
| Progress | Chunk progress bar with count (e.g., 31/47) |
| Time | Transfer duration from first report to completion |
| Sources | Where chunks came from — hostname-resolved counts (e.g., "danlap: 8 plex: 3 server: 1") |
| Skip Reason | Why the envoy was skipped (e.g., insufficient disk space) |

**Per-chunk detail** (click a row to expand): Shows each chunk with its source and download time in milliseconds. Displays as compact chips: `0:danlap:1531 45ms  1:plex:1531 52ms  ...`

### Visualization View (`/swarm/viz`)

Alternative visualization of swarm distributions with four server-rendered views:

1. **Chunk distribution heatmap** — SVG grid showing which chunks each envoy has, colored by status (complete/downloading/missing/skipped).
2. **Chunk flow matrix** — source × target grid of chunk counts. Each cell shows how many chunks its row-peer served to its column-peer, with background intensity scaled from the row-peer's color so high-throughput flows stand out and unused pairs stay inert. The diagonal is always blank (self-transfers don't exist). Pure HTML/CSS — no JavaScript.
3. **Download Duration** — horizontal bar chart per envoy, sorted slowest → fastest, width normalized against the slowest envoy.
4. **Per-envoy source strips** — mini heatmap colored by the peer that served each chunk. When per-chunk detail is unavailable (e.g., cached/resurrected blobs), source colors are synthesized proportionally from the aggregate source breakdown so the strip shows the correct distribution ratio instead of all-grey.

When more than 24 envoys participate in a distribution, visualizations are capped to the 24 alphabetically-first envoys (matching the size of the tiered color palette). The full envoy table remains uncapped.

#### Per-envoy visual identity

Every envoy gets a deterministic hex color assigned by alphabetic slot in a 24-slot tiered palette so the same envoy looks identical across every viz section and across page refreshes:

- **Slots 0–7** — Catppuccin Mocha accents (Yellow, Red, Green, Blue, Peach, Mauve, Teal, Pink)
- **Slots 8–15** — Catppuccin Latte (deeper/saturated variants of the same eight hues)
- **Slots 16–23** — Catppuccin Mocha again; the fleet is large enough that perfect uniqueness has already broken down, and row labels remain readable

Slot assignment is alphabetic by hostname, so an envoy keeps the same color regardless of its status at render time. Fleets beyond 24 envoys drop the excess from the visualizations entirely rather than collapsing to a fallback grey — legend saturation is a harder failure than omission. The full envoy table above the visualizations still lists every envoy.

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How swarm works
- [Swarm Operations](../../operations/swarm.md) — Configuration and troubleshooting
- [vigocli swarm](../cli/swarm.md) — CLI reference
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
