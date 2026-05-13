# Plan / record: swarm-subsystem rename (0.50.0)

**Status:** executed 2026-05-12 (via `/scrutinize` → approved by Dan).
**Target version:** 0.50.0 (rolls the minor — large vocabulary churn, no functional change).
**Approach:** big-bang single commit + plan record, verification script extended in the same commit. Mirrors the 0.31.0 substrate rename (`internal.docs/plan-substrate-rename.md`).

## Naming map

| Legacy | New | What it is | Admin CLI | Agent CLI | Filesystem | REST |
|--------|-----|------------|-----------|-----------|------------|------|
| `swarmcast` | `filecast` | admin-pushed fleet file distribution | `vigocli swarm filecast` | (n/a — admin push) | `/var/lib/vigo/swarm/filecast/` | `/api/v1/swarm/filecast/`, `/swarm/filecast/…` (peer, 1531) |
| `swarmgit` | `gitback` | git over swarm (ADR-015) | `vigocli swarm gitback` | `vigo swarm gitback {status,purge,project …}` | `~/.vigo-gitback/<project_id>/`, `/var/lib/vigo/swarm/gitback/projects/<pid>.git`, `/var/run/vigo/gitback.sock`, `~/gitback/<project>/` (per-user working clones) | `/api/v1/swarm/gitback/…`, `/swarm/gitback/{push,envelopes}/…` (peer, 1531) |
| `swarmresolve` | `hostdox` | opt-in agent-managed `/etc/hosts` block from peer `HOST:` gossip | — | `vigo swarm hostdox status` | managed `/etc/hosts` block | — |
| `swarmreg` | `curator` | planned P2P artifact registry (ADR-024 — doc-only so far) | `vigocli swarm curator` (planned) | `vigo swarm curator` (planned) | `/var/lib/vigo/artifacts/…` (planned) | `/api/v1/swarm/curator/…` (planned) |

Also renamed by these maps (substring of `swarmgit`): `swarmgitmesh` → `gitbackmesh`; `git-remote-swarmgit` → `git-remote-gitback`; `swarmgit://` → `gitback://`; the `swarm.swarmgit.enabled` / `swarm.swarmresolve` / `swarm.swarmcast.*` / `swarm.swarmreg.*` server.yaml keys; the usercrate `swarmgit:` / `swarmreg:` flags; the proto fields `swarmcast_max_size_bytes` / `swarmcast_default_retention` / `swarmgit_max_bundle_bytes` / `swarmgit_envelope_retention_secs` (→ `filecast_*` / `gitback_*`); the `.swarmgit.yaml` sidecar pattern (→ `.gitback.yaml`).

**Unchanged:** the `swarm` umbrella itself, `agent/src/swarm/`, `server/swarm/`, `/api/v1/swarm/…`, `vigo swarm <sub>` / `vigocli swarm <sub>`, `swarmview`, the sibling subsystems `longdrawer` / `lockbox` / `puddle`, multicast addresses, port 1531, and the swarm transport. Also unchanged: Dan's `~/swarmgit/{vigo,tessera,vigo-web}` dev-repo directory — that's a coincidence of the 0.31.0 `~/cairn/` → `~/swarmgit/` move, not the subsystem; the `gitback` subsystem's own per-user working-clone convention is `~/gitback/<project>/` in code.

## Deferred: the `anchor` extraction

`gitback` and the planned `curator` share an underlying primitive — the **puddle-rooted ownership/access layer**: `resource_id = sha256(founder_puddle_pubkey ‖ name)`, the stack of Ed25519 capability tokens (membership/roles), the founder-rekey delegation chain (ADR-019), the friendly-name claims (ADR-022), the signed-envelope gossip (revokes/leaves/claims), and the root-owned helper-daemon pattern. Today it lives intermingled with the git-specific code in `agent/src/swarm/gitback/` (`tokens.rs`, `rekey.rs`, `envelopes.rs`, `pubkey_bootstrap.rs`, `pending_claims.rs`, etc. are ~pure; `cli.rs`, `helper.rs`, `project_state.rs`, `mod.rs` interleave).

Its agreed name is **`anchor`** (the codebase already says "anchored to the founder's puddle pubkey", `project reanchor`, etc.). It was **not** extracted in this rename — that's a follow-up tied to curator's ADR-024 slice 1, when there's a second consumer to validate the seam against. Until then it rides inside `gitback/` and ADR-024 prose refers to it as "gitback's machinery"; that wording flips to "the `anchor` layer" when the extraction lands. See `pending.md`.

## Scope of changes (this commit)

- **File/dir renames** (`git mv`): `agent/src/swarm/swarmgit/` → `gitback/`; `agent/src/swarm/swarmresolve.rs` → `hostdox.rs`; `agent/src/traits/swarmgit.rs` → `gitback.rs`; `agent/tests/swarmgit_helper.rs` → `gitback_helper.rs`; `cli/swarm_{swarmcast,swarmgit}.go` (+`_test`) → `swarm_{filecast,gitback}.go`; `server/api/swarm_{swarmcast,swarmgit}{,_test}.go` → `swarm_{filecast,gitback}{,_test}.go`; `server/swarm/swarmgitmesh/` → `gitbackmesh/` (+ files inside); `ui/swarm_{swarmcast,swarmgit}.go` → `swarm_{filecast,gitback}.go`; `ui/templates/pages/{swarmcast,swarmgit}.html` → `{filecast,gitback}.html`; ADRs 013/015/019/022/023 slug `swarmgit` → `gitback`, ADR 024 `swarmreg` → `curator`; `docs/engineering/subsystems/swarmgit.md` → `gitback.md`; `docs/reference/cli/{agent-swarm-swarmgit,swarm-swarmcast,swarm-swarmgit}.md` → `{agent-swarm-gitback,swarm-filecast,swarm-gitback}.md`; `docs/reference/traits/swarmgit.md` → `gitback.md`; `docs/reference/ui/swarm-swarmcast.md` → `swarm-filecast.md`; `scripts/purge-swarmgit.sh` → `purge-gitback.sh`.
- **Content rename**: case-aware token substitution (`swarmcast`/`Swarmcast`/`SWARMCAST` → `filecast`/…, same for the other three) across all tracked files **except** the historical record `internal.docs/plan-substrate-rename.md`, the verification script + allowlist (rewritten by hand), and the four `~/swarmgit/{tessera,vigo-web}` dev-dir lines in `CLAUDE.md` (restored after the global pass). Covers Rust, Go, proto field names, server.yaml.example keys, usercrate `.vgo.example` flags, REST routes, HTMX/templates, docs, ADRs, glossary, `.gitignore`, the Makefile comment.
- **Codegen**: `make proto` (regenerates `generated.code/proto/vigo/*.pb.go` after the proto field renames; the Rust side is `build.rs`-generated, picked up by `cargo build`).
- **Verification**: `scripts/check-legacy-naming.sh` extended with the new tokens + legacy paths (REST `/api/v1/swarm/swarm*/`, FS `/var/lib/vigo/swarm/swarm*`, `agent/src/swarm/swarmgit/`, `server/swarm/swarmgitmesh`, `git-remote-swarmgit`, `swarmgit://`, `~/.vigo-swarmgit`); `scripts/check-legacy-naming.allowlist` gains the 4 `CLAUDE.md` dev-dir lines + this plan doc (whole-file). Wired into `make lint` already.
- **Version**: `VERSION` + `agent/Cargo.toml` + `agent/Cargo.lock` → `0.50.0`.
- **Memory**: `MEMORY.md` index, `pending.md` (adds the `anchor`-extraction follow-up), and `reference_*` / `feedback_*` / `project_*` files mentioning the old tokens.

## Out of scope (deliberately deferred)

- Extracting `agent/src/swarm/anchor/` from gitback — follows curator's ADR-024 slice 1 (see above; tracked in `pending.md`).
- Moving Dan's `~/swarmgit/` dev-repo directory on disk — his call, independent of this commit.
- Renaming the `swarm` umbrella, `longdrawer` / `lockbox` / `puddle`, or the product brands (Connwaer, Scrier, Spanner, …) — not in scope.
- Rewriting the stale pre-ADR-015 `gitback` glossary entry (it describes the old bundle-sidecar model) — separate `/docs-review` pass.

## Lab-envoy migration notes (post-deploy)

The renamed agent writes/reads the new on-disk locations only. On each envoy after the new agent lands:
- per-user gitback state at `~/.vigo-gitback/` and root-owned bare repos at `/var/lib/vigo/swarm/gitback/projects/` are re-created from scratch — ADR-023 auto-re-adopts founded projects at `vigo swarm puddle unlock`; otherwise `scripts/purge-gitback.sh` clears stale state so the envoy re-adopts cleanly.
- the `git-remote-gitback` symlink is installed by the agent's bootstrap, which also removes the old `git-remote-swarmgit` symlink.
- filecast seed/cache under `/var/lib/vigo/swarm/swarmcast/` → `…/filecast/`: re-fetched on next distribute; the server-side bind-mounted seed dir + `swarm.filecast.*` config also moved.
- `gitback://<…>` URLs replace `swarmgit://<…>`; old remotes need re-pointing.
