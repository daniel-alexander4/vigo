
# Swarm Page

**Route:** `/swarm`

Displays all swarm blobs — both server-uploaded and envoy-seeded — with distribution progress. Auto-refreshes every 5 seconds on the detail view.

## List View

Shows all blobs in a table with distribution summaries.

| Column | Description |
|--------|-------------|
| Label | Blob label (original filename), links to detail view |
| SHA256 | First 12 characters of the content hash |
| Source | `server` (uploaded via CLI) or seeder hostname (envoy-originated) |
| Size | Human-readable file size |
| Chunks | Number of 1 MiB chunks |
| Created | When the blob was uploaded or seeded |
| Distribution | Status pill counts: complete / downloading / pending / skipped |

Stat cards at the top show total blob count, total size, and active distributions (any blob with pending or downloading envoys).

## Detail View

**Route:** `/swarm/{sha256}`

Shows a single blob with per-envoy distribution status.

**Blob metadata:** SHA256, label, source (seeder hostname or "server"), retention, size, chunks, created time.

**Per-envoy table** (auto-refreshes via HTMX every 5 seconds):

| Column | Description |
|--------|-------------|
| Hostname | Envoy hostname (clickable — expands per-chunk detail) |
| Status | `complete`, `downloading`, `pending`, or `skipped` |
| Progress | Chunk progress bar with count (e.g., 31/47) |
| Time | Transfer duration from first report to completion |
| Sources | Where chunks came from — hostname-resolved counts (e.g., "danlap: 8 plex: 3 server: 1") |
| Skip Reason | Why the envoy was skipped (e.g., insufficient disk space) |

**Per-chunk detail** (click a row to expand): Shows each chunk with its source and download time in milliseconds. Displays as compact chips: `0:danlap:1531 45ms  1:plex:1531 52ms  ...`

## Related

- [Swarm Concepts](../../concepts/swarm.md) — How swarm works
- [Swarm Operations](../../operations/swarm.md) — Configuration and troubleshooting
- [vigocli swarm](../cli/swarm.md) — CLI reference
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
