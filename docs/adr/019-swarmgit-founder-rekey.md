---
title: "ADR-019: Swarmgit founder-rekey via anchor-preserving delegation chains"
---

# ADR-019: Swarmgit founder-rekey via anchor-preserving delegation chains

**Status:** Complete. Shipped across five slices: data layer (0.35.18), chained verification (0.35.19), rekey-time hooks (0.35.20), multi-puddle re-invite + rejoin + `PushChain` wire format (0.35.21), `re-found` escape hatch + this ADR (0.35.22). Builds on ADR-018 (puddle rekey) and ADR-015 (unified swarmgit substrate).
**Date:** 2026-05-01

## Context

ADR-018 rotates the puddle keypair to recover from a compromised or hygiene-rotated identity, but explicitly defers swarmgit. The deferral is a real problem: under ADR-015, every swarmgit project's identity is `project_id = sha256(founder_puddle_pubkey ‖ handle)`, and capability tokens chain back to that founder pubkey via `verify_chain` (`tokens.rs:321`). A naive founder rekey would therefore:

- Make every `swarmgit://<old_pid>/<handle>` URL the founder typed into a `git remote` config dead.
- Strand bare repos at `/var/lib/vigo/swarm/swarmgit/projects/<old_pid>.git` across every member's disk.
- Leave capability tokens that mathematically still verify against the OLD founder pubkey, but the founder no longer holds the OLD private key, so they cannot mint new tokens against that anchor.
- Break helper-daemon-mediated `git push` / `git fetch` for the founder's own envoys against their own bare repos, because the founder's tokens.json has them as admin via the OLD identity.

ADR-018's deferral was correct as scoping — swarmgit founder-rekey is its own design exercise, not a slice of puddle rekey — but the gap had to close before users with founded projects could safely run `puddle rekey-pair`. ADR-019 is that design.

The dominant use case is personal-DR (founder = sole member, `dr_scope: swarm-wide`). Multi-puddle projects (members across foreign puddles, team-repo + DR shape) are a smaller fraction but a non-zero one — Dan explicitly chose "multi-puddle must work in Phase 1, not deferred" during the design discussion.

## Decision

### Anchor-preserving identity

`project_id` stays anchored to the **genesis** founder pubkey forever — the value baked in at `cmd_init` time via `sha256(local_puddle_pubkey ‖ handle)`. URLs survive every rekey, bare repos don't move, and capability tokens issued before any rekey continue to verify under the original anchor.

The bridge from genesis to the current signing identity is a **delegation chain**: an Ed25519-signed list mapping each retired founder pubkey to its successor, persisted at `~/.vigo-swarmgit/<project_id>/delegations.json`. Each entry is signed by the *previous* link's `new_founder_pubkey` (or, for the first link, the genesis pubkey baked into `project_id`). At verification time, `tokens::verify_chain` walks the delegation list before walking the token chain and applies authority windows so each historical founder pubkey is only trusted to sign tokens with `issued_at` falling inside its half-open `[delegated_at, next_delegated_at)` window.

Authority windows close two attacks in one mechanism:

- **Deposed-founder attack**: an attacker who has compromised a retired founder key cannot sign new tokens dated past that founder's window — `cur.issued_at >= next_delegation.delegated_at` rejects.
- **Antedated-token attack**: a new founder cannot backdate tokens to before their window opened — `cur.issued_at < this_delegation.delegated_at` also rejects.

The chain is capped at `DELEGATION_MAX_LEN = 5` (hard fail at verify time, pre-flight refused on rekey) with a soft warn band starting at `DELEGATION_WARN_LEN = 3`. Past the cap the operator must `swarmgit project re-found <handle>` (mints a fresh project_id from the current puddle pubkey, byte-migrates) or pass `--force-orphan-projects` on the rekey to mark the project frozen.

### Five-slice shipping cadence

1. **Data layer (0.35.18)** — `DelegationEnvelope` in `agent/src/swarm/swarmgit/envelopes.rs` (canonical bytes, sign / verify, `build` / `build_with`, distinct `DELEGATION_DOMAIN` for cross-envelope replay protection); `DelegationChain` in `project_state.rs` (ordered append-only persistence with cap enforcement); `DELEGATION_WARN_LEN` / `DELEGATION_MAX_LEN` constants. No callers yet — pure data.
2. **Chained verification (0.35.19)** — `tokens::verify_chain` gains a `delegations: &[DelegationEnvelope]` parameter (third position). Pre-walks the delegation list with order + project-id + signature checks, builds the founder-window table, then applies the founder-window rule at chain[1] (parent = bootstrap). Helper daemon (`helper.rs::authorize`) loads the per-user `DelegationChain` from disk on every git operation. Empty list = pre-rekey behavior, identical to pre-ADR-019.
3. **Rekey-time hooks (0.35.20)** — new module `agent/src/swarm/swarmgit/rekey.rs` (sibling of lockbox's `identity_rewrap.rs`). `pre_flight_check_caps` runs before any keypair mutation in `puddle/cli.rs::cmd_rekey_pair` / `cmd_rekey`; aborts the rekey if any founded project is at the cap unless `--force-orphan-projects` is passed. `apply_post_rotation_swarmgit_sync` signs + appends delegations under OLD for every founded project, rewrites `project.json::members[]` OLD → NEW for every project the user is in. "Founded by me" predicate: `project.founder_puddle_pubkey ∈ {current pubkey} ∪ {old_puddle_pubkey for every retired_puddles.json entry}` — recovers the full genesis → current history without a new state file.
4. **Multi-puddle re-invite + rejoin + `PushChain` (0.35.21)** — `apply_post_rotation_swarmgit_sync` extended to also rebuild `tokens.json` to `[bootstrap, fresh_admin_under_NEW]` so `cmd_invite` / `cmd_re_invite_all` work after rekey. `Invitation` struct gains optional `delegations` field. New `vigo swarm swarmgit project re-invite-all` (founder side) and `rejoin` (member side, idempotent) verbs. New `PushChain` envelope on the wire (`X-Vigo-Chain` header) carries both the token chain and the founder's delegations, with backwards-compat fallback for pre-ADR-019 senders that emitted bare `TokenChain` JSON.
5. **`re-found` escape hatch + this ADR (0.35.22)** — `vigo swarm swarmgit project re-found <handle>` mints a fresh project_id from the current puddle pubkey, byte-migrates from the at-cap bare repo via the same `git fetch +refs/*:refs/*` path `reanchor` uses, and tombstones the OLD project_id on disk via a new `project.json::tombstoned_at` field. Tombstoned projects are archive-only: helper daemon refuses git operations, `cmd_invite` / `cmd_re_invite_all` skip them, `swarmgit project status` flags them with a warning banner. `--force-orphan-projects` on `rekey-pair` also tombstones over-cap projects so they refuse new admin operations until re-founded.

### Why these specific shapes

**Anchor-preserving over abstract identity.** The "give swarmgit its own per-project keypair, decoupled from puddle" alternative was considered and rejected. It would have given the same property (rekey leaves URLs / bytes untouched) but at the cost of a much heavier refactor: every project would need a wrapped per-project keypair on disk, and the rotation-of-the-rotation-key problem would re-emerge inside swarmgit. The anchor-preserving design gives the same UX win without introducing a second identity layer.

**Each rekey signs delegation under the previous-rekey key, not under genesis.** ADR-018's `rekey-pair` already requires the OLD passphrase to unwrap the OLD key during the rotation ceremony. That same property gives swarmgit the signing key it needs for the next delegation. The founder does NOT need to retain the genesis private key after the first rotation — only the immediately-prior key is needed, and ADR-018 guarantees it's available.

**Authority windows over a permissive "all historical pubkeys count" rule.** Without authority windows, a compromised retired founder key signs new tokens forever. Authority windows are the cheapest fix that closes the deposed-founder attack: each pubkey owns the half-open `[start, end)` interval of timestamps it was authoritative for. The cost is one i64 comparison at chain[1] verification time.

**Hard cap at 5, soft warn at 3.** Picked over no-cap to bound chain-walk cost on the user-foreground push path (~250µs worst-case for 5 extra Ed25519 verifies) and to surface chain-hygiene incidents in `swarmgit status` so the operator is nudged to `re-found` before the cap blocks them. `--force-orphan-projects` is the documented escape hatch when the operator hits the cap on a project they don't care about.

**Multi-puddle rejoin via fresh OOB capability codes, not via gossiped delegations.** The threat model wrinkle is load-bearing: an attacker who has compromised a retired founder key can forge a delegation envelope pointing at their own pubkey; the signature mathematically verifies. ADR-019 therefore requires that delegations only become *authoritative* on a member envoy via a fresh out-of-band capability code from a trusted founder. The `re-invite-all` (founder) + `rejoin` (member) flow is the trust hop — same shape as the original `project invite` / `join` flow. Delegation gossip as an informational hint (status surface flags "founder seems to have rekeyed; ask OOB and run rejoin") could ride on top later, but is not load-bearing.

**Receive-side push verification trusts sender-presented delegations.** `verify_chain_for_push` in `fan_out.rs` accepts the sender's delegations as-is, with no comparison against receiver-local trusted state. Acceptable because swarmgit's threat model is *availability*, not *confidentiality* — an attacker who has compromised a retired founder key could push junk to bare repos but cannot exfiltrate user-readable bytes (lockbox owns confidentiality). Authoritative trust still flows through OOB cap codes at rejoin time, which is what protects member envoys' helper-daemon-mediated git operations.

**`re-found` mints a NEW project_id, not "rotate the chain anchor in place".** Once the chain hits cap, the cleanest reset is to start fresh under the founder's current identity. Project_id changes (this is the one place in the design where it does), URLs change, and members must be re-invited under the new project_id. The on-disk tombstone marker keeps the old bare repo as an archive.

### What stays out of scope

- **Delegation gossip rail.** Originally planned as a slice 4 deliverable; deferred since the OOB rejoin flow makes it nice-to-have, not load-bearing. A future change could add `POST /swarm/swarmgit/envelopes/<pid>/delegations` for inbound and `GET /swarm/swarmgit/envelopes/<pid>/digest` extension for catch-up, with receivers treating envelopes as informational hints in `swarmgit status` rather than authority. Nothing in ADR-019's design forecloses this.
- **Receiver-side trusted-founder pin.** `verify_chain_for_push` could compare sender-presented delegations against a per-project pin and reject divergence. The pin would be installed at `rejoin` time. Phase 1 lives without it because swarmgit's threat model accepts the bare-repo-bytes risk; if a future use case needs stronger receive-side trust (e.g., swarmgit as a paid hosted service rather than personal-DR), this is the shape to add.
- **Cross-rekey coordination of multi-puddle membership.** When a founder rekeys, every cross-puddle member needs a one-time `rejoin` ceremony. The founder can batch this via `re-invite-all`, but each member still has to physically invoke `rejoin` with the OOB code. Automating this would require a trust-rooted gossip primitive that doesn't exist in v1 (and would re-open the deposed-founder threat model). Manual rejoin is the honest cost.
- **Founder identity migration to a different puddle.** ADR-019 assumes the founder's puddle identity persists across rekeys; the genesis pubkey is *retired*, not *transferred to a different puddle*. A founder who wants to hand the project off to a different user / different puddle runs `reanchor` (which does change project_id and require member re-invitation, by design).

## Alternatives Considered

**Re-found at every rekey (no delegation chain at all).** Each `rekey-pair` would mint a new project_id, byte-migrate, tombstone the old. Rejected: URLs change on every rekey, members must rejoin every time. Personal-DR's "founder = sole member" case still has cost (every envoy in the founder's own puddle would need to update remotes), and multi-puddle projects become operationally painful (every cross-puddle member rejoins on every rekey). The delegation-chain design preserves URLs through up to 5 rekeys before re-found is required.

**OLD-key-signed handoff envelope as authority.** An attractively simple shape: founder's `rekey-pair` produces an envelope signed by OLD naming NEW, members auto-accept on first sighting. Rejected for the same threat-model reason ADR-018 rejected its analog: an attacker holding the OLD key signs the same envelope and points victims at their own pubkey. Each member's first acceptance of NEW must come through a fresh OOB capability code, exactly like the original `project invite` flow.

**Delegations applied at admin level, not founder level.** A delegation could be issued by any admin (not just the founder), bridging from one admin pubkey to another. Rejected: admin tokens are already chain links — extending the rotation primitive to non-founder admins would require multiple parallel delegation chains rooted at each admin's tenure, dramatically complicating verification. Founder-only keeps the chain singly-rooted at the genesis pubkey baked into project_id.

**No cap, just rely on operator hygiene.** Without `DELEGATION_MAX_LEN`, the chain grows unbounded and each verification adds linear cost. Bounded chains keep verification cost predictable, force a hygiene reset (via `re-found`) periodically, and let `swarmgit status` surface the hygiene state. The cap is also belt-and-braces against a peer presenting an extra-long chain in the X-Vigo-Chain header — verification rejects.

## Consequences

**Code changes:**
- `agent/src/swarm/swarmgit/envelopes.rs` — `DelegationEnvelope` type + canonical bytes + sign/verify (slice 1).
- `agent/src/swarm/swarmgit/project_state.rs` — `DelegationChain` persistence (slice 1); `tombstoned_at` field on `ProjectConfig` (slice 5).
- `agent/src/swarm/swarmgit/tokens.rs` — `verify_chain` signature change + new `ChainError` variants + authority-window logic (slice 2).
- `agent/src/swarm/swarmgit/helper.rs` — per-user `DelegationChain` load on every authorize call (slice 2); tombstoned-project refusal (slice 5); fan-out delegation transmission (slice 4).
- `agent/src/swarm/swarmgit/rekey.rs` — new module (slice 3); tokens.json rebuild + tombstone marking (slice 4 / slice 5).
- `agent/src/swarm/swarmgit/cli.rs` — `cmd_re_invite_all`, `cmd_rejoin` (slice 4); `cmd_re_found`, `refuse_if_tombstoned`, status surface (slice 5).
- `agent/src/swarm/swarmgit/fan_out.rs` — `PushChain` wire envelope + chain-header encoder/decoder + delegations parameter on `fan_out_bundle` and `verify_chain_for_push` (slice 4).
- `agent/src/swarm/puddle/cli.rs` — pre-flight check + post-rotation hook calls in `cmd_rekey_pair` / `cmd_rekey`, `--force-orphan-projects` flag, swarmgit outcome printer (slices 3–4).

**Documentation:**
- This ADR.
- `docs/adr/_index.md` — entry added.
- `docs/adr/018-puddle-rekey.md` — status header refreshed; "out of scope" item points at ADR-019.
- `docs/reference/cli/agent-swarm-swarmgit.md` — verb table + reference sections for `re-invite-all`, `rejoin`, `re-found`; ADR-019 status subsection.
- `docs/reference/cli/agent-swarm-puddle.md` — `rekey-pair` / `rekey` rows mention `--force-orphan-projects`; "What gets preserved" / "What does NOT survive" sections updated.
- `docs/engineering/subsystems/swarmgit.md` — full ADR-019 section (data layer, authority windows, multi-puddle flow, `PushChain` wire format, shipping cadence).
- `CLAUDE.md` — swarmgit agent block extended.

**Operational:**
- Z-bumps for the implementation slices: 0.35.18 (data layer), 0.35.19 (chained verifier), 0.35.20 (rekey-time hooks), 0.35.21 (multi-puddle rejoin + PushChain), 0.35.22 (re-found + this ADR).
- Agent rebuild + cross-compile + redeploy required for envoys to honor the new wire format. Older agents still parse the legacy bare-`TokenChain` chain header via the decoder fallback path — gradual rollout is fine.
- No server change. No DB migration. No proto change.

**Behavioral consequences:**
- **Personal-DR (single-puddle) survives founder rekey transparently.** Same URL, same bytes, no operator action beyond running `puddle rekey-pair` and `rekey --from <peer>` on each surviving envoy.
- **Multi-puddle projects need a one-time ceremony per rekey.** Founder runs `swarmgit project re-invite-all`, communicates each invitation JSON block OOB to the matching member puddle. Each cross-puddle member runs `rejoin <pid> --invitation -`. Until each member rejoins, their helper daemon rejects the founder's post-rekey pushes (`SignerOutsideFounderWindow`), and `swarmgit status` flags the divergence.
- **Hard cap at 5 chained rekeys.** Past the cap the next rekey fails the pre-flight check unless the operator re-founds the project (mints a new project_id, breaks URLs, requires member re-invitation) or passes `--force-orphan-projects` (tombstones the project, refuses further admin operations until re-founded).
- **Tombstoned projects are archive-only.** Bare repo + URL stay on disk; helper daemon refuses git operations; `cmd_invite` / `cmd_re_invite_all` skip them. Operator either runs `re-found` (fresh project_id, byte-migrate) or `project leave` (drop the archive).
- **Hot-path budget:** receive-side push verification grows ≤250µs at max chain length (5 extra Ed25519 verifies). Helper-daemon load adds one small file read per request (`delegations.json`, ≤1.5KB). Zero changes to agent check-in / convergence / server gRPC handler paths.
