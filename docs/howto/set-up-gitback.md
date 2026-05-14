
# Gitback — Unified Git Substrate (ADR-015)

> **ADR-015 cutover complete.** Steps 1–3 shipped earlier; step 4 shipped across six slices on 2026-04-30. The substrate is keyed on `project_id = sha256(founder_puddle_pubkey ‖ handle)` with helper-daemon-mediated push/fetch over a SO_PEERCRED-gated Unix socket, push-driven fan-out, LAN auto-discovery, cross-envoy revoke/leave envelope gossip with digest catch-up, project-id-keyed fleet observability (UI + REST + CLI), a REST member-roster endpoint for push-time lookup, and `reanchor` byte-level migration. ADR-016 (lockbox membership on puddle) is the next ADR in the puddle arc.

Gitback is the P2P git substrate underlying both personal disaster-recovery (today's primary use case) and multi-member collaboration (an axis introduced by ADR-015). Each project is identified by `project_id = sha256(founder_puddle_pubkey ‖ handle)`; project shape is described by two orthogonal axes:

| `members:`           | `dr_scope:`    | Use case                      |
|----------------------|----------------|-------------------------------|
| `{my-puddle}`         | `swarm-wide`   | Personal-DR (today's default) |
| `{my-puddle}`         | `members-only` | Single-user, single-puddle     |
| `{team1, team2, …}`  | `members-only` | Multi-member collaboration    |
| `{team1, team2, …}`  | `swarm-wide`   | Team repo + swarm-wide DR     |

Per-puddle identity (per [ADR-014](../adr/014-puddle-primitive.md)) gives multi-envoy users a stable trust unit; the substrate sees fleets, not envoys.

Gitback is one of four content subsystems that ride the [swarm](set-up-swarm.md) substrate. The server plays no role in membership decisions — it provides three thin services (gitbackmesh aggregator, cross-subnet address resolution, audit-log entries). Membership is puddle-anchored capability tokens, verified offline against the URL's project_id.

## Status (ADR-015 arc)

| Piece | Status |
|-------|--------|
| Multi-call binary dispatch (`git-remote-gitback`) | ✅ 0.34.9 (step 1) |
| Capability-token primitives (`tokens.rs`) | ✅ 0.34.10 (step 2) |
| `~/.vigo-gitback/<project_id>/` per-user state + `projects/<project_id>.git` storage | ✅ step 3 |
| `vigo swarm gitback project {init,invite,join,leave,list,status}` local verbs | ✅ step 3 |
| `vigo swarm gitback {status,purge}` top-level verbs (Phase C) | ✅ 0.38.8–0.38.9 |
| `vigocli swarm gitback members` admin verb (Phase C) | ✅ 0.38.10 |
| `vigo swarm gitback project {revoke,leave}` envelope **signing** + sentinel queueing | ✅ step 4 first slice (CLI signs inline via puddle session helper) |
| Helper daemon + SO_PEERCRED socket + receive-pack / upload-pack proxy | ✅ step 4 first slice (`/var/run/vigo/gitback.sock`) |
| `git-remote-gitback` body — `connect` capability + transparent splice | ✅ step 4 first slice |
| Revoke / leave envelope persistence + 90-day reap | ✅ step 4 first slice (`/var/lib/vigo/swarm/gitback/envelopes/<project_id>/`) |
| Push-driven fan-out (cycle prevention via delta check) | ✅ step 4 second slice |
| Receive-side `POST /swarm/gitback/push/<project_id_hex>` endpoint | ✅ step 4 second slice |
| LAN auto-discovery via announce extension (`GITBACKPROJECT:` lines + project-peer index) | ✅ step 4 third slice |
| Cross-envoy envelope gossip (digest-based catch-up) | ✅ step 4 fourth slice |
| Re-emit `gitback` trait from new state surface | ✅ step 4 fifth slice |
| Server-side gitbackmesh schema rewrite (project-id-keyed) | ✅ step 4 fifth slice |
| `/gitback` UI page rewrite for project-id view | ✅ step 4 fifth slice |
| `vigocli swarm gitback` CLI rewrite for project-id view | ✅ step 4 fifth slice |
| REST member-roster endpoint (`GET /api/v1/swarm/gitback/projects/<pid>/roster`) | ✅ step 4 sixth slice |
| `reanchor` byte-level migration (`git fetch +refs/*:refs/*` from old to new bare repo) | ✅ step 4 sixth slice |
| Friendly `gitback://<puddle-name>/<repo>` URLs + fleet name map (ADR-022) | ✅ 0.42.0 |
| Receiver-initiated DR catch-up (`GET /swarm/gitback/bundle/<pid>` + catch-up reactor) | ✅ 0.46.0 |
| Puddle-wide project adoption — `puddle unlock` mints founder-bootstrap state for the puddle's founded projects (ADR-023; widens `GITBACKPROJECT:` to `…:<handle>:<dr_scope>`; reactor flushes `gitback-announced.json`) | ✅ 0.49.0 |
| Bare-repo `HEAD` reconciliation — keep `HEAD` pointing at a live branch so a fresh `git clone` checks out without `git checkout main` | ✅ 0.49.6 |
| Helper-daemon `STATUS` verb — `vigo swarm gitback status` gets the root-owned bare-repo facts from the daemon instead of mis-reporting "0 bare repos" when run unprivileged | ✅ 0.49.7 |

### Receiver-initiated DR catch-up (0.46.0)

`project init` / `join` writes only per-user state; the post-receive fan-out fires only on a ref delta. So between joining and the founder's next push, a member held no bare repo at all — "swarm-wide DR" didn't actually reach a freshly-enrolled or re-joined envoy until something pushed. (Operators worked around it with a throwaway tag push or a hand-edited `cross_subnet_members`.) The catch-up reactor closes the gap: every ~45 s it walks each user's `~/.vigo-gitback/<pid>/`, and for the first project that has local state but no (or an empty) bare repo, it `GET`s `/swarm/gitback/bundle/<pid>` from a peer that announces holding it (`GITBACKPROJECT:` index, addressed by the announced LAN IP). The request carries the same `X-Vigo-Chain` envelope a push does, so the serving peer verifies membership identically; the response body is a `git bundle create - --all`, streamed straight to a staging file and `git bundle verify`d before `git fetch`. One repo backfilled per tick — git history is hundreds of MB for a real repo and DR catch-up isn't latency-critical; the reactor is a no-op once every locally-held project has refs.

**Bundle-size cap (one limit, all paths).** A project's `git bundle --all` is capped at `swarm.gitback.max_bundle_bytes` (server.yaml, default **2 GiB**; `0`/unset → the default — no uncapped mode). The same limit is enforced on every transfer path: the publisher fan-out (`fan_out_bundle` checks the bundle's file size before any POST), the push-receive POST handlers (`handle_gitback_push*` count the streamed body), and the DR catch-up GET (`ingest_bundle` limits the `io::copy` from the response to `cap + 1` bytes and rejects an over-cap stream). None of these paths buffers the whole bundle in RAM — pushes stream from the on-disk bundle file, receives stream to a staging file. (Before 0.49.1 the catch-up GET silently capped at ureq's 10 MiB `read_to_vec` default and the receive cap was a separate hardcoded 20 GiB.)

### Puddle-wide project adoption (0.49.0, [ADR-023](../adr/023-gitback-puddle-wide-adoption.md))

DR catch-up backfills a bare repo for a project an envoy *already has per-user state for* — but `project init` writes that state only on the founding envoy, so a founder's *other* envoys never had state and the reactor never fired for them (`vigocli swarm gitback list` showed `HOSTS 1` forever). The fix piggybacks on the catch-up loop: each tick the reactor flushes `/var/lib/vigo/swarm/puddle/gitback-announced.json` (`[{project_id, handle, dr_scope}]`, 0644, world-readable — same self-certifying public data as the `gitback://` URL) from the `ProjectPeers` index, which now also carries the handle + scope because the `GITBACKPROJECT:` announce line was widened to `GITBACKPROJECT:<host>:<pid_hex>:<handle>:<dr_scope>` (the legacy 2-field form still parses during a rolling agent upgrade). Then `vigo swarm puddle unlock` — the one moment that runs as the user with a freshly-spawned session helper, alongside the existing lockbox bootstrap — reads that cache and, for every announced project where `sha256(local_puddle_pubkey ‖ handle) == project_id` and there's no `~/.vigo-gitback/<pid>/` yet, mints founder-bootstrap state (the same admin token chain + `project.json` `project init` produces, factored into a shared `mint_founder_project` helper). The 0.46.0 reactor then pulls the bytes on its next tick. Founder-current-key only: a post-rekey ([ADR-019](../adr/019-gitback-founder-rekey.md)) founded project stays anchored to the *genesis* pubkey, so it won't match — use `gitback project rejoin`. Non-founder memberships are never auto-adopted; they still need an invitation. No new attack surface — self-minting a founder bootstrap for `sha256(my_pubkey ‖ handle)` is precisely what `project init` already does; the bootstrap requires `signer == subject == that puddle's pubkey` and the signature is verified, so a spoofed `GITBACKPROJECT:` line buys an attacker nothing. Agent-only change: cross-compile + redeploy the agent.

### Bare-repo `HEAD` reconciliation (0.49.6)

`git init --bare` leaves `HEAD` at git's compiled-in default (`refs/heads/master`); the first push or DR-catch-up bundle ingest establishes the real branches (commonly just `main`) but never repoints `HEAD`. So `git upload-pack` advertised `symref=HEAD:refs/heads/master`, a nonexistent ref, and every fresh `git clone gitback://…/<repo>` warned "remote HEAD refers to nonexistent ref, unable to checkout" and left an empty working tree until the operator ran `git checkout main` by hand. `fan_out::set_head_to_default_branch` reconciles `HEAD` to a branch that actually exists — preferring `main`, then `master`, then the alphabetically-first branch — and is invoked at the points where a bare repo's refs can move: after a ref-changing `git-receive-pack` (the first push's post-receive), inside `apply_bundle` (every fan-out / DR ingest), and before `git-upload-pack` serves a clone (which also self-heals repos created before this shipped). Idempotent — a no-op when `HEAD` is already valid, `Ok(())` on a branchless repo. The preference order is deterministic, so receivers converge on the same `HEAD` the originator picked. Agent-only change: cross-compile + redeploy.

### Helper-daemon `STATUS` verb (0.49.7)

The bare repos under `/var/lib/vigo/swarm/gitback/projects/` are `0700 root:root` — non-root callers reach them only through the helper daemon. So `vigo swarm gitback status`, which an operator runs as their own (unprivileged) user, couldn't `stat` into `projects/`: `project_repo_path(...).exists()` returned `false` for every project, and the command printed "0 with a local bare repo / 0 B on disk / no bare repo yet" even when the repos were fully materialized. (The `gitback` *trait* never had this problem — it's built by the root agent, which is why `vigocli swarm gitback` was always correct.) Fixed by giving the helper daemon a third socket verb alongside `HELLO` / `RESOLVE-NAME`: `STATUS` → `OK <compact-json-array>\n`, the daemon (root) running `traits::gitback::collect_projects` and serializing the result — the same per-project view the trait carries. The CLI asks for it; if the socket's unreachable it falls back to a local scan and marks the output `partial data:` rather than asserting a falsehood. The verb is unauthenticated — the data is the same public tier as the `0644` `gitback-announced.json` cache and the server-bound trait — and runs `collect_projects` in `spawn_blocking` so it doesn't stall the reactor. Agent-only change: cross-compile + redeploy.

## ADR-019: Founder-rekey delegation chains

ADR-018 rotates the puddle keypair but explicitly defers gitback. Project IDs are anchored to `sha256(founder_puddle_pubkey ‖ handle)`, so a founder rekey would naively orphan every founded project — dead URLs, stranded bare repos, capability tokens that no longer chain to the founder's current signing identity. ADR-019 closes this by introducing a per-project **delegation chain**: an Ed25519-signed bridge from each retired founder pubkey to its successor, persisted alongside `tokens.json` and `project.json`. Project_id stays anchored to the genesis founder pubkey forever — URLs survive, bare repos don't move, pre-rekey tokens still verify.

**On-disk shape** (`~/.vigo-gitback/<project_id>/delegations.json`):

```json
{
  "version": 1,
  "delegations": [
    {
      "project_id":           "<hex>",
      "old_founder_pubkey":   "<hex>",   // signer of THIS envelope
      "new_founder_pubkey":   "<hex>",   // delegated-to key
      "delegated_at":         <unix-ts>,
      "signature":            "<hex 64-byte ed25519 sig>"
    }
  ]
}
```

Append-only ordered list. Each entry is signed by the *previous* link's `new_founder_pubkey` (or, for the first entry, the genesis pubkey baked into `project_id`). Verifier walks the list before the token chain; any link's signature failure, broken pubkey linkage, or non-strict-increasing `delegated_at` aborts. Hard cap at `DELEGATION_MAX_LEN = 5`; warn band starts at `DELEGATION_WARN_LEN = 3`.

**Authority windows.** Each founder pubkey owns a half-open `[delegated_at, next_delegated_at)` interval. Genesis covers `(-∞, d_0.delegated_at)`; the most-recent delegation covers `[d_N.delegated_at, ∞)`. At chain[1] (token whose parent is the bootstrap), the verifier requires `signer_puddle_pubkey == founder_at(token.issued_at)`. This closes both the **deposed-founder attack** (a retired founder's compromised key signs fresh tokens dated past their window) and the **antedated-token attack** (a new founder backdates tokens to before their window opened).

**Rekey-time hooks.** `puddle rekey-pair` and `rekey --from` automatically run two integration steps via `agent/src/swarm/gitback/rekey.rs`:

- **Pre-flight** (`pre_flight_check_caps`) — walks every founded project; aborts the rekey before any keypair mutation if any chain is at cap, unless `--force-orphan-projects` is passed.
- **Post-rotation** (`apply_post_rotation_gitback_sync`) — for every founded project, signs + appends a delegation envelope under OLD; for every project the user is a member of, rewrites `project.json::members[]` OLD → NEW. Each envoy in a puddle maintains its own self-consistent local chain (signatures byte-identical modulo `delegated_at`).

**Founded-by-me predicate.** `project.founder_puddle_pubkey ∈ {current puddle pubkey} ∪ {old_puddle_pubkey for every entry in retired_puddles.json}`. Recovers the full `genesis → r_1 → … → current` history without a new state file. A project founded at any past identity counts as "mine" for delegation purposes.

**Threat-model wrinkle.** An attacker holding a retired founder key can forge a delegation envelope pointing at their own pubkey — the signature mathematically verifies. ADR-019 therefore requires that delegations only become *authoritative* on a member envoy via a fresh out-of-band capability code from a trusted founder. The `vigo swarm gitback project re-invite-all` (founder) + `rejoin` (member) flow shipped at 0.35.21 mediates this trust hop, same shape as the original `project invite` / `join` flow. Single-puddle personal-DR is unaffected — every envoy in the user's own puddle inherits trust via ADR-018's pair-claim ceremony.

**Multi-puddle rejoin flow (slice 4).** After a founder rekey:

1. Founder runs `vigo swarm gitback project re-invite-all`. The verb walks every founded project where `members[]` minus the local fleet is non-empty, mints a fresh `Member` token chained off the local user's current admin leaf (which slice 3's rekey hook rebuilt with `[bootstrap, fresh_admin_under_NEW]`), and embeds the project's full delegation chain in each `Invitation` so the recipient's verifier can walk back to the genesis founder pubkey baked into `project_id`.
2. Founder communicates each invitation JSON block OOB to the matching member puddle (per-recipient stderr separators make this scriptable).
3. Each cross-puddle member runs `vigo swarm gitback project rejoin <pid> --invitation -`. The verb is idempotent — does NOT bail when the project already exists locally — and updates `project.json` + `tokens.json` + `delegations.json` from the invitation. When the invitation has empty delegations (founder rolled back or re-founded under genesis), `delegations.json` is removed so the verifier doesn't apply bogus authority windows.

**Wire chain header (`X-Vigo-Chain`).** Slice 4 also gained a `PushChain` envelope so the receive-side push verifier can chain a post-rekey leaf back to genesis without consulting per-user state:

```rust
struct PushChain {
    version: u32,
    chain: Vec<Token>,
    delegations: Vec<DelegationEnvelope>,
}
```

`encode_chain_header(chain, delegations)` and `decode_chain_header()` round-trip this shape; the decoder also tolerates pre-ADR-019 senders that emitted bare `TokenChain` JSON via a fallback path. `verify_chain_for_push` threads sender-presented delegations into `verify_chain`. Acceptable because gitback's threat model is *availability*, not confidentiality — an attacker who has compromised a retired founder key could push junk to bare repos but cannot exfiltrate user-readable bytes (lockbox owns confidentiality). Authoritative trust still flows through OOB cap codes at rejoin time.

**Re-found escape hatch (slice 5).** Once a project's delegation chain hits `DELEGATION_MAX_LEN`, the founder runs `vigo swarm gitback project re-found <handle>`. This:

1. Refuses unless the calling user IS the current founder (predicate matches `re-invite-all`'s).
2. Refuses unless chain is at cap, project is tombstoned, or `--force` is passed.
3. Refuses if the founder hasn't rekeyed since founding (new `project_id = sha256(current_puddle_pubkey ‖ handle)` would collide with the old).
4. Mints a fresh founder bootstrap admin token under the current puddle identity, persists `project.json` + `tokens.json` under the new project_id with `members: [this_fleet]`.
5. Byte-migrates from the old bare repo via `git -C <new_repo> fetch <old_repo> +refs/*:refs/*` (root-only; non-root prints a note).
6. Tombstones the old project on disk: `project.json::tombstoned_at = unix_ts`. Tombstoned projects keep their bare repo + URL on disk as an archive — helper daemon refuses git operations against them, CLI verbs (`invite`, `revoke`, `re-invite-all`) skip them, `project status` flags them with a warning banner. `--force-orphan-projects` on `rekey-pair` also writes the tombstone marker, so projects past the cap can be left behind during a rekey rather than blocking it.

URLs change at re-found (this is the one place in the design where they do). Cross-puddle members must update their remotes to the new project_id and run a fresh `rejoin` ceremony — the founder typically runs `re-invite-all <new_pid>` immediately after re-found to mint the invitations.

**Shipping cadence.** Five slices in tree at 0.35.18–0.35.22: data layer; chained verifier with authority windows; rekey-time hooks + `--force-orphan-projects`; multi-puddle `re-invite-all` + `rejoin` + `PushChain` wire format; `re-found` escape hatch + tombstone marker + the formal [ADR-019 document](../adr/019-gitback-founder-rekey.md).

## ADR-022: Friendly URLs (`gitback://<puddle-name>/<repo>`)

The self-certifying `gitback://<project_id>/<repo>` form is unrememberable. ADR-022 (0.42.0) layers a friendly form on top: a puddle claims a human-meaningful **name** (`vigo swarm puddle name set <name>`, signed by the puddle key, persisted at `~/.vigo-puddle/name-claim.json`), and `gitback://<name>/<repo>` resolves as `project_id = sha256(<that puddle's pubkey> ‖ <repo>)` — then proceeds exactly as a hex URL. Resolution lives in `git-remote-gitback`: a 64-hex first component is a project_id directly; otherwise it's a name, and the binary asks the helper daemon (`RESOLVE-NAME <name> <repo>`) which looks the name up in the locally-cached fleet name map and replies `OK <pid_hex> <founder_pubkey_hex>` (or `ERR …`). On a fleet-map miss (propagation lag, or a fresh envoy whose name-map bootstrap hasn't run), the helper falls back to the SO_PEERCRED'd caller's own `~/.vigo-puddle/name-claim.json` — it runs as root, so it can read it — so `gitback://<my-name>/<repo>` resolves from the very envoy that just ran `puddle name set`, without waiting for the trait → server → re-fetch round trip; a missing/corrupt `names.json` is therefore non-fatal too. The clone output prints the resolved `(project_id, founder pubkey)` so an operator can eyeball the binding.

Claims propagate as `gitback`-trait data — the collector reports each user's `name-claim.json` verbatim, `gitbackmesh` joins them into a `name → claims` map (`GET /api/v1/swarm/puddle/names`), each agent caches it (`/var/lib/vigo/swarm/puddle/names.json`, refreshed on startup + every 15 min, off the hot path). **The server publishes signed claims and never arbitrates** — a name with one claimant resolves; a name with two unrelated claimants is a collision the resolver refuses (use the hex form); two claims where the newer carries a `supersedes` rekey-delegation chain back to the older are a `re-found` and the resolver follows it (so `re-found` auto-re-issues the claim with that chain — see ADR-019's `re-found` above). Repo handles are uncontested within a puddle (one puddle can't have two `vigo`s — `sha256(pubkey ‖ handle)` already guarantees it), so there's no claiming for handles, ever. **A friendly name is a hint, not a trust anchor** — spoofable by squatting (first claim wins, including a malicious first-claimer); the unforgeable form is always `gitback://<project_id>/<repo>`. Full design: [ADR-022](../adr/022-gitback-friendly-names.md).

`gitbackmesh` also reverse-resolves the name map into `founder_pubkey → name` (uncontested names only; freshest claim wins when a puddle holds several) and stamps each `ProjectSummary.FriendlyName`, so the admin surfaces — the `/gitback` UI page and `vigocli swarm gitback {list,show}` — display `gitback://<name>/<handle>` with the canonical hex URL alongside. Same hint-not-anchor rule: a contested name leaves `FriendlyName` empty and only the hex form shows.

## Operability

Gitback is gated per-envoy via the `swarm.gitback.enabled` pattern list in server.yaml (0.41.0+). Both `swarm.enabled` AND `swarm.gitback.enabled` must allow the host.

```yaml
swarm:
  enabled: ["*"]
  gitback:
    enabled: ["annlap", "plex", "girlslaptop"]
```

When a host falls out of the pattern list, the gitback reactor idles on the next check-in (publisher and receiver inert, helper-daemon socket stays bound but refuses new pushes/fetches). Already-held bare repos under `/var/lib/vigo/swarm/gitback/{projects,mirrors}/` are preserved — pattern flips are pause/resume, not evict.

The destructive lever moved to the **usercrate `gitback: bool` flag** (0.41.0). Removing `gitback: true` from a usercrate triggers a scrub of `~/.vigo-gitback/` for that user on the next reconcile. Per-user state goes; the root-owned bare repos under `/var/lib/vigo/swarm/gitback/` survive — admin cleanup is a separate path. See [swarm Operability](set-up-swarm.md#operability--pattern-list-authorization).

Pattern-list changes record tamper-evident audit-chain entries (`swarm.gitback.enabled`) under `actor=config.reload` at publish time.

## Related

- [ADR-015 — Unified gitback substrate](../adr/015-unified-gitback-substrate.md) — the foundational design (project shape, capability tokens, helper daemon, fan-out, gossip)
- [ADR-019 — Founder-rekey delegation chains](../adr/019-gitback-founder-rekey.md) — the design behind `re-invite-all`, `rejoin`, `re-found`, the delegation chain primitive, and authority windows
- [ADR-022 — Friendly gitback names](../adr/022-gitback-friendly-names.md) — `gitback://<puddle-name>/<repo>` layered on the hex form; the fleet name map; first-come/ambiguous-on-collision semantics
- [`vigo swarm gitback` CLI reference](../reference/vigocli/agent-swarm-gitback.md) — operator-facing verb walkthroughs
- [Swarm](set-up-swarm.md) — the underlying P2P substrate gitback rides
- [Puddle](set-up-puddle.md) — per-user puddle identity (ADR-014); the trust unit gitback's tokens chain to
- [Lockbox](set-up-lockbox.md) — the encryption-first sibling subsystem that owns *confidentiality* (gitback owns *availability*)
- [`gitback` trait reference](../reference/vigo/traits/) — schema of the per-project entries the agent emits on each check-in (project_id, members, dr_scope, head_refs, envelope counts)

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
