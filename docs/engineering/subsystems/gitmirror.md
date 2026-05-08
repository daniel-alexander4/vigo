
# Gitmirror — Unified Git Substrate (ADR-015)

> **ADR-015 cutover complete.** Steps 1–3 shipped earlier; step 4 shipped across six slices on 2026-04-30. The substrate is keyed on `project_id = sha256(founder_puddle_pubkey)` with helper-daemon-mediated push/fetch over a SO_PEERCRED-gated Unix socket, push-driven fan-out, LAN auto-discovery, cross-envoy revoke/leave envelope gossip with digest catch-up, project-id-keyed fleet observability (UI + REST + CLI), a REST member-roster endpoint for push-time lookup, and `reanchor` byte-level migration. ADR-016 (cryptsync membership on puddle) is the next ADR in the puddle arc.

Gitmirror is the P2P git substrate underlying both personal disaster-recovery (today's primary use case) and multi-member collaboration (an axis introduced by ADR-015). Each project is identified by `project_id = sha256(founder_puddle_pubkey)`; project shape is described by two orthogonal axes:

| `members:`           | `dr_fanout:`   | Use case                      |
|----------------------|----------------|-------------------------------|
| `{my-puddle}`         | `fleet-wide`   | Personal-DR (today's default) |
| `{my-puddle}`         | `members-only` | Single-user, single-puddle     |
| `{team1, team2, …}`  | `members-only` | Multi-member collaboration    |
| `{team1, team2, …}`  | `fleet-wide`   | Team repo + fleet-wide DR     |

Per-puddle identity (per [ADR-014](../../adr/014-puddle-primitive.md)) gives multi-envoy users a stable trust unit; the substrate sees fleets, not envoys.

Gitmirror is one of four content subsystems that ride the [swarm](swarm.md) substrate. The server plays no role in membership decisions — it provides three thin services (gitmirrormesh aggregator, cross-subnet address resolution, audit-log entries). Membership is puddle-anchored capability tokens, verified offline against the URL's project_id.

## Status (ADR-015 arc)

| Piece | Status |
|-------|--------|
| Multi-call binary dispatch (`git-remote-vigo`) | ✅ 0.34.9 (step 1) |
| Capability-token primitives (`tokens.rs`) | ✅ 0.34.10 (step 2) |
| `~/.vigo-gitmirror/<project_id>/` per-user state + `projects/<project_id>.git` storage | ✅ step 3 |
| `vigo swarm gitmirror project {init,invite,join,leave,list,status}` local verbs | ✅ step 3 |
| `vigo swarm gitmirror {status,purge}` top-level verbs (Phase C) | ✅ 0.38.8–0.38.9 |
| `vigocli swarm gitmirror members` admin verb (Phase C) | ✅ 0.38.10 |
| `vigo swarm gitmirror project {revoke,leave}` envelope **signing** + sentinel queueing | ✅ step 4 first slice (CLI signs inline via puddle session helper) |
| Helper daemon + SO_PEERCRED socket + receive-pack / upload-pack proxy | ✅ step 4 first slice (`/var/run/vigo/gitmirror.sock`) |
| `git-remote-vigo` body — `connect` capability + transparent splice | ✅ step 4 first slice |
| Revoke / leave envelope persistence + 90-day reap | ✅ step 4 first slice (`/var/lib/vigo/swarm/gitmirror/envelopes/<project_id>/`) |
| Push-driven fan-out (cycle prevention via delta check) | ✅ step 4 second slice |
| Receive-side `POST /swarm/gitmirror/push/<project_id_hex>` endpoint | ✅ step 4 second slice |
| LAN auto-discovery via announce extension (`GITMIRRORPROJECT:` lines + project-peer index) | ✅ step 4 third slice |
| Cross-envoy envelope gossip (digest-based catch-up) | ✅ step 4 fourth slice |
| Re-emit `gitmirror` trait from new state surface | ✅ step 4 fifth slice |
| Server-side gitmirrormesh schema rewrite (project-id-keyed) | ✅ step 4 fifth slice |
| `/gitmirror` UI page rewrite for project-id view | ✅ step 4 fifth slice |
| `vigocli swarm gitmirror` CLI rewrite for project-id view | ✅ step 4 fifth slice |
| REST member-roster endpoint (`GET /api/v1/swarm/gitmirror/projects/<pid>/roster`) | ✅ step 4 sixth slice |
| `reanchor` byte-level migration (`git fetch +refs/*:refs/*` from old to new bare repo) | ✅ step 4 sixth slice |

## ADR-019: Founder-rekey delegation chains

ADR-018 rotates the puddle keypair but explicitly defers gitmirror. Project IDs are anchored to `sha256(founder_puddle_pubkey)`, so a founder rekey would naively orphan every founded project — dead URLs, stranded bare repos, capability tokens that no longer chain to the founder's current signing identity. ADR-019 closes this by introducing a per-project **delegation chain**: an Ed25519-signed bridge from each retired founder pubkey to its successor, persisted alongside `tokens.json` and `project.json`. Project_id stays anchored to the genesis founder pubkey forever — URLs survive, bare repos don't move, pre-rekey tokens still verify.

**On-disk shape** (`~/.vigo-gitmirror/<project_id>/delegations.json`):

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

**Rekey-time hooks.** `puddle rekey-pair` and `rekey --from` automatically run two integration steps via `agent/src/swarm/gitmirror/rekey.rs`:

- **Pre-flight** (`pre_flight_check_caps`) — walks every founded project; aborts the rekey before any keypair mutation if any chain is at cap, unless `--force-orphan-projects` is passed.
- **Post-rotation** (`apply_post_rotation_gitmirror_sync`) — for every founded project, signs + appends a delegation envelope under OLD; for every project the user is a member of, rewrites `project.json::members[]` OLD → NEW. Each envoy in a puddle maintains its own self-consistent local chain (signatures byte-identical modulo `delegated_at`).

**Founded-by-me predicate.** `project.founder_puddle_pubkey ∈ {current puddle pubkey} ∪ {old_puddle_pubkey for every entry in retired_puddles.json}`. Recovers the full `genesis → r_1 → … → current` history without a new state file. A project founded at any past identity counts as "mine" for delegation purposes.

**Threat-model wrinkle.** An attacker holding a retired founder key can forge a delegation envelope pointing at their own pubkey — the signature mathematically verifies. ADR-019 therefore requires that delegations only become *authoritative* on a member envoy via a fresh out-of-band capability code from a trusted founder. The `vigo swarm gitmirror project re-invite-all` (founder) + `rejoin` (member) flow shipped at 0.35.21 mediates this trust hop, same shape as the original `project invite` / `join` flow. Single-puddle personal-DR is unaffected — every envoy in the user's own puddle inherits trust via ADR-018's pair-claim ceremony.

**Multi-puddle rejoin flow (slice 4).** After a founder rekey:

1. Founder runs `vigo swarm gitmirror project re-invite-all`. The verb walks every founded project where `members[]` minus the local fleet is non-empty, mints a fresh `Member` token chained off the local user's current admin leaf (which slice 3's rekey hook rebuilt with `[bootstrap, fresh_admin_under_NEW]`), and embeds the project's full delegation chain in each `Invitation` so the recipient's verifier can walk back to the genesis founder pubkey baked into `project_id`.
2. Founder communicates each invitation JSON block OOB to the matching member puddle (per-recipient stderr separators make this scriptable).
3. Each cross-puddle member runs `vigo swarm gitmirror project rejoin <pid> --invitation -`. The verb is idempotent — does NOT bail when the project already exists locally — and updates `project.json` + `tokens.json` + `delegations.json` from the invitation. When the invitation has empty delegations (founder rolled back or re-founded under genesis), `delegations.json` is removed so the verifier doesn't apply bogus authority windows.

**Wire chain header (`X-Vigo-Chain`).** Slice 4 also gained a `PushChain` envelope so the receive-side push verifier can chain a post-rekey leaf back to genesis without consulting per-user state:

```rust
struct PushChain {
    version: u32,
    chain: Vec<Token>,
    delegations: Vec<DelegationEnvelope>,
}
```

`encode_chain_header(chain, delegations)` and `decode_chain_header()` round-trip this shape; the decoder also tolerates pre-ADR-019 senders that emitted bare `TokenChain` JSON via a fallback path. `verify_chain_for_push` threads sender-presented delegations into `verify_chain`. Acceptable because gitmirror's threat model is *availability*, not confidentiality — an attacker who has compromised a retired founder key could push junk to bare repos but cannot exfiltrate user-readable bytes (cryptsync owns confidentiality). Authoritative trust still flows through OOB cap codes at rejoin time.

**Re-found escape hatch (slice 5).** Once a project's delegation chain hits `DELEGATION_MAX_LEN`, the founder runs `vigo swarm gitmirror project re-found <handle>`. This:

1. Refuses unless the calling user IS the current founder (predicate matches `re-invite-all`'s).
2. Refuses unless chain is at cap, project is tombstoned, or `--force` is passed.
3. Refuses if the founder hasn't rekeyed since founding (new `project_id = sha256(current_puddle_pubkey)` would collide with the old).
4. Mints a fresh founder bootstrap admin token under the current puddle identity, persists `project.json` + `tokens.json` under the new project_id with `members: [this_fleet]`.
5. Byte-migrates from the old bare repo via `git -C <new_repo> fetch <old_repo> +refs/*:refs/*` (root-only; non-root prints a note).
6. Tombstones the old project on disk: `project.json::tombstoned_at = unix_ts`. Tombstoned projects keep their bare repo + URL on disk as an archive — helper daemon refuses git operations against them, CLI verbs (`invite`, `revoke`, `re-invite-all`) skip them, `project status` flags them with a warning banner. `--force-orphan-projects` on `rekey-pair` also writes the tombstone marker, so projects past the cap can be left behind during a rekey rather than blocking it.

URLs change at re-found (this is the one place in the design where they do). Cross-puddle members must update their remotes to the new project_id and run a fresh `rejoin` ceremony — the founder typically runs `re-invite-all <new_pid>` immediately after re-found to mint the invitations.

**Shipping cadence.** Five slices in tree at 0.35.18–0.35.22: data layer; chained verifier with authority windows; rekey-time hooks + `--force-orphan-projects`; multi-puddle `re-invite-all` + `rejoin` + `PushChain` wire format; `re-found` escape hatch + tombstone marker + the formal [ADR-019 document](../../adr/019-gitmirror-founder-rekey.md).

## Operability

Gitmirror is gated per-envoy via the `swarm.gitmirror.enabled` pattern list in server.yaml (0.41.0+). Both `swarm.enabled` AND `swarm.gitmirror.enabled` must allow the host.

```yaml
swarm:
  enabled: ["*"]
  gitmirror:
    enabled: ["annlap", "plex", "girlslaptop"]
```

When a host falls out of the pattern list, the gitmirror reactor idles on the next check-in (publisher and receiver inert, helper-daemon socket stays bound but refuses new pushes/fetches). Already-held bare repos under `/var/lib/vigo/swarm/gitmirror/{projects,mirrors}/` are preserved — pattern flips are pause/resume, not evict.

The destructive lever moved to the **usercrate `gitmirror: bool` flag** (0.41.0). Removing `gitmirror: true` from a usercrate triggers a scrub of `~/.vigo-gitmirror/` for that user on the next reconcile. Per-user state goes; the root-owned bare repos under `/var/lib/vigo/swarm/gitmirror/` survive — admin cleanup is a separate path. See [swarm Operability](swarm.md#operability--pattern-list-authorization).

Pattern-list changes record tamper-evident audit-chain entries (`swarm.gitmirror.enabled`) under `actor=config.reload` at publish time.

## Related

- [ADR-015 — Unified gitmirror substrate](../../adr/015-unified-gitmirror-substrate.md) — the foundational design (project shape, capability tokens, helper daemon, fan-out, gossip)
- [ADR-019 — Founder-rekey delegation chains](../../adr/019-gitmirror-founder-rekey.md) — the design behind `re-invite-all`, `rejoin`, `re-found`, the delegation chain primitive, and authority windows
- [`vigo swarm gitmirror` CLI reference](../../reference/cli/agent-swarm-gitmirror.md) — operator-facing verb walkthroughs
- [Swarm](swarm.md) — the underlying P2P substrate gitmirror rides
- [Puddle](puddle.md) — per-user puddle identity (ADR-014); the trust unit gitmirror's tokens chain to
- [Cryptsync](cryptsync.md) — the encryption-first sibling subsystem that owns *confidentiality* (gitmirror owns *availability*)
- [`gitmirror` trait reference](../../reference/traits/gitmirror.md) — schema of the per-project entries the agent emits on each check-in (project_id, members, dr_fanout, head_refs, envelope counts)

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
