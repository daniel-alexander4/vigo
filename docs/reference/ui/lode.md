
# Lode Page

**Route:** `/lode`

Displays all admin-distributed payloads ("lode") with distribution progress across the fleet. The payloads ride the [swarm](../../engineering/subsystems/swarm.md) substrate (peer-to-peer mTLS transport on port 1531); this page is the operator-facing surface for managing and observing those distributions. Auto-refreshes every 5 seconds on the detail view.

**Cairn bundles are excluded.** Personal-DR repo mirrors (labels prefixed with `cairn:`) have their own page at `/cairn` — see the [cairn subsystem docs](../../engineering/subsystems/cairn.md) — and are filtered out of Lode. Pasting a cairn blob sha into `/lode/{sha}` redirects to `/cairn/{label}`.

## List View

Page opens with a health banner surfacing active delivery problems (e.g., envoys that matched a target but couldn't receive). Headline reads "N payload(s) active, M revoked. No delivery failures." when clean.

Four stat cards follow: Total Files, Total Size, Active Distributions, Seeding Envoys.

The payload table columns:

| Column | Description |
|--------|-------------|
| Status | Color-coded pill: `complete` (green), `distributing` (yellow), `cached` (blue), `revoked` (gray) |
| Name | The payload's label (original filename) as a bold link to the detail view. A dim second line shows the SHA256 short form (full value on hover), the seeder hostname, and the chunk count. |
| Size | Human-readable payload size |
| Distribution | Status pill counts: source / complete / cached / downloading / pending / skipped (active blobs), or revoking / revoked (revoked blobs — in-flight and fully-evicted envoy counts). The total excludes the source — it counts only target envoys. `cached` (blue pill) means the envoy already had the blob from a previous distribution. |
| Created | When the payload was uploaded or seeded |

## Detail View

**Route:** `/lode/{sha256}`

Shows a single blob with per-envoy distribution status. Auto-refreshes via HTMX every 5 seconds.

### Chunk Flow Matrix

Target × source grid of chunk counts with a per-envoy row trailer on the right and a column-total footer row at the bottom.

- Each row is a receiving envoy; each column is a peer that served it.
- Cell values are the chunks transferred from source (column) to target (row), with background intensity scaled from the source's color so a sweep down any column reads as "this source's footprint across the fleet." The diagonal is always blank.
- **Row trailer** (right of the cells): status pill, `N/M` chunk progress (with `+X cached` suffix when the envoy had chunks from a previous distribution), and a duration bar with human-readable time. The trailer answers "what's going on with this envoy?" for rows the matrix grid can't explain on its own — cached downloads produce no cells, skipped envoys produce no cells, and the matrix alone can't tell a pending row from a fully-complete one.
- **Column totals** (`served` footer row): total chunks each source contributed fleet-wide.
- Pure HTML/CSS — no JavaScript.

When more than 24 envoys participate in a distribution, visualizations are capped to the 24 alphabetically-first envoys (matching the size of the tiered color palette). A "showing 24 of N envoys" notice appears next to the status pills when the cap is active.

### Per-envoy visual identity

Every envoy gets a deterministic hex color assigned by alphabetic slot in a 24-slot tiered palette so the same envoy looks identical across every viz section and across page refreshes:

- **Slots 0–7** — Catppuccin Mocha accents (Yellow, Red, Green, Blue, Peach, Mauve, Teal, Pink)
- **Slots 8–15** — Catppuccin Latte (deeper/saturated variants of the same eight hues)
- **Slots 16–23** — Catppuccin Mocha again; the fleet is large enough that perfect uniqueness has already broken down, and row labels remain readable

Slot assignment is alphabetic by hostname, so an envoy keeps the same color regardless of its status at render time. Fleets beyond 24 envoys drop the excess from the visualizations entirely rather than collapsing to a fallback grey — legend saturation is a harder failure than omission. The full envoy table above the visualizations still lists every envoy.

## Related

- [Swarm Concepts](../../engineering/subsystems/swarm.md) — How swarm works
- [Swarm Operations](../../operations/swarm.md) — Configuration and troubleshooting
- [vigocli lode](../cli/lode.md) — CLI reference
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
