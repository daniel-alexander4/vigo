# ADR-006: HTMX-first web UI

**Status:** Accepted  
**Date:** 2025-06-01 (retroactive)

## Context

The web UI needs to provide dashboards, operational views, and interactive features. The choice between SPA frameworks (React, Vue) and server-rendered approaches affects complexity, maintenance burden, and developer experience.

## Decision

Use Go templates + HTMX with `hx-boost="true"` for SPA-like navigation. Server renders HTML fragments, HTMX swaps them in. JavaScript only for features that genuinely cannot round-trip to the server (AI chat drawer, streaming responses, isowebauth flow).

## Consequences

- No build step for the frontend — templates are compiled into the binary
- No JavaScript framework to maintain, update, or debug
- Every UI interaction is a server endpoint returning HTML — easy to test, easy to reason about
- `hx-boost` gives SPA-like navigation without client-side routing
- Fragment pattern: `GET /ui/fragments/...` returns partial HTML for HTMX to swap
- Trade-off: complex client-side state (multi-step wizards, drag-and-drop) would be harder — but Vigo's UI doesn't need these
- Gotcha: inline `<script>` tags with `fetch()` break under `hx-boost` — scripts don't re-execute on boosted navigation. All data fetching must use HTMX attributes.

## Alternatives considered

- **React SPA** — rejected because it adds a build pipeline, node_modules, and a JavaScript ecosystem to maintain for a solo dev
- **Server-rendered without HTMX** — rejected because full page reloads feel sluggish; HTMX gives the interactivity without the complexity
