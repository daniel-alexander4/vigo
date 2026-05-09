---
title: "ADR-016: Lockbox membership on puddle"
---

# ADR-016: Lockbox membership on puddle

**Status:** Complete. Slices 1–4 shipped (CLI restructure + puddle-aware recipient resolution 0.35.7; TOFU puddle-pubkey pinning + unlock auto-hint 0.35.8; puddle-membership certificate 0.35.9; puddle-derived wrapping + puddle auto-bootstrap 0.35.10). The "Eviction is puddle-level" line in the original Decision section is superseded by [ADR-017](017-no-puddle-level-revoke.md). Reserved verbs (`puddle adopt`, `standalone adopt`, `standalone recipients {add,remove,list}`) remain explicitly future-only — surface them when real demand appears.
**Date:** 2026-04-29
**Updated 2026-04-29:** CLI surface restructured — the `--standalone` flag pattern is replaced with `lockbox puddle *` and `lockbox standalone *` subcommand subtrees. Mode-agnostic verbs (`unlock`, `lock`, `encrypt`, `decrypt`, `forget`, `reencrypt`, `leave`, `status`) stay at the top level; mode-specific lifecycle (`init`, `enroll`) lives under the appropriate subtree. The structure makes the dual-mode nature of lockbox immediately visible in `--help` and leaves room for future migration verbs (`puddle adopt`, `standalone adopt`) and standalone-only recipient management (`standalone recipients {add,remove,list}`). Per-envoy-key invariant captured as a Consequences bullet (one short comment in the recipient-resolution path + integration test `recipients_are_per_envoy_not_per_puddle`). Puddle verb references updated to ADR-014's renamed surface (`unlock`/`join` instead of `activate`).
**Updated 2026-05-01 (slice 4):** Wrap-key resolution split into two modes recorded in `~/.lockbox/config.yaml#wrap_mode`. Standalone wraps under a per-envoy passphrase the operator types at `init`/`enroll` and again at `unlock`. Puddle wraps under a key derived from the puddle identity via a deterministic Ed25519 signature over a fixed domain-separated challenge (`b"vigo-lockbox-wrap-v1"`), passed through Argon2id at the same parameters as standalone — the Argon2id stretch is redundant against high-entropy input, but it keeps the on-disk envelope format byte-identical across modes so the version doesn't fork. Puddle-mode `unlock` skips the password prompt and asks the running puddle session helper to sign the challenge; if the helper isn't running, `unlock` fails with a pointer to `vigo swarm puddle unlock` rather than falling back to a prompt. `puddle unlock` auto-runs `lockbox puddle init` inline when `~/.lockbox/identity.age` is absent, using the just-spawned session helper to derive the wrap key — the user gets a working lockbox surface as a single side effect of unlocking their puddle identity. `puddle join` does not auto-run lockbox (the session helper isn't running yet at that point); the next `unlock` after `join` handles it.

**Updated 2026-04-30 (eviction superseded):** The "Eviction is puddle-level" line in the Decision section below is superseded by [ADR-017](017-no-puddle-level-revoke.md). Puddle ships no `revoke` verb; cooperative envoy removal uses the existing `vigo swarm lockbox leave` envelope; adversarial removal (compromise) requires re-initing the puddle, because the puddle keypair itself is the compromised credential. The rest of this ADR's decisions (recipient resolution, onboarding via `puddle unlock`/`join`, per-envoy keys preserved, puddle-cert membership, puddle-derived wrapping) are unchanged.

## Context

Lockbox today treats every envoy as an independent identity: each envoy generates its own age keypair on `vigo swarm lockbox init`, wraps it with a per-envoy passphrase, and gossips its pubkey to other envoys via the recipients-exchange protocol. The recipients list on each envoy grows with every new envoy in the user's set; rotating the list when an envoy joins or leaves requires per-pair gossip; eviction is via signed LEAVE envelopes per-envoy.

The pain is operational, not security. Per-envoy keys provide compartmentalization that's largely illusory (every envoy can decrypt all content because content is encrypted to all envoys' pubkeys), but the per-envoy gossip / per-envoy enrollment / per-envoy passphrase ceremony is real friction. With puddle (ADR-014) providing a per-user identity primitive, lockbox's **membership layer** can be replaced with puddle-aware queries. The **encryption keys** stay per-envoy — the security delta of unifying them is genuinely nil per the analysis, but per-envoy keys preserve a marginal compartmentalization property and avoid concentrating risk on a single puddle key. Smaller refactor; same UX win.

## Decision

### What changes

- **Recipients are computed from puddle membership, not pairwise gossip.** When dan-on-envoy-A encrypts a file in `~/lockbox/`, the agent looks up dan's puddle membership (via the puddle primitive) and encrypts to every per-envoy age pubkey advertised by envoys in dan's puddle. The recipient set is "every envoy in puddle P" expressed as their per-envoy age pubkeys.
- **Onboarding becomes `puddle unlock` / `puddle join`-driven.** A new envoy joining dan's puddle runs `vigo swarm puddle join --from <host>` (peer-pair) or `vigo swarm puddle unlock` (after manual scp), per ADR-014. The agent then auto-runs `lockbox puddle init` to generate that envoy's per-envoy age key, registers the pubkey with the puddle, and the envoy starts participating in lockbox. The user does not run separate `lockbox` lifecycle verbs.
- **Eviction is puddle-level.** Removing an envoy from the user's puddle (via puddle's lock-and-revoke flow) automatically removes its age pubkey from lockbox's effective recipient set on every other puddle envoy. No separate per-envoy LEAVE envelope ceremony.
- **Admin eviction (`vigocli swarm lockbox evict`) gates on puddle membership.** Today's admin-eviction path stays; what changes is the trust model for who's a "member."

### What stays the same

- **Per-envoy age keys.** Each envoy still generates its own age identity at `~/.lockbox/identity.age`, wrapped with a per-envoy passphrase or with the puddle passphrase if the user prefers — this is a CLI flag at `lockbox puddle init` / `lockbox standalone init` time. Compartmentalization preserved; no single-puddle-key blast radius for lockbox content.
- **`~/lockbox/` content directory.** Files at rest are age ciphertext; auto-encrypt-on-write per ADR-012 still applies; the watcher / pusher / reactor pipeline is unchanged.
- **Wire format.** Push payloads are age-encrypted to recipients, transmitted on the swarm peer port (1531) over mTLS. The encoding doesn't change; only the recipient-list resolution changes.
- **Mode-agnostic CLI verbs.** `vigo swarm lockbox {unlock,lock,encrypt,decrypt,forget,reencrypt,leave,status}` are unchanged in behavior and live at the top level — these don't care which mode is active and are the verbs users type every day.

### CLI surface

The `--standalone` flag pattern is replaced with subcommand subtrees that make mode an explicit, structural property:

```
vigo swarm lockbox                       (mode-agnostic top-level verbs)
  ├── unlock          daily: unwrap age key, populate session helper
  ├── lock            daily: zero in-memory key
  ├── encrypt <file>  manual encrypt to current recipients
  ├── decrypt <file>  manual decrypt
  ├── forget          clear local state
  ├── reencrypt       re-encrypt corpus to current recipients
  ├── leave           emit LEAVE envelope, drop participation
  ├── status          auto-detect mode, show local state
  │
  ├── puddle/                               (puddle-mode-specific lifecycle)
  │   └── init        manual puddle-mode init (auto-run by `puddle join` / `puddle unlock`)
  │
  └── standalone/                          (standalone-mode-specific lifecycle)
      ├── init                opt out of puddle mode; per-envoy keypair, no puddle registration
      └── enroll              gossip-mesh registration
```

Reserved structure for future verbs:

- `lockbox puddle adopt` — migrate from standalone to puddle mode (re-register existing per-envoy age key under puddle membership).
- `lockbox standalone adopt` — migrate from puddle to standalone mode (drop puddle membership, fall back to pairwise gossip).
- `lockbox standalone recipients {add,remove,list}` — manual recipient control for strict-standalone deployments.

Asymmetry note: mode-agnostic verbs at the top level + standalone explicitly under its own subtree + puddle under its own subtree. The pattern is symmetric (both modes have their own subtrees) without forcing daily verbs to gain a `puddle` qualifier they don't need. Puddle mode is the path of least resistance for the common case; standalone is a discoverable opt-out for users who want it.

### Bootstrap order

For a new user setting up Vigo from scratch:

1. Enroll first envoy in Vigo (existing flow).
2. `vigo swarm puddle init` — generates puddle keypair, sets passphrase.
3. `vigo swarm puddle unlock` on the same envoy — auto-runs `lockbox puddle init`. Puddle identity active; envoy is the sole member.
4. Onboard second envoy via peer-pair (preferred):
   - On envoy A: `vigo swarm puddle pair` — prints pairing code.
   - On envoy B: `vigo swarm puddle join --from <A-host>` — receives wrapped blob over swarm mTLS, prompts for passphrase, unwraps, auto-runs `lockbox puddle init`. Both envoys now in puddle; lockbox recipient list auto-updated on both.
   - (Air-gapped fallback: manual `scp ~/.vigo-puddle/identity.wrapped` then `vigo swarm puddle unlock` on B — see ADR-014.)

After step 4, additional envoys onboard via the same `pair` / `join` flow.

### Standalone-lockbox escape hatch

Users who want to run lockbox without puddle (for testing, isolated environments, or to defer the puddle rollout) can run `vigo swarm lockbox standalone init` to generate a per-envoy key without registering with a puddle, then `vigo swarm lockbox standalone enroll` to join the pairwise-gossip mesh. Recipients list falls back to today's pairwise-gossip model. Standalone and puddle-managed lockbox envoys can interoperate as recipients of each other if explicitly cross-listed.

## Alternatives Considered

**Full key unification — share one age key across all the user's envoys.** Tempting and architecturally cleaner; the membership layer becomes trivial. Rejected because losing the wrapped puddle key + passphrase on all envoys becomes an absolute data-loss event for the user's encrypted content. Per-envoy keys mean a single envoy compromise (or single passphrase loss) doesn't kill the whole lockbox corpus. The marginal compartmentalization is worth keeping.

**Leave lockbox entirely alone; build puddle only for swarmgit.** Rejected after consideration. Lockbox's current ceremony (per-envoy `init` + `enroll` + `unlock`) is the loudest UX pain point in the user-facing swarm subsystems, and puddle was sized partly to address it. Walking away from the lockbox win once puddle exists would be leaving real value on the table. The membership-only refactor is bounded enough to ship.

**Migrate lockbox's per-envoy passphrases to the puddle passphrase.** Considered. Each envoy could re-wrap its existing per-envoy age identity under the puddle passphrase on first `puddle activate`. Rejected — adds a one-shot migration step, complicates the rollout, and forces users to either remember the old per-envoy passphrase to do the rewrap or accept a re-init. Cleaner: lockbox's per-envoy passphrase remains separate; users can manually rotate to the puddle passphrase via `vigo swarm lockbox rewrap` if desired.

## Consequences

**Files to modify (agent — Rust):**
- `agent/src/swarm/lockbox/cli.rs` — restructured into `puddle` and `standalone` subcommand subtrees (see CLI surface section above). Mode-agnostic verbs stay at the top level. `puddle unlock` / `puddle join` (per ADR-014) auto-run `lockbox puddle init` if absent.
- `agent/src/swarm/lockbox/peer_pubkey_cache.rs` (or equivalent recipient-list management) — recipient resolution gains a puddle-membership-aware path. Default is "ask puddle for puddle members, encrypt to their **per-envoy** age pubkeys." **Add a short comment in the recipient-resolution function citing this ADR and the per-envoy-keys invariant** — see the dedicated bullet below.
- `agent/src/swarm/lockbox/leaves.rs` — leave-envelope issuance still per-envoy on explicit `leave`, but additionally fires when an envoy is removed from the user's puddle.
- `agent/src/swarm/lockbox/discovery.rs` — multicast announcement payload includes puddle membership signature alongside the per-envoy age pubkey, so peers can verify "this envoy is genuinely in dan's puddle" before adding to their recipient set.

**Per-envoy-keys invariant — code comment + integration test.**

This ADR deliberately preserves per-envoy age keys after introducing puddle identity (see "Context" — single-envoy compromise must not decrypt the whole puddle's corpus). The code-level enforcement of this invariant is twofold:

1. **One short comment** in the recipient-resolution function (post-refactor) citing this ADR. Concrete shape:
   ```rust
   // Recipients are per-envoy age pubkeys, not the puddle pubkey.
   // ADR-016 keeps per-envoy keys deliberately: single-envoy
   // compromise must not decrypt the whole puddle's corpus.
   ```
   No more comments. This is exactly the kind of non-obvious invariant CLAUDE.md says comments are for.

2. **An integration test named `recipients_are_per_envoy_not_per_puddle`** that simulates two envoys in the same puddle, calls the recipient resolution, and asserts the result contains **two distinct age pubkeys** (not one puddle key). The test name itself documents the invariant; anyone who later tries to "simplify" by switching to puddle-key recipients hits a failing test with a name that explains why.

**No server-side changes.**

**No protobuf changes.**

**Documentation:**
- `docs/concepts/lockbox.md` — describe puddle-aware membership. Bootstrap order via peer-pair. The standalone subtree. Per-envoy keys still preserved.
- `docs/reference/cli/agent-swarm-lockbox.md` — `lockbox puddle *` and `lockbox standalone *` subtree structure with per-verb help text.

**Operational:**
- Agent rebuild + cross-compile + redeploy required.
- Z-level version bump (smaller change than ADR-015's swarmgit rework).
- No new dependencies; reuses ADR-014's puddle primitive.

**Estimated weight:** ~20–50 KiB on the agent. Mostly recipient-list-resolution refactor + small additions to discovery and leaves. Smaller than ADR-014 or ADR-015.

**Behavioral consequences:**
- Existing standalone-style lockbox workflows (manual `init` + per-envoy passphrase) continue to work under `lockbox standalone *`. Migration path is "set up puddle, then re-init this envoy's lockbox under puddle mode" — eventually exposable as `lockbox puddle adopt` once the need surfaces. No silent migration; no data loss; the user can choose when to make the switch.
- New users get a single setup ceremony for both subsystems via `puddle unlock` (post-`init`) or `puddle join` (peer-pair onboarding) — both auto-run `lockbox puddle init`.
- Lockbox's compartmentalization (per-envoy keys, single-envoy compromise doesn't break others) is preserved deliberately and enforced in code via the comment + test described above. Puddle provides the membership UX win without the security regression of a shared decryption key.
