---
title: "ADR-016: Cryptsync membership on self-fleet"
---

# ADR-016: Cryptsync membership on self-fleet

**Status:** Accepted (pending implementation)
**Date:** 2026-04-29

## Context

Cryptsync today treats every envoy as an independent identity: each envoy generates its own age keypair on `vigo swarm cryptsync init`, wraps it with a per-envoy passphrase, and gossips its pubkey to other envoys via the recipients-exchange protocol. The recipients list on each envoy grows with every new envoy in the user's set; rotating the list when an envoy joins or leaves requires per-pair gossip; eviction is via signed LEAVE envelopes per-envoy.

The pain is operational, not security. Per-envoy keys provide compartmentalization that's largely illusory (every envoy can decrypt all content because content is encrypted to all envoys' pubkeys), but the per-envoy gossip / per-envoy enrollment / per-envoy passphrase ceremony is real friction. With self-fleet (ADR-014) providing a per-user identity primitive, cryptsync's **membership layer** can be replaced with fleet-aware queries. The **encryption keys** stay per-envoy — the security delta of unifying them is genuinely nil per the analysis, but per-envoy keys preserve a marginal compartmentalization property and avoid concentrating risk on a single fleet key. Smaller refactor; same UX win.

## Decision

### What changes

- **Recipients are computed from fleet membership, not pairwise gossip.** When dan-on-envoy-A encrypts a file in `~/cryptsync/`, the agent looks up dan's fleet membership (via the self-fleet primitive) and encrypts to every per-envoy age pubkey advertised by envoys in dan's fleet. The recipient set is "every envoy in fleet F" expressed as their per-envoy age pubkeys.
- **Onboarding becomes `self-fleet activate`-driven.** A new envoy joining dan's fleet runs `vigo swarm self-fleet activate` (per ADR-014). The agent then auto-runs `cryptsync init` to generate that envoy's per-envoy age key, registers the pubkey with the fleet, and the envoy starts participating in cryptsync. The user does not run separate `cryptsync init` / `enroll` verbs.
- **Eviction is fleet-level.** Removing an envoy from the user's fleet (via self-fleet's deactivate-and-revoke flow) automatically removes its age pubkey from cryptsync's effective recipient set on every other fleet envoy. No separate per-envoy LEAVE envelope ceremony.
- **Admin eviction (`vigocli swarm cryptsync evict`) gates on fleet membership.** Today's admin-eviction path stays; what changes is the trust model for who's a "member."

### What stays the same

- **Per-envoy age keys.** Each envoy still generates its own age identity at `~/.cryptsync/identity.age`, wrapped with a per-envoy passphrase or with the fleet passphrase if the user prefers — this is a CLI flag at `cryptsync init` time. Compartmentalization preserved; no single-fleet-key blast radius for cryptsync content.
- **`~/cryptsync/` content directory.** Files at rest are age ciphertext; auto-encrypt-on-write per ADR-012 still applies; the watcher / pusher / reactor pipeline is unchanged.
- **Wire format.** Push payloads are age-encrypted to recipients, transmitted on the swarm peer port (1531) over mTLS. The encoding doesn't change; only the recipient-list resolution changes.
- **Existing CLI verbs.** `vigo swarm cryptsync {init,enroll,unlock,lock,encrypt,decrypt,forget,leave,reencrypt,status}` continue to work. `init` becomes implicit during `self-fleet activate` for users who want the seamless flow; explicit `init` is preserved for users who want to enroll cryptsync without self-fleet.

### Bootstrap order

For a new user setting up Vigo from scratch:

1. Enroll first envoy in Vigo (existing flow).
2. `vigo swarm self-fleet init` — generates fleet keypair, sets passphrase.
3. `vigo swarm self-fleet activate` on the same envoy — auto-runs `cryptsync init`. Fleet identity active; envoy is the sole member.
4. Manual `scp ~/.vigo-fleet/identity.wrapped` to second envoy.
5. `vigo swarm self-fleet activate` on second envoy — prompts for passphrase, unwraps, auto-runs `cryptsync init` on this envoy. Both envoys now in fleet; cryptsync recipient list auto-updated on both.

After step 5, additional envoys can use the same passphrase + wrapped key file delivered via cryptsync (since cryptsync is now operational), removing the manual scp from subsequent setups.

### Standalone-cryptsync escape hatch

Users who want to run cryptsync without self-fleet (for testing, isolated environments, or to defer the self-fleet rollout) can run `vigo swarm cryptsync init --standalone` to generate a per-envoy key without registering with a fleet. Recipients list falls back to today's pairwise-gossip model. Standalone and self-fleet-managed cryptsync envoys can interoperate as recipients of each other if explicitly cross-listed.

## Alternatives Considered

**Full key unification — share one age key across all the user's envoys.** Tempting and architecturally cleaner; the membership layer becomes trivial. Rejected because losing the wrapped fleet key + passphrase on all envoys becomes an absolute data-loss event for the user's encrypted content. Per-envoy keys mean a single envoy compromise (or single passphrase loss) doesn't kill the whole cryptsync corpus. The marginal compartmentalization is worth keeping.

**Leave cryptsync entirely alone; build self-fleet only for gitmirror.** Rejected after consideration. Cryptsync's current ceremony (per-envoy `init` + `enroll` + `unlock`) is the loudest UX pain point in the user-facing swarm subsystems, and self-fleet was sized partly to address it. Walking away from the cryptsync win once self-fleet exists would be leaving real value on the table. The membership-only refactor is bounded enough to ship.

**Migrate cryptsync's per-envoy passphrases to the fleet passphrase.** Considered. Each envoy could re-wrap its existing per-envoy age identity under the fleet passphrase on first `self-fleet activate`. Rejected — adds a one-shot migration step, complicates the rollout, and forces users to either remember the old per-envoy passphrase to do the rewrap or accept a re-init. Cleaner: cryptsync's per-envoy passphrase remains separate; users can manually rotate to the fleet passphrase via `vigo swarm cryptsync rewrap` if desired.

## Consequences

**Files to modify (agent — Rust):**
- `agent/src/swarm/cryptsync/cli.rs` — new `--standalone` flag on `init`; `activate` (in self-fleet) auto-runs `init` if absent; existing verbs preserved.
- `agent/src/swarm/cryptsync/peer_pubkey_cache.rs` (or equivalent recipient-list management) — recipient resolution gains a fleet-membership-aware path. Default is "ask self-fleet for fleet members, encrypt to their per-envoy age pubkeys."
- `agent/src/swarm/cryptsync/leaves.rs` — leave-envelope issuance still per-envoy on explicit `leave`, but additionally fires when an envoy is removed from the user's fleet.
- `agent/src/swarm/cryptsync/discovery.rs` — multicast announcement payload includes fleet membership signature alongside the per-envoy age pubkey, so peers can verify "this envoy is genuinely in dan's fleet" before adding to their recipient set.

**No server-side changes.**

**No protobuf changes.**

**Documentation:**
- `docs/concepts/cryptsync.md` — describe fleet-aware membership. Bootstrap order. The standalone fallback. Per-envoy keys still preserved.
- `docs/reference/cli/agent-swarm-cryptsync.md` — `--standalone` flag.

**Operational:**
- Agent rebuild + cross-compile + redeploy required.
- Z-level version bump (smaller change than ADR-015's gitmirror rework).
- No new dependencies; reuses ADR-014's self-fleet primitive.

**Estimated weight:** ~20–50 KiB on the agent. Mostly recipient-list-resolution refactor + small additions to discovery and leaves. Smaller than ADR-014 or ADR-015.

**Behavioral consequences:**
- Existing standalone-style cryptsync workflows (manual `init` + per-envoy passphrase) continue to work via `--standalone`. Migration path is "set up self-fleet, then re-init this envoy's cryptsync via self-fleet." No silent migration; no data loss; the user can choose when to make the switch.
- New users get a single setup ceremony for both subsystems via `self-fleet activate`.
- Cryptsync's compartmentalization (per-envoy keys, single-envoy compromise doesn't break others) is preserved deliberately. Self-fleet provides the membership UX win without the security regression of a shared decryption key.
