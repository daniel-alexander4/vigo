# ADR-001: YAML-only config path

**Status:** Accepted  
**Date:** 2025-01-01 (retroactive)

## Context

Configuration management tools typically offer multiple config input paths: API writes, database records, CLI mutations, and files. This creates ambiguity about the source of truth and makes it hard to audit what changed, when, and why.

## Decision

Vigo has exactly one config path: edit YAML files in `/srv/vigo/stockpile/`, then publish to `stockpile/.live/` via `vigocli config publish`. The publish CLI calls the server's REST reload endpoint to activate the new config — reload is pull-based, not filesystem-watched.

No API endpoint, database record, CLI command, or UI action can write configuration. The filesystem is the single source of truth.

## Consequences

- Config is always diffable, git-trackable, and auditable
- No reconciliation between database state and file state
- `vigocli config publish` is the only mutation gate — it validates before syncing
- Operators can use any text editor, version control, or CI/CD pipeline they already have
- Trade-off: no "quick fix" via API — every change requires file access. This is intentional: config changes should be deliberate.

## Alternatives considered

- **API-writable config** — rejected because it creates two sources of truth and makes audit trails harder
- **Database-backed config with file export** — rejected because the database becomes the real source and files become stale copies
