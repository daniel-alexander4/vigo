# Plan: Substrate lifecycle unification (cairn + sigil)

*Drives from `substrate-audit.md`. Supersedes `~/.claude/plans/bubbly-wibbling-metcalfe.md`.*

## Context

The audit identified six gap rows between cairn and sigil — authoring, self-leave, admin evict, status, members, purge — plus a retire-hook blocked on a missing prereq verb. Shipping any one of these as a substrate-specific one-off would bake asymmetry in; shipping them all as one monolithic change is too big. The plan splits the work into four phases, each independently shippable, ordered so earlier phases don't lock in decisions that constrain later ones.

Phase A is cairn-specific authoring with no trust-model implications, so it runs first and alone. Phase B is the cross-substrate lifecycle design where vocabulary gets locked in — nothing further can ship until its naming is decided. Phase C fills in the remaining symmetric rows on the vocabulary Phase B establishes. Phase D is the `vigocli envoys retire` prerequisite + both substrates' hooks.

This plan details **Phase A only**. Phases B/C/D are sketched at the end and will each get their own detailed plan when reached.

## Phase A — cairn source CLI

**Goal:** make the CLI the sole authoring surface for cairn sidecars. Users never hand-edit `.cairn.yaml`. File stays as the on-disk storage format because it's portable across clones and already consumed by the reactor.

### Verbs

```
vigo cairn source add <path>     [--label L] [--max-mirrors N]
                                 [--tags a,b] [--cross-subnet h1,h2]
                                 [--exclude glob]... [--include glob]...
                                 [--description "..."]
                                 [--no-gitignore]           # default: append .cairn.yaml to .gitignore
vigo cairn source remove <project>    # deletes sidecar; receiver mirrors untouched
vigo cairn source pause <project>     # sets paused: true in sidecar
vigo cairn source resume <project>    # clears paused
vigo cairn source show <project>      # pretty-print sidecar + validation state
vigo cairn source validate <path>     # dry-run parse + schema check; no write
vigo cairn source list                # (already exists; extend output with paused column)
```

All verbs resolve `<project>` as `vigo cairn <verb>` today does — by label, by dir basename, by full path.

### Core behavior

- **`add`**: requires `<path>` to be an existing git repo (has `.git/`). Refuses if a `.cairn.yaml` already exists (points user at `edit` flow via `remove` then `add`, or `source show` to inspect). Writes sidecar with validated fields. Asks about `.gitignore` vs commit if neither flag passed.
- **`remove`**: deletes the sidecar, confirms interactively unless `--yes`. Does NOT purge receiver-side mirrors of the project — other envoys continue holding their copies until a separate purge step (Phase C).
- **`pause` / `resume`**: flip a `paused: true` field. The reactor (`agent/src/cairn/reactor.rs`) already has a paused-state concept per pending.md's `vigo cairn source list` output ("Paused" column); verify this field is honored or wire it up.
- **`show`**: reads + validates + pretty-prints. Surfaces any schema errors the reactor would log on next tick.
- **`validate`**: takes a path to a proposed sidecar file (not necessarily written yet). Returns 0 / non-zero. Useful for CI or templating flows.

### Gitignore hygiene

Default behavior at `add` time: append `.cairn.yaml` to the repo's `.gitignore` (create the file if absent). This keeps cairn enrollment personal to this clone — a shared-upstream repo never sees an individual user's cairn config leak in a PR. Idempotent: if `.cairn.yaml` is already gitignored, no second entry is written.

`--no-gitignore` suppresses the gitignore edit entirely. Use it when you want the sidecar to ride along with clones (personal repos across your own envoys) or when the repo's gitignore strategy is externally managed. The CLI never runs `git add` or `git commit` — tracking the sidecar is the user's explicit next action.

Rationale for the default: cairn is personal-DR. Most users will have a mix of solo repos (where inherited enrollment is fine) and contributed repos (where committing a personal sidecar is wrong). Gitignore-by-default picks the safer of the two — reenrolling after a fresh clone is one command, leaking personal state into a shared repo requires a PR to fix.

### Schema source of truth

Reuse the existing sidecar parser/validator under `agent/src/cairn/sidecar.rs` (or wherever sidecar schema lives — verify at impl time). The CLI does NOT invent a second schema. Adding a field to the sidecar means adding one struct field + one CLI flag in the same commit; no drift possible.

### Non-goals for Phase A

- No lifecycle verbs (leave / evict / purge) — those are Phase B/C.
- No cross-substrate symmetry with sigil verbs — sigil has no "sources" concept; shape-difference per the audit.
- No admin vigocli surface for source authoring. Author-state is user-level; admin visibility of what's published lives on the existing `/cairn` fleet UI via trait projection, no new CLI.
- No migration tool for existing hand-written sidecars. Existing `.cairn.yaml` files are valid as-is; the CLI just offers a cleaner authoring path going forward.

### Critical files

- **New**: `agent/src/cairn/source_cli.rs` — verb dispatcher mirroring `agent/src/sigil/cli.rs` shape.
- **Modified**: `agent/src/cli.rs` (or wherever top-level `vigo cairn` routes) — add `source` subverbs.
- **Modified**: `agent/src/cairn/sidecar.rs` — expose public write API (`write_sidecar`, `read_and_validate`) if not already public.
- **Modified**: `agent/src/cairn/reactor.rs` — confirm `paused: true` short-circuits the publish path (wire up if missing).
- **Modified**: `docs/reference/cli/agent-cairn.md` — document all source verbs; remove "not yet implemented" note from `source show`.
- **Modified**: `docs/engineering/subsystems/cairn.md` — note that sidecar authoring is CLI-driven; hand-editing still works but is not the supported path.

### Existing utilities to reuse

- `agent/src/cairn/sidecar.rs` parser/validator — no duplication.
- `agent/src/cairn/reactor.rs` label resolution — same label-vs-path resolution logic used by existing verbs.
- `agent/src/sigil/cli.rs` as structural template — same verb-dispatcher shape, same error-formatting patterns.
- Catppuccin Mocha palette from `cli/format.go` (or agent-side equivalent) for any prompted output.

### Versioning

- Bump `VERSION` + `agent/Cargo.toml` together (per `feedback_bump_every_agent_change.md`). 0.30.13 → 0.30.14.
- Agent-only change; no server change, no proto change.
- Cross-compile + redeploy agents; mixed-version fleets degrade gracefully (old agents keep honoring hand-written sidecars).

### Verification

- **Unit tests** (`agent/src/cairn/source_cli/tests.rs`): each verb's happy path + error paths (add to non-repo, add with existing sidecar, remove nonexistent, validate bad YAML, validate missing required field).
- **E2E on danlap**:
  1. `mkdir -p ~/cairn/testproj && cd ~/cairn/testproj && git init`
  2. `vigo cairn source add .` — prompts gitignore, writes sidecar, reactor picks up within 5s.
  3. `vigo cairn source show testproj` — prints config.
  4. `vigo cairn source pause testproj` — reactor stops publishing new bundles (verify via `vigo cairn source list` Status column).
  5. `vigo cairn source resume testproj` — publishing resumes.
  6. `vigo cairn source validate /tmp/invalid.yaml` (handcrafted bad file) — non-zero exit + readable error.
  7. `vigo cairn source remove testproj` — sidecar gone; next reactor tick drops the source; mirrors on peers remain until their own prune.
- **Unprivileged test** (per `feedback_test_user_cli_unprivileged.md`): run all verbs as the unprivileged user, confirm 0600 sidecar mode when written, owner matches invoking user.

### Acceptance criteria

- [ ] All seven source verbs work and are documented.
- [ ] Hand-editing `.cairn.yaml` still works (sidecar file unchanged as wire format).
- [ ] `paused: true` actually short-circuits the publish loop in the reactor.
- [ ] CLI writes 0600 sidecars owned by the invoking user.
- [ ] Unit tests cover happy + error paths.
- [ ] Docs updated (`docs/reference/cli/agent-cairn.md`, `docs/engineering/subsystems/cairn.md`).

## Phases B/C/D (sketch — detailed plans to follow)

### Phase B — Unified leave/evict vocabulary (both substrates)

Design pass first (no code): lock in verb names, flag shapes, trust model. Then implement.

- **`vigo <sub> leave`** (self-action):
  - sigil: already designed in the superseded plan — signed LEAVE envelope, peer-table gossip, local scrub.
  - cairn: needs design. Cairn "leave" has ambiguous scope (stop publishing? stop receiving? both?). The right answer is probably "opt out of cairn on this envoy for this user" = remove all sidecars the user authored here + flag receiver state to stop accepting new mirrors for this user. Needs Dan discussion before drafting.
- **`vigocli <sub> evict --host <h>`** (admin):
  - sigil: designed — task-dispatch to enrolled envoys, local forget + reencrypt queue.
  - cairn: evict = remove host from `selected_mirrors` computation fleet-wide, purge receiver mirrors sourced from that host on other envoys, stop considering the evicted host for future mirror selection. Needs design.

Expected split: ~2 weeks of focused work across both substrates once vocabulary is locked.

### Phase C — Fill symmetric rows

- `vigo cairn status` (parallel to sigil).
- `vigocli cairn members` + `vigocli sigil members`.
- `vigo cairn purge` (distinct from `prune`; full-wipe local state) + `vigo sigil purge`.

Cleanup phase; small per-item. ~1 week.

### Phase D — Retire-hook

Prereq: design + ship `vigocli envoys retire <host>` as a general envoy-lifecycle verb. When present, cairn + sigil both hook it for automatic `--all-users` eviction.

Separate engineering; retire is its own prereq work independent of this audit's scope.

## Out of scope (plan-wide)

- **Echo and lode** — per audit, both are shape-different and outside the unification effort.
- **Web UI lifecycle buttons** — CLI first, UI parity follows in a separate uiux pass once the CLI vocabulary stabilizes.
- **Windows parity** — Linux + macOS ship first per the 2026-04-20 Linux-first doctrine.
