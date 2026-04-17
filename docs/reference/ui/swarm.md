
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

1. **Chunk distribution heatmap** — SVG grid showing which chunks each envoy has
2. **Peer topology graph** — D3 force-directed graph showing chunk transfer relationships
3. **Distribution timeline** — cumulative download curves per envoy
4. **Per-envoy source strips** — mini heatmap colored by source peer. When per-chunk detail is unavailable (e.g., cached/resurrected blobs), source colors are synthesized proportionally from the aggregate source breakdown so the strip shows the correct distribution ratio instead of all-grey

When more than 50 envoys participate in a distribution, visualizations are capped to the 50 most interesting envoys (downloading/pending first) for readability. The full envoy table remains uncapped.

#### Per-envoy visual identity

Every envoy gets a deterministic **color + shape + dash pattern** hashed from its hostname, so the same envoy looks identical across every viz section and across page refreshes. The three dimensions scale past the point where any single dimension saturates:

| Dimension | Palette | Visible in |
|---|---|---|
| **Color** | 14 Catppuccin Mocha accents | every section (heatmap row labels, topology dot fill, timeline polyline stroke, source strip cells, all legends) |
| **Shape** | 5 symbols (circle, square, triangle, diamond, star) | Peer Topology dot fills |
| **Dash** | 4 patterns (solid, dashed, dotted, dash-dot) | Distribution Timeline polylines |

All three are always present — there's no "switch mode at N envoys" logic, so the visual grammar is identical whether the fleet has 5 envoys or 500. Combined capacity is 14 × 5 × 4 = 280 unique visual identities, which comfortably exceeds any plausible fleet size where per-envoy coloring still makes sense.

The **stroke color** on topology dots is a separate dimension, driven by the envoy's convergence status (complete/downloading/pending/skipped) — so a single dot tells you both "which envoy this is" (fill color + shape) and "what state it's in" (ring color).

Hash-based assignment uses FNV-32a with different bit ranges per dimension so the three dimensions are uncorrelated — two envoys that happen to collide on color almost always differ on shape or dash, so they're still distinguishable in the topology and timeline views.

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How swarm works
- [Swarm Operations](../../operations/swarm.md) — Configuration and troubleshooting
- [vigocli swarm](../cli/swarm.md) — CLI reference
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
