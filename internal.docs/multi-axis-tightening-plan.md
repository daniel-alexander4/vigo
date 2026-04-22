# Multi-Axis Config Tightening — Implementation Plan

Companion to `multi-axis-audit.md`. The audit catalogs what exists; this plan sequences what to delete. Dictionaries are deferred (revisit in 3+ months with real data on inline-lookup counts).

**Goal:** reduce the number of "different ways to do the same thing" without sacrificing load-bearing flexibility. After this, each question in the [canonical rulebook](multi-axis-audit.md#canonical-rule-per-question) has exactly one answer.

## Scope — what goes

Five mechanisms targeted for removal, tiered by risk:

### Hard deletes (loud warning → fatal, two releases apart)

1. **`environment:` field on match blocks** — lightweight env tagging, redundant with nested subdirectories or tags.
2. **`environment_overrides:` on match blocks** — per-node env-specific vars, redundant with `environments.vgo` or nested subdirs.
3. **`vars_from:` external files** — shared var blocks, redundant with `common.vgo` at a parent directory.
4. **Conditional vars** (`when:`-guarded var values, field `conditionals:` on module vars) — redundant with lookup tables inside modules.
5. **Match vars** (`match:` with `when:`-guarded value list, fallback via `default:`) — redundant with lookup tables inside modules.
6. **`modules/users/` pattern** — superseded by usercrates (shipped 0.26.4).

### Soft warnings only — behavior stays, operator is nudged

7. **Raw `modules:` lists in match blocks with >3 modules** — nudge operators toward defining a role. Load succeeds; warning shows the match file + line. Genuine leaf exceptions (≤3 modules) keep working without complaint.

## Scope — what stays

Preserved because deletion would cause role-count explosion, per-host LOC inflation, or genuine loss of cross-cutting axes not covered by the remaining mechanisms:

- **Matchfiles, roles, usercrates, directory inheritance via common.vgo** — the structural mechanisms, kept intact.
- **`case:` on roles** — same role, different modules per platform. Deletion would split roles like `web-server` into `web-server-linux` + `web-server-windows`. Keep.
- **Platform-keyed and tag-keyed lookup tables** inside module fields. Compact, local, readable. Keep.
- **`when:` on resources** (conditional execution) — kept as-is.
- **Compliance attachment via both inline `compliance:` blocks and `compliance.vgo` directory-level** — documented canonical path (directory-level preferred for DRY) but both continue to work.

## Env model — decided

Env is **cross-cutting**, not hierarchical. A host's env is declared on the match block via the `environment:` field; directories organize by business axis (customer, site, tenant), not env. `environments.vgo` is retained as *the* way to declare env-specific modules + vars, env-only (not generalized to other tags). Rationale in `multi-axis-audit.md` under "Is environments.vgo just for environments?".

- `environment:` field on match blocks — **kept**
- `environments.vgo` at any directory level — **kept**
- Env-based subdirectory convention — **demoted** (not forbidden, just not canonical; docs stop recommending it)
- Generalizing environments.vgo to per-tag — **rejected**; roles handle per-tag module sets and lookup tables handle per-tag values.

## Phase sequencing — revised after baseline audit

Baseline audit (2026-04-22): live stockpile uses none of the targeted mechanisms; example-configs has one `environment:` reference only; test fixtures reference each mechanism once. Since there are zero affected operators, the warn-then-fatal two-release dance is over-engineered. Collapse to **single-release delete** with a release-note migration guide.

Each deletion is one mechanism at a time, tested green between. Bundled into one `feat(config)` commit at version 0.27.0.

### Phase 0 — Baseline audit (read-only, 30 min)

Before writing any warning code, grep the live stockpile to know what would warn.

Commands:
- `grep -rn 'environment:' /srv/vigo/stockpile/ | grep -v .live` — count + list every `environment:` field use
- `grep -rn 'environment_overrides:' /srv/vigo/stockpile/`
- `grep -rn 'vars_from:' /srv/vigo/stockpile/`
- `grep -rn 'conditionals:' /srv/vigo/stockpile/ | head` — conditional vars
- `grep -rn 'match:\s*$' /srv/vigo/stockpile/` — match-vars marker
- `find /srv/vigo/stockpile/modules/users -type f` — users pattern
- In example-configs too

Document the current count of each. If any mechanism is used heavily (say >10 uses), the Phase 1 warning will be loud — may want to migrate some in advance. If unused or rarely used, deletion is low-pain.

Output: brief note appended to this doc with counts per mechanism.

### Phase 1 — Deletions ship as 0.27.0 (replaces the warn phase)

Loader emits a non-fatal warning at every deprecation site. Warning includes:
- What mechanism is deprecated
- Where it was found (file + line)
- What to use instead
- Pointer to this plan or to the canonical-rulebook section of `multi-axis-config.md`

Implementation sites:

| Mechanism | File | Hook |
|---|---|---|
| `environment:` field | `server/config/loader.go` `loadEnvoysDir` matchfile parse | Check field presence post-YAML-decode |
| `environment_overrides:` | same | Check for non-empty map |
| `vars_from:` | same + `common.vgo` parse | Check for non-empty slice |
| Conditional vars | `server/config/loader.go` `loadModuleDefs` module parse | Check `ConditionalVars` field |
| Match vars | same | Check `MatchVars` field |
| `modules/users/*.vgo` | `server/config/loader.go` `loadModuleDefs` | Path-prefix check on every module |
| Raw `modules:` in match block (>3) | `loadEnvoysDir` match parse | Length check |

Warning channel: `slog.Warn` with structured fields (mechanism, path, line, canonical-path). Also surfaced via `GET /api/v1/config/warnings` endpoint for the web UI to render a banner on config publish.

Documentation updates in the same release:
- `docs/concepts/multi-axis-config.md` — rewrite with the canonical rulebook. One resolution-order diagram. Each deprecated mechanism gets a migration snippet.
- `docs/concepts/config-format.md` — sections for the deprecated mechanisms get a "deprecated, will be removed in 0.28.0" banner.
- Release notes — clear migration guide.

Version bump: 0.26.6 → 0.27.0 (new major since the warning behavior is a noticeable UX change, even though code compiles and runs).

### Phase 2 — Canonical docs only (same release as Phase 1, but called out separately)

Rewrite `docs/concepts/multi-axis-config.md` with:
- The canonical rulebook (one answer per question)
- Resolution-order diagram: ONE diagram showing module assignment → var resolution → conditional evaluation → usercrate auto-apply → exclusion filter
- Migration snippets for every deprecated mechanism
- "When to use which mechanism" decision tree

Update `docs/concepts/config-format.md` to match. Cross-reference from both.

### Phase 3 — Migrate Dan's own stockpile (operator-side, between Phase 1 and Phase 3)

Dan runs `vigocli config publish` against the live stockpile after Phase 1 ships, sees the warnings, migrates each one:
- Replace `environment:` fields with nested subdir moves (or tags)
- Replace `environment_overrides:` with environments.vgo (narrow use) or common.vgo
- Collapse `vars_from:` into parent common.vgo
- Replace conditional vars / match vars with lookup tables
- Migrate `modules/users/*.vgo` → `usercrates/` (already partly done in 0.26.5)
- Refactor any match block with >3 raw modules into a role

Validate each migration with `vigocli config trace <hostname>` before moving the next.

### Phase 4 — Flip to fatal (next release after migration, e.g. 0.28.0)

Same detection hooks in the loader, but `slog.Error` → `return fmt.Errorf(...)`. Load fails with the same descriptive message. Example configs scrubbed for deprecated mechanisms. Tests for the loader's rejection behavior.

Removed code paths:
- Parse logic for `environment:`, `environment_overrides:`, `vars_from:`, conditional vars, match vars
- Priority-chain merge steps that consumed those fields
- The implicit "users modules get special compliance inheritance" behavior if it was scoped to `modules/users/` (usercrate compliance inheritance replaces it)

Raw `modules:` in match blocks stays a warning indefinitely — there's no clean removal path for "an operator genuinely has one bespoke host."

Version bump: 0.27.x → 0.28.0.

### Phase 5 — `vigocli config trace` attribution (same release as Phase 4 or follow-up)

Already shows inheritance level + file origin for modules. Extend to show var origin per-value. Today `vigocli config trace web01` may show the final var values. Change to show each var with:
- Final value
- Source: module-default / common.vgo:<dir> / match-block / environments.vgo:<env> / usercrate

This makes the simplified chain debuggable at a glance.

Server-side: extend the trace struct to carry per-var provenance. Template rendering in `cli/configtrace.go`.

## Test plan

Per phase:

**Phase 1:**
- Unit tests in `server/config/loader_test.go` — each deprecation target: `TestWarnsOn<Mechanism>` that loads a stockpile with the deprecated field and asserts a warning fires. Use `testing/slog` or an injected logger.
- Integration test that `vigocli config publish` against a known-deprecated stockpile exits 0 but prints warnings.

**Phase 4:**
- Unit tests: `TestRejects<Mechanism>` — same fixtures as Phase 1, assert fatal error with expected message substring.
- Integration test: `vigocli config publish` exits non-zero on deprecated stockpile.
- Regression test: the cleaned stockpile still loads and every envoy's resolved module list matches pre-cleanup output.

Pre-existing `server/config/loader_test.go` tests stay green throughout. New tests added per phase are all-green on commit.

## Rollback plan

Phase 1 warnings are reversible: one commit removes the detection code, nothing downstream depends on them.

Phase 4 fatal-ing is harder to reverse because the parse code is removed. If an operator complains post-Phase-4 about a dropped feature, the revert is:
1. Restore the struct fields and YAML tags from git history
2. Restore the merge logic
3. Revert the `return error` to a warning

Do NOT remove the struct fields from types.go during Phase 4. Leave them present but unconsumed — that way a revert is a single-file change, not a types-package reverification.

Actually — reconsidering: leaving dead fields in types.go invites the exact "operator invents their own way" re-emergence. Better to remove cleanly and accept the harder revert. The Phase 1 → Phase 4 gap gives the operator cohort a full release cycle to migrate; if we reach Phase 4 without a revert trigger, we've earned the cleanup.

## Success criteria

After Phase 4 ships:
- `docs/concepts/multi-axis-config.md` has one section per question with one answer each
- `server/config/loader.go` has no parse path for the six deprecated mechanisms
- Dan's live stockpile + `example-configs/` reference none of them
- `vigocli config trace` attributes each value to a single source
- Loader LOC reduced (estimate: ~500 lines of var-resolution + parse-field code gone)

## Open decisions for Dan

1. **`environments.vgo` fate:** keep narrow (module-sets only, deprecate `vars:` section) or delete entirely?
2. **Phase 1 release target:** 0.27.0 or piggyback on the next bump?
3. **Phase 4 release target:** 0.28.0, or defer if design partners surface an edge case?
4. **Raw match-block `modules:` threshold:** warn at >3, or stricter (>1)?
5. **Soft-deprecate `roles/` directory (legacy multi-file roles)?** Canonical is `roles.vgo`. Same soft-warn treatment would make sense but is out of scope for this plan — flag as follow-up.

## What this plan does NOT include

- Dictionaries (deferred; revisit at 3-month milestone with inline-lookup counts)
- Directory-mirror-infrastructure convention (operator-side recommendation only; not enforced)
- Changes to executor behavior, trait collection, or convergence
- Agent-side changes (agent doesn't parse these fields; server resolves them pre-checkin)
- Changes to roles' `case:` or `includes:` semantics
- Changes to matchfile hostname globbing or first-match-wins rule
