---
title: "ADR-016: Cryptsync membership on self-fleet"
---

# ADR-016: Cryptsync membership on self-fleet

**Status:** Accepted (pending implementation)
**Date:** 2026-04-29
**Updated 2026-04-29:** CLI surface restructured — the `--standalone` flag pattern is replaced with `cryptsync fleet *` and `cryptsync standalone *` subcommand subtrees. Mode-agnostic verbs (`unlock`, `lock`, `encrypt`, `decrypt`, `forget`, `reencrypt`, `leave`, `status`) stay at the top level; mode-specific lifecycle (`init`, `enroll`) lives under the appropriate subtree. The structure makes the dual-mode nature of cryptsync immediately visible in `--help` and leaves room for future migration verbs (`fleet adopt`, `standalone adopt`) and standalone-only recipient management (`standalone recipients {add,remove,list}`). Per-envoy-key invariant captured as a Consequences bullet (one short comment in the recipient-resolution path + integration test `recipients_are_per_envoy_not_per_fleet`). Self-fleet verb references updated to ADR-014's renamed surface (`unlock`/`join` instead of `activate`).

## Context

Cryptsync today treats every envoy as an independent identity: each envoy generates its own age keypair on `vigo swarm cryptsync init`, wraps it with a per-envoy passphrase, and gossips its pubkey to other envoys via the recipients-exchange protocol. The recipients list on each envoy grows with every new envoy in the user's set; rotating the list when an envoy joins or leaves requires per-pair gossip; eviction is via signed LEAVE envelopes per-envoy.

The pain is operational, not security. Per-envoy keys provide compartmentalization that's largely illusory (every envoy can decrypt all content because content is encrypted to all envoys' pubkeys), but the per-envoy gossip / per-envoy enrollment / per-envoy passphrase ceremony is real friction. With self-fleet (ADR-014) providing a per-user identity primitive, cryptsync's **membership layer** can be replaced with fleet-aware queries. The **encryption keys** stay per-envoy — the security delta of unifying them is genuinely nil per the analysis, but per-envoy keys preserve a marginal compartmentalization property and avoid concentrating risk on a single fleet key. Smaller refactor; same UX win.

## Decision

### What changes

- **Recipients are computed from fleet membership, not pairwise gossip.** When dan-on-envoy-A encrypts a file in `~/cryptsync/`, the agent looks up dan's fleet membership (via the self-fleet primitive) and encrypts to every per-envoy age pubkey advertised by envoys in dan's fleet. The recipient set is "every envoy in fleet F" expressed as their per-envoy age pubkeys.
- **Onboarding becomes `self-fleet unlock` / `self-fleet join`-driven.** A new envoy joining dan's fleet runs `vigo swarm self-fleet join --from <host>` (peer-pair) or `vigo swarm self-fleet unlock` (after manual scp), per ADR-014. The agent then auto-runs `cryptsync fleet init` to generate that envoy's per-envoy age key, registers the pubkey with the fleet, and the envoy starts participating in cryptsync. The user does not run separate `cryptsync` lifecycle verbs.
- **Eviction is fleet-level.** Removing an envoy from the user's fleet (via self-fleet's lock-and-revoke flow) automatically removes its age pubkey from cryptsync's effective recipient set on every other fleet envoy. No separate per-envoy LEAVE envelope ceremony.
- **Admin eviction (`vigocli swarm cryptsync evict`) gates on fleet membership.** Today's admin-eviction path stays; what changes is the trust model for who's a "member."

### What stays the same

- **Per-envoy age keys.** Each envoy still generates its own age identity at `~/.cryptsync/identity.age`, wrapped with a per-envoy passphrase or with the fleet passphrase if the user prefers — this is a CLI flag at `cryptsync fleet init` / `cryptsync standalone init` time. Compartmentalization preserved; no single-fleet-key blast radius for cryptsync content.
- **`~/cryptsync/` content directory.** Files at rest are age ciphertext; auto-encrypt-on-write per ADR-012 still applies; the watcher / pusher / reactor pipeline is unchanged.
- **Wire format.** Push payloads are age-encrypted to recipients, transmitted on the swarm peer port (1531) over mTLS. The encoding doesn't change; only the recipient-list resolution changes.
- **Mode-agnostic CLI verbs.** `vigo swarm cryptsync {unlock,lock,encrypt,decrypt,forget,reencrypt,leave,status}` are unchanged in behavior and live at the top level — these don't care which mode is active and are the verbs users type every day.

### CLI surface

The `--standalone` flag pattern is replaced with subcommand subtrees that make mode an explicit, structural property:

```
vigo swarm cryptsync                       (mode-agnostic top-level verbs)
  ├── unlock          daily: unwrap age key, populate session helper
  ├── lock            daily: zero in-memory key
  ├── encrypt <file>  manual encrypt to current recipients
  ├── decrypt <file>  manual decrypt
  ├── forget          clear local state
  ├── reencrypt       re-encrypt corpus to current recipients
  ├── leave           emit LEAVE envelope, drop participation
  ├── status          auto-detect mode, show local state
  │
  ├── fleet/                               (fleet-mode-specific lifecycle)
  │   └── init        manual fleet-mode init (auto-run by `self-fleet join` / `self-fleet unlock`)
  │
  └── standalone/                          (standalone-mode-specific lifecycle)
      ├── init                opt out of fleet mode; per-envoy keypair, no fleet registration
      └── enroll              gossip-mesh registration
```

Reserved structure for future verbs:

- `cryptsync fleet adopt` — migrate from standalone to fleet mode (re-register existing per-envoy age key under fleet membership).
- `cryptsync standalone adopt` — migrate from fleet to standalone mode (drop fleet membership, fall back to pairwise gossip).
- `cryptsync standalone recipients {add,remove,list}` — manual recipient control for strict-standalone deployments.

Asymmetry note: mode-agnostic verbs at the top level + standalone explicitly under its own subtree + fleet under its own subtree. The pattern is symmetric (both modes have their own subtrees) without forcing daily verbs to gain a `fleet` qualifier they don't need. Fleet mode is the path of least resistance for the common case; standalone is a discoverable opt-out for users who want it.

### Bootstrap order

For a new user setting up Vigo from scratch:

1. Enroll first envoy in Vigo (existing flow).
2. `vigo swarm self-fleet init` — generates fleet keypair, sets passphrase.
3. `vigo swarm self-fleet unlock` on the same envoy — auto-runs `cryptsync fleet init`. Fleet identity active; envoy is the sole member.
4. Onboard second envoy via peer-pair (preferred):
   - On envoy A: `vigo swarm self-fleet pair` — prints pairing code.
   - On envoy B: `vigo swarm self-fleet join --from <A-host>` — receives wrapped blob over swarm mTLS, prompts for passphrase, unwraps, auto-runs `cryptsync fleet init`. Both envoys now in fleet; cryptsync recipient list auto-updated on both.
   - (Air-gapped fallback: manual `scp ~/.vigo-fleet/identity.wrapped` then `vigo swarm self-fleet unlock` on B — see ADR-014.)

After step 4, additional envoys onboard via the same `pair` / `join` flow.

### Standalone-cryptsync escape hatch

Users who want to run cryptsync without self-fleet (for testing, isolated environments, or to defer the self-fleet rollout) can run `vigo swarm cryptsync standalone init` to generate a per-envoy key without registering with a fleet, then `vigo swarm cryptsync standalone enroll` to join the pairwise-gossip mesh. Recipients list falls back to today's pairwise-gossip model. Standalone and fleet-managed cryptsync envoys can interoperate as recipients of each other if explicitly cross-listed.

## Alternatives Considered

**Full key unification — share one age key across all the user's envoys.** Tempting and architecturally cleaner; the membership layer becomes trivial. Rejected because losing the wrapped fleet key + passphrase on all envoys becomes an absolute data-loss event for the user's encrypted content. Per-envoy keys mean a single envoy compromise (or single passphrase loss) doesn't kill the whole cryptsync corpus. The marginal compartmentalization is worth keeping.

**Leave cryptsync entirely alone; build self-fleet only for gitmirror.** Rejected after consideration. Cryptsync's current ceremony (per-envoy `init` + `enroll` + `unlock`) is the loudest UX pain point in the user-facing swarm subsystems, and self-fleet was sized partly to address it. Walking away from the cryptsync win once self-fleet exists would be leaving real value on the table. The membership-only refactor is bounded enough to ship.

**Migrate cryptsync's per-envoy passphrases to the fleet passphrase.** Considered. Each envoy could re-wrap its existing per-envoy age identity under the fleet passphrase on first `self-fleet activate`. Rejected — adds a one-shot migration step, complicates the rollout, and forces users to either remember the old per-envoy passphrase to do the rewrap or accept a re-init. Cleaner: cryptsync's per-envoy passphrase remains separate; users can manually rotate to the fleet passphrase via `vigo swarm cryptsync rewrap` if desired.

## Consequences

**Files to modify (agent — Rust):**
- `agent/src/swarm/cryptsync/cli.rs` — restructured into `fleet` and `standalone` subcommand subtrees (see CLI surface section above). Mode-agnostic verbs stay at the top level. `self-fleet unlock` / `self-fleet join` (per ADR-014) auto-run `cryptsync fleet init` if absent.
- `agent/src/swarm/cryptsync/peer_pubkey_cache.rs` (or equivalent recipient-list management) — recipient resolution gains a fleet-membership-aware path. Default is "ask self-fleet for fleet members, encrypt to their **per-envoy** age pubkeys." **Add a short comment in the recipient-resolution function citing this ADR and the per-envoy-keys invariant** — see the dedicated bullet below.
- `agent/src/swarm/cryptsync/leaves.rs` — leave-envelope issuance still per-envoy on explicit `leave`, but additionally fires when an envoy is removed from the user's fleet.
- `agent/src/swarm/cryptsync/discovery.rs` — multicast announcement payload includes fleet membership signature alongside the per-envoy age pubkey, so peers can verify "this envoy is genuinely in dan's fleet" before adding to their recipient set.

**Per-envoy-keys invariant — code comment + integration test.**

This ADR deliberately preserves per-envoy age keys after introducing fleet identity (see "Context" — single-envoy compromise must not decrypt the whole fleet's corpus). The code-level enforcement of this invariant is twofold:

1. **One short comment** in the recipient-resolution function (post-refactor) citing this ADR. Concrete shape:
   ```rust
   // Recipients are per-envoy age pubkeys, not the fleet pubkey.
   // ADR-016 keeps per-envoy keys deliberately: single-envoy
   // compromise must not decrypt the whole fleet's corpus.
   ```
   No more comments. This is exactly the kind of non-obvious invariant CLAUDE.md says comments are for.

2. **An integration test named `recipients_are_per_envoy_not_per_fleet`** that simulates two envoys in the same fleet, calls the recipient resolution, and asserts the result contains **two distinct age pubkeys** (not one fleet key). The test name itself documents the invariant; anyone who later tries to "simplify" by switching to fleet-key recipients hits a failing test with a name that explains why.

**No server-side changes.**

**No protobuf changes.**

**Documentation:**
- `docs/concepts/cryptsync.md` — describe fleet-aware membership. Bootstrap order via peer-pair. The standalone subtree. Per-envoy keys still preserved.
- `docs/reference/cli/agent-swarm-cryptsync.md` — `cryptsync fleet *` and `cryptsync standalone *` subtree structure with per-verb help text.

**Operational:**
- Agent rebuild + cross-compile + redeploy required.
- Z-level version bump (smaller change than ADR-015's gitmirror rework).
- No new dependencies; reuses ADR-014's self-fleet primitive.

**Estimated weight:** ~20–50 KiB on the agent. Mostly recipient-list-resolution refactor + small additions to discovery and leaves. Smaller than ADR-014 or ADR-015.

**Behavioral consequences:**
- Existing standalone-style cryptsync workflows (manual `init` + per-envoy passphrase) continue to work under `cryptsync standalone *`. Migration path is "set up self-fleet, then re-init this envoy's cryptsync under fleet mode" — eventually exposable as `cryptsync fleet adopt` once the need surfaces. No silent migration; no data loss; the user can choose when to make the switch.
- New users get a single setup ceremony for both subsystems via `self-fleet unlock` (post-`init`) or `self-fleet join` (peer-pair onboarding) — both auto-run `cryptsync fleet init`.
- Cryptsync's compartmentalization (per-envoy keys, single-envoy compromise doesn't break others) is preserved deliberately and enforced in code via the comment + test described above. Self-fleet provides the membership UX win without the security regression of a shared decryption key.
