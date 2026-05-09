# Plan: Substrate Rename to Descriptive Vocabulary

**Status:** approved 2026-04-26 via /discuss session.
**Target version:** 0.31.0 (rolls minor — refactor with no functional change but large vocabulary churn).
**Approach:** big-bang single commit, verification script committed alongside.

## Decisions ratified (from /discuss)

| # | Decision |
|---|----------|
| 1 | Rename substrate subsystems and module primitive to descriptive labels |
| 2 | `lode` → `payload` |
| 3 | `cairn` → `gitmirror` |
| 4 | `echo` → `longdrawer` |
| 5 | `sigil` → `lockbox` |
| 6 | `stones` → `modules` |
| 7 | Keep `swarm` umbrella; group all admin verbs under `vigocli swarm <sub>` |
| 8 | Mirror grouping everywhere: agent CLI, filesystem, REST, source tree |
| 9 | Keep stockpile / scaffolding / structure / worksite metaphor |
| 10 | Keep product brands (Connwaer, Sandgorgon, Scrier, Spanner, vigosrv/vigocli/vigo); hide Sandgorgon nav + page route in this release; preserve code for v2 |
| 11 | Big-bang single commit + comprehensive legacy-naming verification script |

## Naming map

| Legacy | New | Admin CLI | Agent CLI | Filesystem | REST | Multicast |
|--------|-----|-----------|-----------|-----------|------|-----------|
| lode | payload | `vigocli swarm swarmcast` | `vigo swarm swarmcast` | `/var/lib/vigo/swarm/swarmcast/` | `/api/v1/swarm/swarmcast/` | n/a |
| cairn | gitmirror | `vigocli swarm gitmirror` | `vigo swarm gitmirror` | `/var/lib/vigo/swarm/gitmirror/` | `/api/v1/swarm/gitmirror/` | n/a |
| echo | longdrawer | `vigocli swarm longdrawer` | `vigo swarm longdrawer` | `/var/lib/vigo/swarm/longdrawer/` | `/api/v1/swarm/longdrawer/` | 224.0.0.43:1532 (unchanged) |
| sigil | lockbox | `vigocli swarm lockbox` | `vigo swarm lockbox` | `/var/lib/vigo/swarm/lockbox/` | `/api/v1/swarm/lockbox/` | 224.0.0.44:1533 (unchanged) |
| stones | modules | n/a (config dir) | n/a | `worksite/stockpile/modules/` | n/a | n/a |

Multicast addresses, port 1531 (peer mTLS), and the swarm transport itself are unchanged. Only naming changes.

## Scope of changes

### Source tree
- `agent/src/lode/` → `agent/src/swarm/swarmcast/`
- `agent/src/cairn/` → `agent/src/swarm/gitmirror/`
- `agent/src/echo/` → `agent/src/swarm/longdrawer/`
- `agent/src/sigil/` → `agent/src/swarm/lockbox/`
- `agent/src/swarm/` (transport) — already at this path; submodule additions land underneath
- `agent/src/traits/{cairn,sigil,echo}.rs` → `agent/src/traits/{gitmirror,lockbox,longdrawer}.rs`
- `agent/src/vigorepo/` — delete entirely (Dan confirmed: "vigorepo should not exist anymore")
- Server-side packages: `server/cairnmesh/`, `server/sigil*/`, `server/echomesh/`, `server/lode*/` (whichever exist) → fold into `server/swarm/<sub>mesh/` or rename in place to match
- `cli/{lode,cairn,echo,sigil}.go` → `cli/swarm.go` with `cli/swarm_{payload,gitmirror,longdrawer,lockbox}.go` per subsystem
- `server/api/{lode,cairn,echo,sigil}.go` → `server/api/swarm_{payload,gitmirror,longdrawer,lockbox}.go`
- `server/db/queries/{cairn,sigil,echo,lode}.sql` → corresponding new names; regen with `make sqlc`
- `worksite/stockpile/stones/` → `worksite/stockpile/modules/`

### Code identifiers
- Rust: `mod sigil`, `use crate::cairn`, `Sigil*` types, `cairn_*` functions, etc. — every identifier referencing legacy names renames to the new vocabulary
- Go: package names, struct names, function names, route handlers, config field names
- Proto: any field/message/RPC names referencing the four legacy names
- SQL: table names, column names, index names

### Config keys (YAML)
- User executor: `sigil: true` → `lockbox: true`; `echo: ...` → `longdrawer: ...` (echo had no `true` flag — was opt-in by drop-in dir, but any references in matchfiles)
- Matchfile vars: `vars: { cairn: true }` → `vars: { gitmirror: true }`
- Sidecar: `.cairn.yaml` → `.gitmirror.yaml`; `cross_subnet_mirrors:` field name kept (still describes the field accurately)
- server.yaml: `swarm.seeding_envoys` kept; any subsystem-specific stanzas renamed

### Filesystem paths
- `/var/lib/vigo/lode/` → `/var/lib/vigo/swarm/swarmcast/`
- `/var/lib/vigo/cairn/` → `/var/lib/vigo/swarm/gitmirror/`
- `/var/lib/vigo/echo/` → `/var/lib/vigo/swarm/longdrawer/` (if it existed at top level)
- `/var/lib/vigo/sigil/` → `/var/lib/vigo/swarm/lockbox/`
- `~/.sigil/` → `~/.lockbox/` (per-user identity dir)
- `~/cairn/` → `~/gitmirror/` (per-user repo source dir)
- `~/echo/` → `~/longdrawer/` (per-user sync dir)
- `~/sigil/` → `~/lockbox/` (per-user encrypted sync dir)
- `/var/lib/vigo/swarm/` (transport state) — unchanged
- Sentinel files: `enroll.pending`, `leave.pending`, `reencrypt.pending` keep filenames inside the new dirs

### REST routes
- `/api/v1/cairn/fleet` → `/api/v1/swarm/gitmirror/fleet`
- `/api/v1/sigil/fleet` → `/api/v1/swarm/lockbox/fleet`
- `/api/v1/echo/fleet` → `/api/v1/swarm/longdrawer/fleet`
- `/api/v1/lode/...` → `/api/v1/swarm/swarmcast/...`
- Agent peer endpoints (port 1531): `/cairn/push/<label>` → `/swarm/gitmirror/push/<label>`; `/sigil/push/<user>/<name>` → `/swarm/lockbox/push/<user>/<name>`; `/sigil/leaves/<user>` → `/swarm/lockbox/leaves/<user>`; `/echo/announce` → `/swarm/longdrawer/announce`

### Web UI
- Page routes: `/cairn`, `/cairn/{label}`, `/lode`, `/lode/{sha256}`, `/sigil/...`, `/echo/...` → corresponding `/swarm/<sub>/...` paths (or whatever shape Dan prefers — inherited from REST)
- Templates: `cairn.tmpl`, `sigil.tmpl`, `lode.tmpl`, `echo.tmpl` → renamed
- HTMX fragments under `/ui/fragments/...` updated
- Nav menu: remove Sandgorgon item; rename four substrate links

### CLI verbs
- `vigocli lode {distribute,status,list,revoke,broken}` → `vigocli swarm swarmcast {distribute,status,list,revoke,broken}`
- `vigocli cairn {status,list,show,evict}` → `vigocli swarm gitmirror {status,list,show,evict}`
- `vigocli echo {status,list,show}` → `vigocli swarm longdrawer {status,list,show}`
- `vigocli sigil {status,list,show,evict}` → `vigocli swarm lockbox {status,list,show,evict}`
- `vigo cairn {recover,source add/remove/pause/resume/show/validate/list,leave}` → `vigo swarm gitmirror {recover,source ...,leave}`
- `vigo sigil {init,enroll,unlock,lock,encrypt,decrypt,forget,leave,reencrypt,status,_apply-evict}` → `vigo swarm lockbox {init,enroll,unlock,lock,encrypt,decrypt,forget,leave,reencrypt,status,_apply-evict}`
- Hidden agent verb `vigo cairn _apply-evict` (via TaskDispatch) → `vigo swarm gitmirror _apply-evict`

### DB schema
- Tables: `cairn_*`, `sigil_*`, etc. — rename via in-place migration (users == 0). Add a one-shot migration that DROPs old tables if present and CREATEs new ones; or rename tables via `ALTER TABLE`. Both work since there's no live data.
- Columns referencing legacy names renamed.
- sqlc regen after SQL changes.

### Trait fields
- Trait blob keys: `cairn`, `sigil`, `echo` → `gitmirror`, `lockbox`, `longdrawer`
- Server-side trait consumers (cairnmesh aggregator, etc.) follow

### Multicast / port log lines
- `multicast announcer started` log lines reference subsystem names — update
- Listener `listener started on 224.0.0.44:1533` log lines — update naming, keep numeric values

### Docs
- `docs/concepts/cairn.md`, `docs/concepts/echo.md`, `docs/concepts/sigil.md` → `docs/concepts/gitmirror.md`, `docs/concepts/longdrawer.md`, `docs/concepts/lockbox.md` (and a new `docs/concepts/payload.md` if lode lacks one — verify in implementation pass)
- `docs/cli/{cairn,echo,sigil,lode}.md` → corresponding `docs/cli/swarm-{sub}.md`
- `docs/cli/_index.md` updates
- `docs/api/` route docs
- `docs/concepts/_index.md`
- `docs/getting-started/server-config.md`
- ADR additions: write `docs/adr/NNNN-substrate-vocabulary-rename.md` documenting the decision

### Memory files
- `feedback_*.md` and `plan_*.md` referencing legacy names: update body text + frontmatter where the term is structural
- `MEMORY.md` index lines mentioning legacy names
- `codebase_swarm.md`, `codebase_echo.md` — content rewrite
- `pending.md`: prune Vigorepo plan entries (Dan confirmed vigorepo is being deleted), update remaining substrate references
- `completed.md`: leave historical entries unchanged (history reads as-it-was) — added to allowlist

### Examples
- `example-configs/stockpile/usercrates/dan.vgo.example` and any other `.example` files referencing `sigil:`, `cairn:`, `echo:` keys
- `server.yaml.example` references

### CLAUDE.md
- Architecture > Binaries section: rewrite swarm content-subsystem listing
- Build & Development Commands: cargo target dir mention is still valid
- Development Workflow: P2P descriptions
- Module System: `worksite/stockpile/stones/*.vgo` → `worksite/stockpile/modules/*.vgo`

## Execution order

### Phase 1 — agent (Rust)
1. Delete `agent/src/vigorepo/` entirely.
2. Move `agent/src/{lode,cairn,echo,sigil}/` → `agent/src/swarm/{payload,gitmirror,longdrawer,lockbox}/` with `git mv`.
3. Update Rust module decls (`mod foo`, `use crate::foo`, `pub mod foo`).
4. Rename Rust identifiers (types, functions, consts) per the naming map.
5. Update filesystem path constants in `agent/src/swarm/<sub>/config.rs` (or equivalent) and any cross-subsystem references.
6. Trait collectors: rename `agent/src/traits/{cairn,sigil,echo}.rs` → new names; update trait blob keys and `agent/src/traits/mod.rs`.
7. Agent CLI: relocate verbs under a new top-level `vigo swarm` Clap subcommand tree.
8. `cd agent && cargo check` until clean.
9. `cd agent && cargo build --release` proof.

### Phase 2 — server (Go)
1. Rename `server/cairnmesh/`, `server/sigil*/`, etc. (whichever directories exist for the substrate subsystems on the server side) into `server/swarm/<sub>mesh/` or equivalent.
2. Rename `server/api/{lode,cairn,echo,sigil}.go` and update REST handlers/routes.
3. Update the route table to mount under `/api/v1/swarm/<sub>/`.
4. Rename SQL queries in `server/db/queries/`; rename DB tables in migrations (in-place edit since users == 0).
5. Run `make sqlc` to regen Go bindings.
6. Run `make migrate` proof on a fresh DB.
7. Update proto file (`protobuf/vigo.proto`) if any field/message references legacy names; run `make proto`.

### Phase 3 — admin CLI (Go)
1. Consolidate `cli/{lode,cairn,echo,sigil}.go` under `cli/swarm.go` with per-subsystem files.
2. Update Cobra command tree to root the four under `vigocli swarm`.
3. Update help strings, flag descriptions.
4. `make build` proof.

### Phase 4 — web UI
1. Rename templates: `ui/templates/{cairn,echo,sigil,lode}*.tmpl` and corresponding fragments.
2. Update template handler routes (Go side).
3. Update nav layout: rename four substrate links, **remove Sandgorgon nav `<li>`**, drop `/sandgorgon/` route from the router.
4. HTMX `hx-get`/`hx-target` references updated.

### Phase 5 — config + modules
1. `git mv worksite/stockpile/stones worksite/stockpile/modules`.
2. Update config loader path constants.
3. Update YAML config keys (`sigil:`, `cairn:`, `echo:` → `lockbox:`, `gitmirror:`, `longdrawer:`) wherever they appear in code as recognized field names.
4. Update example configs and any `.vgo.example` files.
5. Sidecar rename: `.cairn.yaml` → `.gitmirror.yaml` everywhere code references it.

### Phase 6 — docs
1. Rename concept pages, CLI pages, API pages.
2. Sweep cross-references via grep.
3. Update CLAUDE.md.
4. Write the ADR documenting the decision.

### Phase 7 — memory
1. Update `MEMORY.md` index.
2. Rename relevant `plan_*.md`, `codebase_*.md`, `feedback_*.md` files.
3. Prune Vigorepo entries from `pending.md` and `MEMORY.md`.
4. Leave `completed.md` as historical record (allowlisted).

### Phase 8 — verification
1. Write `scripts/check-legacy-naming.sh` (see spec below).
2. Write the allowlist file `scripts/check-legacy-naming.allowlist`.
3. Run the script. Resolve every flag.
4. Re-run until clean.

### Phase 9 — build proofs and commit
1. `cd agent && cargo check && cargo clippy && cargo build --release`.
2. `make build`.
3. `make test`.
4. `make lint`.
5. `cd agent && cargo test`.
6. `scripts/check-legacy-naming.sh` clean.
7. Bump `VERSION` to **0.31.0**; bump `agent/Cargo.toml` to match; `cargo build` to refresh `Cargo.lock`.
8. Commit with evening (6PM-11:59PM Pacific) timestamp.

## Verification script: `scripts/check-legacy-naming.sh`

### Behavior
- Runs from repo root.
- Scans all tracked files (uses `git ls-files` so unrelated working-dir cruft doesn't trigger noise).
- File-type filter: `.go`, `.rs`, `.md`, `.vgo`, `.vgo.example`, `.example`, `.yaml`, `.yml`, `.sql`, `.proto`, `.html`, `.tmpl`, `.css`, `.js`, `.sh`, `.toml`, `Makefile`, `Dockerfile`, `Cargo.toml`, `Cargo.lock`, `go.mod`.
- Whole-word patterns to flag:
  - `\blode\b`
  - `\bcairn\b`
  - `\bsigil\b`
  - `\bstone\b`, `\bstones\b`
  - `\becho\b` — special-cased (see allowlist)
  - Path patterns: `/var/lib/vigo/lode`, `/var/lib/vigo/cairn`, `/var/lib/vigo/sigil`, `/var/lib/vigo/echo` (without the `swarm/` prefix)
  - REST patterns: `/api/v1/lode`, `/api/v1/cairn`, `/api/v1/sigil`, `/api/v1/echo` (without `/swarm/`)
  - User-dir patterns: `~/.sigil`, `~/cairn`, `~/sigil`, `~/echo` (and their resolved forms)
- Output format: `path:line:column: <snippet> — matched: <term>`.
- Exit 1 if any match outside the allowlist; exit 0 if clean.

### Allowlist (`scripts/check-legacy-naming.allowlist`)
- `completed.md` — historical record; all matches allowlisted.
- Shell `echo` builtin in `*.sh` files: matched lines starting with `\s*echo\s` are allowlisted, but `echo` as a path component, struct field, or quoted token is NOT (regex captures the difference).
- Specific lines Dan accepts as historical (e.g., commit-message-quoted text in docs that references the old vocabulary intentionally).
- Format: `path:line_number:term` for exact-match exclusions; `path:*:term` to allowlist a whole file.

### Implementation
- Pure bash + grep/awk; no extra deps.
- Sub-100 lines.
- Runs in <2s on the current repo.
- Add to `make lint` so future drift is caught at lint time.

## Risks and mitigations

| Risk | Mitigation |
|------|-----------|
| Existing lab envoys hold state at old paths (`/var/lib/vigo/cairn/`, `~/.sigil/`, etc.) | Document in commit message + agent restart procedure: stop agent, `mv` old paths to new under `/var/lib/vigo/swarm/<sub>/` (or rm-and-let-rebuild for ephemeral state like peer caches), restart. Manual one-shot per envoy. |
| Existing user data in `~/cairn/`, `~/sigil/`, `~/echo/`, `~/.sigil/` on Dan's machines | Manual `mv` per-user post-deploy. Document in commit message. |
| DB has old tables on the running server image | Migrations `DROP IF EXISTS old_name` before `CREATE`; users == 0 means no data loss concern. |
| Verification script false positives on the word "echo" | Allowlist shell builtin in `*.sh`; whole-word match avoids matching `cache`, `echo_chamber`, etc. |
| Rename misses a path string in a template | Verification script + post-rename smoke test (start server, click each substrate page, exec each CLI verb in dry-run mode). |
| `Cargo.lock` churn | Rebuild after Cargo.toml package rename if any sub-crate names change; otherwise no churn since the `vigo` crate name is unchanged. |
| Vigorepo deletion leaves orphaned references | Verification script catches any leftover `vigorepo` mention after directory deletion. |

## Commit message draft

```
refactor(rename): substrate subsystems and module primitive to descriptive names (0.31.0)

Substrate subsystems renamed for clarity:
  lode    → payload    (admin-pushed binary distribution)
  cairn   → gitmirror  (P2P git repo backup)
  echo    → longdrawer   (LAN plaintext per-user dir sync)
  sigil   → lockbox  (LAN encrypted per-user dir sync)
  stones  → modules    (config primitive)

All admin and agent verbs now grouped under `swarm`:
  vigocli swarm {payload,gitmirror,longdrawer,lockbox} ...
  vigo    swarm {payload,gitmirror,longdrawer,lockbox} ...

Symmetry across surfaces:
  /var/lib/vigo/swarm/<sub>/                       (filesystem state)
  /api/v1/swarm/<sub>/...                          (REST routes)
  agent/src/swarm/<sub>/                           (Rust source tree)
  worksite/stockpile/modules/                      (config primitive dir)

Sandgorgon nav menu entry and `/sandgorgon/` page route removed in this
release; server/CLI/DB code preserved for v2.

Vigorepo subsystem deleted (was being rearchitected; folded into the
gitmirror story).

scripts/check-legacy-naming.sh added; runs in `make lint` to prevent
vocabulary drift.

Migration notes for lab envoys are in the README — paths under
/var/lib/vigo/{lode,cairn,echo,sigil}/ and ~/{cairn,echo,sigil}/ need to
move to the new locations before the new agent starts.
```

## Out of scope (deliberately deferred)

- Renaming `stockpile/scaffolding/structure/worksite` directory metaphor (Item 9 — keep).
- Renaming `swarm` umbrella (Item 7 — keep).
- Renaming `Connwaer`, `Sandgorgon`, `Scrier`, `Spanner`, binary names (Item 10 — keep).
- DB-row data migration tooling for live envoys (no live envoys in scope; users == 0).
- Backwards-compat shims, aliases, or deprecation warnings (per `feedback_no_shims`).
