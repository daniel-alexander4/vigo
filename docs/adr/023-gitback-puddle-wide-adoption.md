---
title: "ADR-023: Puddle-wide gitback project adoption"
---

# ADR-023: Puddle-wide gitback project adoption

**Status:** Implemented in 0.49.0.
**Date:** 2026-05-11

## Context

A puddle (per [ADR-014](014-puddle-primitive.md)) is one user's collection of envoys treated as a single trust unit. The natural expectation for `dr_scope: swarm-wide` projects — today's default, the personal-DR case — is: "I found a repo on one of my envoys; my other envoys hold replicas of it."

That doesn't happen. `vigo swarm gitback project init <handle>` writes `~/.vigo-gitback/<project_id>/{project.json,tokens.json}` **only on the founding envoy**. The receiver-side DR catch-up reactor ([0.46.0](../engineering/subsystems/gitback.md)) backfills a bare repo for projects an envoy *already has local state for* but no bare repo — so a freshly-`join`ed *member fleet* catches up, but the founder's *own other envoys* never get local state and so the reactor never fires for them. The LAN announce (`GITBACKPROJECT:<host>:<pid_hex>`) carries only host + project_id — no handle, no founder pubkey — so an envoy seeing it can't reconstruct the project (it needs the handle to mint a token body, and to verify `sha256(its_puddle_pubkey ‖ handle) == pid`). Result: `vigocli swarm gitback list` shows `HOSTS 1` on every project until the operator manually creates state on each envoy — and 0.48.0's one-shot `project init` makes the *founder* side smooth but doesn't change this.

The fundamental tension: **adopting a project requires signing with the puddle key** (only the user's session helper, running as the user, holds the unwrapped Ed25519 key) **but discovering which projects to adopt requires the swarm announce index** (only the root agent has it). Adoption needs a handoff between the two.

## Decision

A puddle member envoy **auto-adopts the projects its puddle founded** at `vigo swarm puddle unlock` time. Three moving parts:

1. **Widen the announce.** `GITBACKPROJECT:<host>:<pid_hex>` → `GITBACKPROJECT:<host>:<pid_hex>:<handle>:<dr_scope>`. The handle is what an adopting envoy needs to mint the bootstrap token and to verify `sha256(its_puddle_pubkey ‖ handle) == pid`; `dr_scope` (`swarm-wide`|`members-only`) is carried so the adopted `project.json` doesn't drift from the founder's intent. The parser tolerates the old 3-field form (`handle = None`, `dr_scope` defaults) for a rolling agent upgrade; the emitter always sends the full form. Both `<handle>` and `<dr_scope>` are colon-free by construction (handle = ASCII alnum + `-_.`; dr_scope is a fixed token), so no escaping.

2. **A world-readable announced-projects cache.** The root agent maintains `/var/lib/vigo/swarm/puddle/gitback-announced.json` — `[{project_id, handle, dr_scope}]` of projects it's currently seeing announced — flushed once per catch-up-reactor tick (~45 s) from `project_peers::global()`. Co-located with `names.json` in the 0755 `puddle_state_dir()`; 0644. Project IDs and handles are public-by-construction (they're the `gitback://<pid>/<handle>` URL form, shared freely; `GITBACKPROJECT:` lines are cleartext multicast already), so a world-readable cache leaks nothing. This is the handoff from the root agent to the user-side `puddle unlock`.

3. **An adoption pass in `puddle unlock`.** After `session::spawn_helper(...)` succeeds — the only moment that runs *as the user* with a freshly-usable signing key, and a step the user runs routinely; it already does the analogous lockbox bootstrap there — `cmd_unlock` reads `gitback-announced.json`, resolves the local puddle pubkey, and for each `(pid, handle, dr_scope)` where `sha256(local_puddle_pubkey ‖ handle) == pid` **and** `~/.vigo-gitback/<pid>/` doesn't already exist, mints the founder-bootstrap state (`project.json` + `tokens.json`, role Admin, `members: [self]`, the announced `dr_scope`) — the same thing `project init` does, factored into a shared helper. The existing catch-up reactor then pulls the bare repo within ~45 s. Failures surface but don't undo the unlock (same pattern as the lockbox follow-up).

End state: `vigo swarm puddle join --from <founder-host>` then `vigo swarm puddle unlock` on a new envoy → it holds every repo your puddle founded within a couple of reactor ticks, with zero per-project commands.

**Why `unlock`, not a root reactor or a new verb.** A root reactor can't sign with the user's puddle key. A new verb is one more thing to remember. `puddle unlock` is the unique spot that (a) runs as the user, (b) has just spawned the session helper, and (c) is run routinely — and the lockbox-bootstrap-on-unlock precedent shows the shape works.

**Why not carry the bootstrap token over the swarm instead of re-minting.** The bootstrap is a self-token (`signer == subject == puddle_pubkey`); every envoy in the puddle would mint an equivalent one (modulo `issued_at`). Re-minting locally is simpler than gossiping `tokens.json` (which isn't gossiped today) and avoids a chicken-and-egg (the catch-up bundle pull needs a token chain in its `X-Vigo-Chain` header — re-minting gives it one).

## Consequences / tradeoffs

- **Founder-key-current only.** Auto-adoption matches `sha256(local_puddle_pubkey ‖ handle) == pid` — i.e. projects founded by the puddle's *current* key. After a founder rekey ([ADR-019](019-gitback-founder-rekey.md)) the project_id stays anchored to the *genesis* pubkey, so `sha256(current ‖ handle) != pid` and the project isn't auto-adopted. The announce can't carry the delegation chain (it'd need the chain to verify, which the adopting envoy doesn't have until it adopts — circular). Post-rekey founder projects on a fresh envoy: use `rejoin`. Documented limitation, not a regression — it just doesn't extend ADR-019 automatically.
- **Founder-side only.** Auto-adoption never adopts projects the local puddle is a *non-founder member* of — those need an invitation (`join` / `rejoin`). Only `sha256(my_pubkey ‖ handle) == pid` adopts.
- **`members: [self]` on multi-puddle adoptions.** An adopted `project.json` lists only the founder as a member. For the personal-DR case (`members: {my-puddle}`) that's exactly right. For a multi-puddle project the member list is incomplete until the founder's authoritative metadata reaches the envoy — no worse than `re-found`, which also mints `members: [new_founder]` and relies on `re-invite-all`. It's metadata drift, not a functional break: the catch-up bundle pull verifies the *token chain*, not the member list, so the replica still materializes.
- **Wire-format break.** `GITBACKPROJECT:` changes shape. Zero users, so no migration shim — the parser just tolerates the short form during a rolling agent upgrade (such a project simply isn't auto-adoptable by an upgraded envoy until the announcer is upgraded too). Update ADR-015's and the CLI reference's description of the line.
- **Multicast budget.** Each `GITBACKPROJECT:` line grows by `~1 + handle.len() + 1 + dr_scope.len()` (≤ ~80 bytes). The discovery announce already has a gitback budget that truncates the project list when it would overflow — verify it still fits a realistic project count and truncates gracefully, not mid-field.
- **Security: no new surface.** A member envoy self-minting a founder-bootstrap token for `sha256(my_pubkey ‖ handle)` is precisely what `project init` already does, just later on a sibling envoy. An attacker can't do it for someone else's project: the bootstrap requires `signer == subject == that puddle's pubkey` and the signature is verified — they'd need the victim's puddle key. Spoofed `GITBACKPROJECT:` announces buy nothing: a non-matching `pid` fails the `sha256` check, a matching one is your own project anyway.
- **One unlock, fan-in.** If a puddle founded N projects, the first `unlock` on a fresh envoy mints N small `project.json`/`tokens.json` pairs (cheap) and the catch-up reactor then backfills them one bare repo per ~45 s tick (deliberately gentle — a real repo's history is hundreds of MB). The reactor is a no-op once every locally-held project has refs.

## Implementation notes

Agent-only change. Target version **0.49.0** (new feature) — cross-compile + redeploy the agent to every envoy; no server/Docker change.

1. **`agent/src/swarm/gitback/project_peers.rs`**
   - `ProjectPeerEntry`: add `handle: Option<String>` and `dr_scope: Option<String>`.
   - `record(pid_hex, host, addr, handle: Option<&str>, dr_scope: Option<&str>, now)` + `record_global(...)` — store/refresh handle + dr_scope (last non-`None` wins).
   - `format_project_line(host, pid_hex, handle: &str, dr_scope: &str) -> String` → `GITBACKPROJECT:<host>:<pid_hex>:<handle>:<dr_scope>`.
   - `parse_project_line(line) -> Option<(String, String, Option<String>, Option<String>)>` — 4+ fields → `(host, pid, Some(handle), Some(dr_scope))`; 3 fields → `(host, pid, None, None)`; reject when present-but-malformed (handle fails the project-handle char rules, dr_scope isn't `swarm-wide`/`members-only`).
   - `enumerate_local_projects() -> Vec<(String, String, String)>` — `(pid_hex, handle, dr_scope)`; read each `~/.vigo-gitback/<pid>/project.json` for `handle` + `dr_scope` (a small `#[derive(Deserialize)]` shim, or `ProjectConfig::load`). Update `enumerate_from_passwd` + its tests.
   - `all_announced(now) -> Vec<(String, String, String)>` — per pid with ≥1 non-stale entry carrying a handle, `(pid_hex, handle, dr_scope)`; dedup by pid.
   - `announced_cache_path()` → `crate::swarm::puddle::names_cache::puddle_state_dir().join("gitback-announced.json")`. `write_announced_cache(now)` — serialize `all_announced` as `[{project_id, handle, dr_scope}]`, atomic 0644 write (tmpfile + rename + chmod). `load_announced_cache() -> Vec<AnnouncedProject>` — read + parse; empty Vec on any error.
2. **`agent/src/swarm/discovery.rs`**
   - Emit sites (the two `format_project_line` call sites): iterate `enumerate_local_projects()` → `(pid, handle, dr_scope)`.
   - Parse site + the `gitback_projects` collected field: now the 4-tuple.
   - Ingest loop: `record_global(pid, host, Some(src_ip), handle.as_deref(), dr_scope.as_deref(), now)`.
   - Verify the gitback multicast budget still fits / truncates whole lines. Update the discovery tests that assert the old line shape.
3. **`agent/src/swarm/gitback/mod.rs`** — in `run_catchup_reactor` (or at the end of `catchup_all_users`), each tick: `project_peers::write_announced_cache(now)`.
4. **`agent/src/swarm/gitback/cli.rs`** — factor the bootstrap mint out of `cmd_init` into `pub(crate) fn mint_founder_project(home: &Path, handle: &str, scope: DrScope) -> Result<[u8; ID_LEN]>` (build `TokenBody`, sign via `sign_with_session`, `verify_signature`, persist `ProjectConfig{members:[founder], dr_scope:scope, …}` + `TokenChain`, return the pid). `cmd_init` calls it. Refuse-if-exists stays in `cmd_init`; the helper assumes the caller checked (and re-checks cheaply).
5. **`agent/src/swarm/puddle/cli.rs`** — in `cmd_unlock`, after `session::spawn_helper(...)` and alongside the lockbox bootstrap block: an adoption pass —
   ```
   for (pid_hex, handle, dr_scope) in project_peers::load_announced_cache():
       if hex(sha256(local_puddle_pubkey ‖ handle)) != pid_hex: continue
       if ~/.vigo-gitback/<pid_hex>/project.json exists: continue
       match gitback::cli::mint_founder_project(&home, &handle, dr_scope.parse().unwrap_or(SwarmWide)):
           Ok(_)  => println!("Adopted gitback project '{handle}' — catch-up reactor will pull it.")
           Err(e) => eprintln!("warning: could not adopt '{handle}': {e:#}")  // don't undo the unlock
   ```
6. **Docs** — this ADR + `_index.md`; the `GITBACKPROJECT:` line description in ADR-015 and in `docs/reference/cli/agent-swarm-gitback.md` (the cross-envoy-push paragraph); `docs/reference/cli/agent-swarm-puddle.md` (`puddle unlock` now auto-adopts the puddle's founded projects); `docs/engineering/subsystems/gitback.md` (a note in the DR-catch-up / lifecycle area). Version → 0.49.0; agent-only → cross-compile + redeploy the agent.

### Tests

- `project_peers`: `format_project_line` round-trips through `parse_project_line`; `parse_project_line` accepts the 3-field legacy form (`handle/dr_scope = None`) and rejects a present-but-malformed handle / unknown dr_scope; `all_announced` dedups by pid and drops handle-less entries; `enumerate_from_passwd` returns `(pid, handle, dr_scope)` triples; `write_announced_cache` / `load_announced_cache` round-trip and create the dir.
- `discovery`: a `GITBACKPROJECT:host:<pid>:<handle>:<dr_scope>` line parses and records with the handle; the legacy 3-field line still records (handle `None`).
- `gitback::cli`: `mint_founder_project` round-trip (needs a fake session-signer — see the existing `cmd_init`/`tokens` test helpers).
- The `cmd_unlock` adoption pass spawns the session helper and signs — not unit-testable in isolation; verified on the home-lab fleet (`puddle join` + `puddle unlock` on a fresh envoy → `vigocli swarm gitback show <handle>` shows it in the Hosts table within a couple of catch-up ticks).

### Verification (home lab)

1. On a non-founding puddle envoy (e.g. plex), confirm `ls ~/.vigo-gitback/` is empty for the founded projects.
2. `vigo swarm puddle unlock` on plex → output lists "Adopted gitback project 'vigo'" etc.; `~/.vigo-gitback/<pid>/` now exists.
3. Wait ~1–2 catch-up ticks → `sudo ls /var/lib/vigo/swarm/gitback/projects/` shows the bare repos; `vigocli swarm gitback show vigo` shows plex (and girlslaptop) in the Hosts table; `vigocli swarm status` shows non-zero `gitback` active blobs.
4. Re-run `vigo swarm puddle unlock` on plex → adoption pass is a no-op (state already present).
