# ADR-002: First-match-wins hostcrate lookup

**Status:** Accepted
**Date:** 2025-01-01 (retroactive)
**Updated 2026-04-12:** Terminology refresh — "node" → "envoy", "node mapping" → "match block / hostcrate". Original decision unchanged.

## Context

Envoys need to be mapped to roles and modules based on their hostname. The mapping system needs to handle wildcards/globs and be predictable when multiple patterns could match.

## Decision

Hostcrate lookup uses first-match-wins: the server walks `.vgo` files with `match:` blocks in definition order, and the first match block whose pattern matches the hostname wins. No merging of multiple matches.

## Consequences

- Behavior is predictable and easy to reason about — put specific patterns before general ones
- Operators can create catch-all defaults at the bottom
- No surprising interactions between overlapping patterns
- Trade-off: you can't layer multiple matching blocks. If an envoy needs traits from two groups, create a role that composes them.
- Directory inheritance (`common.vgo`) provides the layering mechanism where multiple configs contribute to a single envoy

## Alternatives considered

- **All-matches-merge** — rejected because merge order becomes ambiguous and debugging "where did this var come from?" gets hard
- **Priority/weight system** — rejected for complexity; definition order is simpler and just as powerful
