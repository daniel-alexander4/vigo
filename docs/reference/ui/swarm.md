
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
| Distribution | Status pill counts: complete / downloading / pending / skipped (active blobs), or revoking / revoked (revoked blobs — in-flight and fully-evicted envoy counts) |

Stat cards at the top show total file count, total size, and active distributions (any file with pending or downloading envoys).

## Detail View

**Route:** `/swarm/{sha256}`

Shows a single blob with per-envoy distribution status.

**Blob metadata:** SHA256, label, source (seeder hostname), retention, size, chunks, created time.

**Per-envoy table** (auto-refreshes via HTMX every 5 seconds):

| Column | Description |
|--------|-------------|
| Hostname | Envoy hostname (clickable — expands per-chunk detail) |
| Status | `complete`, `downloading`, `pending`, `skipped`, `revoking`, or `revoked` (see [swarm CLI reference](../cli/swarm.md) for the revoke state machine) |
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
4. **Per-envoy source strips** — mini heatmap colored by source peer

When more than 50 envoys participate in a distribution, visualizations are capped to the 50 most interesting envoys (downloading/pending first) for readability. The full envoy table remains uncapped.

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How swarm works
- [Swarm Operations](../../operations/swarm.md) — Configuration and troubleshooting
- [vigocli swarm](../cli/swarm.md) — CLI reference
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
