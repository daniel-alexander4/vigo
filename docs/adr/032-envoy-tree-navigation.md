---
title: "ADR-032: Envoy tree navigation — directory-hierarchy view alongside the flat envoys table"
---

# ADR-032: Envoy tree navigation — directory-hierarchy view alongside the flat envoys table

**Status:** Approved — implementation in this commit (slice 1).
**Date:** 2026-05-24. Direction ratified by Dan in the same session.

## Context

The flat `/envoys` table is the canonical envoy view today: paginated, sortable, four filter axes (convergence, drift, reachability, hostname search). It works well for fleets up to low-thousands. Past that point, operators need a way to *navigate* the fleet rather than scroll through it — to drill into "all envoys in `dc1/web/`," to understand which slice of the config matched which envoys, to triage problems by config-inheritance branch instead of by hostname alphabet.

The pending entry's framing was direct: *"In order for UI to support a massive number of envoys, we need to be able to traverse it like a json tree according to directory structure and then filter based on the various cross axes that exist."* The directory structure in question is `.live/` — the published config tree the existing `/fleet/config-tree` page already renders. Envoys aren't naturally organized by hostname; they're organized by which hostcrate file matched them, which lives at a deterministic path under `.live/`.

Four design questions to settle before any code:

1. **Tree definition.** Reuse the config-tree, or invent a separate envoy-tree?
2. **Navigation pattern.** Replace `/envoys`, or coexist as `/envoys/tree`?
3. **Filter UX.** Faceted sidebar, URL-param chips, or both?
4. **Detail panes.** Deep-link into the tree path, or own pages?

## Decision

### Shape — coexist, walk the same `.live/` filesystem, extend the chip pattern, click-through to existing detail

**1. Coexist, not replace.** `/envoys` stays as the flat-list canonical surface. New `/envoys/tree` is a sibling navigation aid for massive fleets. Same dataset (FleetIndex), two presentations. Operators with 50-envoy fleets keep the flat table without forcing a tree they don't need; operators with 5000+ envoys get the navigation aid without losing the at-a-glance status the flat view delivers. Replacing would optimize the wrong axis — the flat view is genuinely better for small fleets.

**2. Walk the `.live/` filesystem; envoys cluster by their hostcrate's `DirPath`.** The pending entry literally says "directory structure," and the directory structure already exists at `.live/` (rendered by `/fleet/config-tree`). The envoy-tree page walks the same `.live/` shape (reusing `ui/configtree.go::walkConfigTree`) and, at each directory node, lists the envoys whose matched hostcrate sits in that directory. `ConfigStore.MatchDirPaths()` is the existing accessor; for each `FleetIndex` envoy, `ConfigStore.Match(hostname)` returns the pattern, `MatchDirPaths()[pattern]` returns the directory. No new SoT — pure join.

Granularity is directory-level, not file-level. `dc1/web/envoys.vgo` and `dc1/web/envoys-canary.vgo` group together under `dc1/web/`. File-level grouping (separating canary from prod hostcrates that share a directory) is a slice-2 refinement; the directory-level grouping is what operators ask for first and is what the existing `MatchDirPaths()` returns.

Envoys with no matched hostcrate cluster under a synthetic `(unmatched)` node at root. Never silently dropped — an unmatched envoy is operator-visible signal that the stockpile has a gap.

**3. Extend the existing URL-param chip pattern; defer faceted sidebar.** The flat `/envoys` page uses URL-param chips (`?conv=converged&drift=changed&reach=offline&search=web`). The tree view uses the *same* chips so the UX is uniform between the two views; filtering on tree hides leaves (and the whole branches they're under) where no envoy survives the filter. New chip axes (tag, role, OS) are slice 2 of this work — adding them to both views together keeps the UX uniform.

A faceted sidebar would be a new UI pattern with sticky positioning and per-axis multi-select widgets — meaningfully more surface for marginal gain over the chip pattern operators already know. Rejected for v1.

**4. Detail page stays at `/envoys/{id}`; click-through navigates there.** An envoy in the tree is a clickable link to the existing detail page. No in-page detail pane, no slide-out drawer. The detail surface (the 6-tab `/envoys/{id}` page) is already the SoT for envoy-level investigation; duplicating it inside the tree page would double the maintenance cost and surface drift.

**Tree-expansion state in `localStorage`, not URL.** Operators expanding 30 branches deep don't want a 2KB URL. Bookmarking the tree page should land on it with the last-known expansion remembered. URL parameters stay reserved for filter state (chip values + search) so bookmarks of filtered views still work cleanly.

**Collapse-expand via browser-native `<details>`, not HTMX.** No round-trips, no JS. The existing `/fleet/config-tree` uses the same pattern. If a single branch grows past a few hundred envoys and the all-rendered-at-once cost becomes operator-visible, HTMX lazy-load per branch is the slice-3 lever — not v1 concern.

## Slicing

- **Slice 1 (this commit):** ADR + the `/envoys/tree` page with directory-hierarchy + envoy list + carry-over chips (conv/drift/reach/search). Click-through to detail.
- **Slice 2 (pending entry added below):** Tag/role/OS filter chips on BOTH `/envoys` flat and `/envoys/tree` (uniform UX), plus faceted hide-empty-branches behavior on tree.
- **Slice 3 (deferred until needed):** HTMX lazy-load per branch — only if a real fleet surfaces all-rendered-at-once cost.

## Consequences

- **Operators get a navigation aid for massive fleets** without losing the flat view's strengths at small scale.
- **Two `/envoys/*` surfaces share one filter model** — the chip pattern is already there; extending it keeps cognitive load flat.
- **No new SoT.** The join is `FleetIndex.AllEnvoys()` × `ConfigStore.Match()` × `ConfigStore.MatchDirPaths()` — all existing accessors. No new database table, no new query, no new index.
- **Doc pages with config-inheritance hierarchies** (e.g., `docs/concepts/multi-axis-config.md`) gain a natural visual companion: operators reading about hostcrate inheritance can flip to the tree view and see it.
- **Unmatched envoys become visible** — today they're scattered in the flat list; the `(unmatched)` synthetic node surfaces them as a triage bucket.

## Out of scope

- File-level grouping (separating multiple hostcrates in one directory) — directory-level is what `MatchDirPaths()` returns natively.
- Faceted sidebar UX — chip pattern extension is cheaper and consistent.
- In-page envoy detail pane — `/envoys/{id}` stays the detail SoT.
- HTMX lazy-load per branch — premature; ratify only when a real fleet exercises the cost.
- Tag/role/OS chip axes — slice 2 of this work, tracked separately.
