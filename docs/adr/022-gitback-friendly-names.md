---
title: "ADR-022: Friendly gitback names (puddle/repo)"
---

# ADR-022: Friendly gitback names (`puddle/repo`)

**Status:** Implemented in 0.42.0.
**Date:** 2026-05-10

## Context

Gitback projects are addressed by `gitback://<project_id>/<handle>` where
`project_id = sha256(founder_puddle_pubkey ‖ handle)` (ADR-015, amended
2026-05-10). That URL is *self-certifying* — the project_id is derived from
the founder's key, so it can't be squatted and resolves identically
everywhere with zero coordination. It is also a 64-character hex string: not
something you put in a README, type, or remember.

We want `gitback://dan/vigo` to work alongside `gitback://<pid>/vigo` — a
friendly handle for repos a user can already access. The hard part is the
`name → identity` mapping: a friendly name is by definition human-meaningful,
and Zooko's triangle says a name can be human-meaningful, decentralized, and
unique — pick two. The self-certifying hex form is the *decentralized +
unique* corner. A friendly name has to spend something. The swarm has no
global-order primitive (the manifest is a LWW-CRDT — "anyone overwrites"),
so we can't synthesize consensus to break ties.

## Decision

Friendly names live in a **`puddle-name / repo-handle`** namespace, layered
on top of (never replacing) the self-certifying hex form.

### Namespace shape

- `gitback://<puddle-name>/<repo-handle>`. The puddle name is the contested
  thing (one per puddle, like a username); repo handles are *uncontested
  within a puddle* — `project_id = sha256(pubkey ‖ handle)` already
  guarantees one puddle can't have two `vigo`s, so no claiming is needed for
  repo handles, ever.
- Resolution: `<puddle-name>` → puddle pubkey (via the name-claim store),
  then `project_id = sha256(<that pubkey> ‖ <repo-handle>)`, then proceed
  exactly as a hex URL would (helper daemon resolves the bare repo + verifies
  the caller's token chain). The repo handle is `validate_handle`-validated
  before hashing; the puddle name uses the same character rules.
- The hex form `gitback://<64-hex pid>/<handle>` stays valid forever and
  unambiguous. **Friendly names are a convenience; the hex pid is the
  proof.** (See "Spoofability" below.)
- This is GitHub's model: puddle name ≈ `github.com/<user>`, repo handle ≈
  the repo name, and "Bob clones Dan's repo to his namespace" = forking (see
  "Forking").

### Name claims: gossip-via-traits, first-come, ambiguous-on-collision

- A **name claim** is a small signed blob:
  `{ puddle_pubkey, name, claimed_at, supersedes?, sig }`, signed by the
  puddle key over a domain-separated canonical encoding (`b"gitback-name-v1\0"`,
  hand-rolled like `tokens.rs`/`leaves.rs` — never JSON whitespace).
  Persisted at `~<user>/.vigo-puddle/name-claim.json`. Creating one requires
  the puddle unlocked (the session helper signs).
- Claims propagate **as traits**: the `gitback` traits collector (already
  runs as the agent, already reads users' dot-dirs) reports each user's
  name claim; `gitbackmesh` joins them into a fleet `name → pubkey` map,
  flagging collisions; the server exposes it at `GET /api/v1/swarm/puddle/names`
  and `vigocli swarm puddle names list`. The server **publishes, never
  arbitrates** — if two puddles claim `dan`, the published map shows both.
  This is the same passive-aggregator role `gitbackmesh` already plays for
  project rosters. The name map is **not** in the check-in bundle — fetched
  on demand, matching ADR-015's "rosters fetch on-demand via REST" decision;
  the agent caches it locally (`/var/lib/vigo/swarm/puddle/names.json`,
  world-readable) and the resolver reads the cache. A P2P envelope-gossip
  channel for claims (parallel to revoke/leave gossip) is a deferrable
  robustness +1 — not in the initial cut, since name resolution only matters
  when the agent is up, which means it's checking in anyway.
- **First-come, cooperatively enforced.** `vigo swarm puddle name set <name>`
  refuses if `<name>` is already claimed by a different pubkey in the agent's
  current view. This kills the *accidental* collision (Alice and Bob both
  reaching for `mainbox`). It does **not** beat the propagation window (two
  puddles can both check "free" and claim within seconds), and it does
  nothing against a deliberate squatter (who patches the check out or crafts
  the blob directly). Both are accepted — see below.
- **Collisions degrade to ambiguous, never to wrong.** A resolver seeing two
  claims for `dan` errors (`"dan" claimed by 2 puddles: 2535353d…, ab12cd… —
  use gitback://<pid>/<handle>`) until a human runs `puddle name clear` on
  one. There is **no automatic tiebreaker** — lowest-timestamp is spoofable,
  lowest-pubkey is grindable, "you already know the pubkey" defeats the
  point. This is a deliberate non-feature.
- **`supersedes` and founder-rekey / `re-found`.** A puddle owner can
  *re-issue* a claim. A bare puddle rekey (ADR-018) does **not** touch the
  name claim — the project_id stays anchored to the genesis pubkey (ADR-019),
  so the friendly URL keeps resolving to the same project. A `re-found`
  (ADR-019 slice 5) *does*: it mints a fresh project anchor (a new
  project_id), so `re-found` auto-re-issues the name claim pointing
  `name → <new anchor>` with a `supersedes` field carrying the puddle's
  rekey-delegation chain proving the new anchor descends from the old. A
  resolver seeing `dan → P1` and `dan → P2` where P2's claim chains back to
  P1 treats it as "P2 supersedes P1, resolve to P2" — *not* a collision.
  Two claims with no chain relationship are a collision. So `gitback://dan/vigo`
  transparently follows a `re-found`; the hex pid does not (it breaks on
  `re-found`, as it always has).

### Forking

"Bob clones Dan's repo to his namespace" = `git clone gitback://dan/vigo`
(needs Bob to be a member of Dan's project, or root on a holding envoy, or
`public_read` on Dan's project — unchanged from today), then in *Bob's*
puddle: `vigo swarm gitback project init vigo` → a new project
`gitback://bob/vigo` with `project_id = sha256(bob_pubkey ‖ "vigo")`,
distinct from Dan's; then `git remote add … gitback://bob/vigo && git push
--all`. Two fully independent projects. Dan's is untouched (a clone is a
read). It is a **fork, not a mirror** — `gitback://bob/vigo` is a snapshot;
it does not auto-track Dan's; Bob brings updates across with `git fetch
gitback://dan/vigo && git push gitback://bob/vigo`, exactly like a GitHub
fork. And the fork carries Dan's full history and authorship forever — there
is no claw-back across forks. Neither is a flaw; they're the parts of the
GitHub mental model that bite people, so they're documented loudly and
`project status` surfaces "looks like a fork of gitback://dan/vigo (shared
root commit X)" — but no upstream-tracking machinery in v1.

## Consequences / tradeoffs

- **Friendly names are spoofable by squatting.** First-claim-wins includes a
  *malicious* first-claimer: if an attacker claims `acme` before the real
  Acme does, `gitback://acme/billing` cleanly resolves to
  `sha256(attacker_pubkey ‖ "billing")` — the attacker's repo, populated
  with whatever they like. The cloner gets the wrong content with no warning
  *unless* there's also a real Acme claim (then it's ambiguous → error). This
  is inherent (Zooko) and the same caveat as "don't trust a DNS name without
  a cert": **a friendly name is a hint, not a trust anchor.** Mitigations:
  (a) the resolver prints the resolved `(project_id, founder-pubkey prefix)`
  in the clone output so a vigilant operator can spot a mismatch; (b) the
  `puddle name set` refuse-check makes the honest party's early claim stick;
  (c) the hex `gitback://<pid>/vigo` is always available and unforgeable.
  The ADR states this plainly so nobody mistakes the friendly form for a
  security mechanism.
- **Names are aliases, not identities.** `puddle name clear` (or a lapsed
  claim someone else grabs) breaks every `gitback://<name>/…` URL anyone
  wrote down; the hex pids keep working. Name claims do not expire on their
  own; `clear` warns loudly.
- **The name namespace is fleet-global, not per-tenant.** In a multi-tenant
  MSP fleet, all tenants' puddles share one name space; a tenant could squat
  another's expected name. The admin monitors the published name list and
  intervenes out of band. Per-tenant name namespaces are a possible future
  refinement, not v1. (Repos stay per-puddle, so `acme/billing` vs
  `globex/billing` don't collide once the puddle names are settled.)
- **Still fleet-scoped.** `gitback://` is envoy-only; the swarm *is* the
  fleet. A friendly name resolves only on enrolled envoys that have the name
  claim (via traits → server → cache). There is no internet-public
  resolution and the `<pid>` URL stays opaque hex by design.

## Implementation notes

1. `agent/src/swarm/puddle/name.rs` — `NameClaim` struct + `canonical_bytes`
   + `issue` / `verify` + persistence; `supersedes` carries a
   `Vec<DelegationEnvelope>` (reuse the ADR-019 type) for the re-found path.
2. `agent/src/swarm/puddle/cli.rs` — `"name" =>` dispatch with `set`/`clear`/
   `show`. `set` requires the puddle unlocked, runs the refuse-check against
   the cached name map, then signs + persists.
3. `agent/src/traits/gitback.rs` — emit each user's name claim alongside the
   existing per-project summaries.
4. `server/swarm/gitbackmesh/` — aggregate name claims into the fleet map,
   flag collisions; `server/api/` — `GET /api/v1/swarm/puddle/names`;
   `cli/` — `vigocli swarm puddle names list`.
5. Agent maintains `/var/lib/vigo/swarm/puddle/names.json` from the REST
   endpoint (refreshed lazily / on a digest-mismatch signal — never on the
   check-in hot path).
6. `agent/src/swarm/gitback/helper.rs` — new socket verb: resolve a puddle
   name → project_id against the cached map (0 → error, >1 → ambiguous error,
   1 → `sha256(pubkey ‖ validate_handle(handle))`). *(0.48.0:* on a cache miss
   the verb falls back to the SO_PEERCRED'd caller's own
   `~/.vigo-puddle/name-claim.json` — the helper runs as root and can read it —
   so `gitback://<my-name>/<repo>` resolves from the envoy that just ran
   `puddle name set`, before the claim has propagated; a missing/corrupt
   `names.json` is therefore non-fatal.*)
7. `agent/src/swarm/gitback/remote_helper.rs` — `parse_gitback_url`
   branches: 64-hex first component → project_id (today); else → ask the
   helper daemon to resolve the name. Clone output prints the resolved
   `(project_id, founder-pubkey prefix)`.
8. `agent/src/swarm/gitback/cli.rs::cmd_re_found` — after minting the new
   project anchor, auto-re-issue the name claim with `supersedes` set and
   mark the old claim superseded.
9. Docs: this ADR + `_index.md`; the `gitback` / `remote-helper` / `puddle`
   CLI references; CLAUDE.md's swarm note. Version → 0.42.0 (new feature);
   agent + server change → cross-compile + redeploy the agent, Docker rebuild
   the server.

Explicit non-features: no automatic conflict resolution for name collisions;
no fork-upstream-tracking; no per-tenant name namespaces (v1); no P2P gossip
channel for name claims in the initial cut (deferrable +1). `public_read`
(letting non-members clone via the friendly URL) is its own orthogonal
feature, not part of this ADR.
