---
title: "ADR-021: Lockbox lifecycle absorbed into puddle"
---

# ADR-021: Lockbox lifecycle absorbed into puddle

**Status:** Decided 2026-05-07. Implementation in 0.40.0 alongside [ADR-020](020-lockbox-puddle-only.md).

**Date:** 2026-05-07

## Context

After [ADR-020](020-lockbox-puddle-only.md) retires standalone mode, lockbox's mesh becomes identical to the puddle. The two concepts that ADR-016 kept distinct — "puddle membership" (trust unit) and "lockbox mesh" (encrypted-sync participants) — collapse into one set: the same puddle members. The subset relationship that allowed for "puddle member who opted out of lockbox via `lockbox: true = false` in usercrate" is the only thing keeping the lockbox subtree distinct, and that flexibility costs more than it buys.

The lockbox subtree today carries three lifecycle verbs that exist as residue from the standalone-mode era:

- `lockbox init` — enroll this envoy for lockbox. Today it's the per-envoy bootstrap; under puddle mode it's the moment the per-envoy age key gets generated.
- `lockbox leave` — sign a LEAVE envelope, gossip to peers, scrub local lockbox state. The envoy stays in the puddle.
- `lockbox forget <host>` — locally remove a peer from `peers.json` and recipients. Per-host eviction without leaving lockbox.

All three operate on **per-capability lifecycle within the puddle**. With lockbox now mandatory on puddle membership, the per-capability layer disappears: leaving lockbox without leaving the puddle is no longer a supported topology, and a peer evicted from lockbox is meaningfully evicted from the puddle's encrypted-sync circle (which, under ADR-020, is the puddle itself).

The verb tree should reflect this. Lifecycle belongs to puddle; the lockbox subtree should describe content operations on files and keys, not membership transitions.

## Decision

**Lockbox's lifecycle layer is fully absorbed into puddle.** The lockbox subtree shrinks to **content-domain verbs only**.

### What moves to puddle

- `lockbox init` — eliminated. Lockbox enrollment becomes an internal step of `puddle init` and `puddle join`. After puddle membership is established, the agent generates the per-envoy age key, writes `~/.lockbox/identity.age`, seeds `recipients.txt` with the local pubkey, and the envoy starts participating in lockbox. No separate verb to run.
- `lockbox leave` — eliminated. Replaced by **`puddle leave`**: leave the puddle on this envoy, which signs a LEAVE envelope (reusing lockbox's existing apply_leave plumbing for the cross-envoy cleanup), gossips it to puddle peers, then scrubs `~/.vigo-puddle/`, `~/.lockbox/`, `~/lockbox/`, and `~/.vigo-swarmgit/`. Local-only when no peers exist (single-envoy puddle).
- `lockbox forget <host>` — eliminated. Replaced by **`puddle evict <host>`**: cross-envoy admin op to evict a hostname from the puddle. Triggers apply_leave on the target host (cleans up lockbox state, regenerates recipients), drops from peer tables on remaining members. The existing `vigocli swarm lockbox evict` admin path is renamed to dispatch this verb.

### What stays in lockbox

Content-domain verbs only:

```
vigo swarm lockbox
  ├── unlock          daily: unwrap age key via puddle helper
  ├── lock            daily: zero in-memory key
  ├── encrypt <file>  manual encrypt to current recipients
  ├── decrypt <file>  manual decrypt
  ├── reencrypt       re-encrypt corpus to current recipients
  ├── resurrect       override an active tombstone
  ├── purge           local wipe of lockbox content + identity (no gossip)
  └── status          show lockbox-specific local state
```

`purge` retains its meaning as a **content-recovery verb**: wipe local lockbox state without gossiping. Useful when an envoy's lockbox corpus is corrupted and the operator wants to reset without leaving the puddle. Distinct from `puddle leave` (which gossips a clean exit).

### Puddle's verb tree (post-absorption)

```
vigo swarm puddle
  ├── init                 create puddle keypair (auto-enrolls lockbox)
  ├── pair                 issue 5-minute single-use pair code
  ├── join --from <host>   claim pair code (auto-enrolls lockbox)
  ├── leave                leave puddle on this envoy + cleanup
  ├── unlock               start session helper
  ├── lock                 stop session helper
  ├── status               show puddle + lockbox + project list state
  ├── members              list puddle members
  ├── evict <host>         admin: cross-envoy evict
  ├── pubkey               print local puddle pubkey
  ├── rotate-passphrase    rotate the passphrase wrapping the puddle keypair
  ├── rekey-pair           ADR-018: issue rekey pair code
  └── rekey                ADR-018: claim rekey pair code
```

`pubkey` already exists; `members`, `unlock`, `lock`, `pair`, `join`, `init`, `status`, `rotate-passphrase`, `rekey-pair`, `rekey` already exist. New verbs introduced by 0.40.0: **`leave`** and **`evict`**.

### Per-envoy opt-out is removed

The usercrate `lockbox: true` flag is retired. Lockbox is mandatory on puddle membership; per-envoy opt-out is gone. An operator who wants an envoy in the puddle but not running lockbox should not put that envoy in the puddle — `lockbox` is no longer separable from puddle membership.

The cost of forced enrollment: a wrapped age key on disk (~1 KB), an empty `~/lockbox/` directory, and peer announces that fan out nothing if no files are written. Trivial.

### Swarmgit lifecycle is NOT absorbed

Swarmgit operates at a different granularity than lockbox. Lockbox is "one mesh per puddle"; swarmgit is "many projects per puddle, each with its own membership stack and dr_scope policy." Project lifecycle (`project init`, `project join`, `project leave`, `project invite`, etc.) lives at the project level — below puddle in the trust hierarchy but with its own scope axis. Folding `swarmgit project *` verbs under `puddle` would either misalign (puddle shouldn't own per-project state) or just rename verbs without simplifying anything.

What swarmgit gets from this work: the puddle session helper (started by `puddle unlock`) becomes the canonical "am I authorized to do puddle-anchored things" gate. `git push swarmgit://...` rides on `puddle unlock` like lockbox does. Already true today; this ADR formalizes the coupling.

## Consequences

- **Lockbox subtree is content-only.** No more "lifecycle vs daily-use" split inside lockbox. The verbs that survive all operate on files or keys, not on membership.
- **Puddle is the single lifecycle home for everything puddle-anchored.** Operators learning the system see a clear hierarchy: `swarm` → `puddle` → `{lockbox, swarmgit}`. Lifecycle questions go to puddle; content questions go to the relevant subsystem.
- **Operator UX simplification.** "How do I leave the puddle?" → `puddle leave`. "How do I evict a peer?" → `puddle evict <host>`. Single-step for either operation, with lockbox state cleanup happening as a side effect.
- **Lockbox mesh = puddle.** No subset distinction; no "lockbox member who isn't a puddle member" case to reason about. Test surface narrows accordingly.
- **`puddle leave` and `puddle evict` are new verb implementations**, not renames. They reuse the lockbox envelope/gossip plumbing for cross-envoy cleanup but carry additional cleanup responsibilities (puddle keypair, swarmgit project state). Estimated implementation effort: ~half-day for `puddle leave` (reuse most of `lockbox leave`'s code); ~half-day for `puddle evict` (reuse `apply_leave` chain).
- **No `lockbox: true` opt-in flag.** Usercrate schema simplifies; modlint's `lockboxWithoutHomeRule` retires.
- **No partial-leave granularity.** An operator who wants to keep swarmgit but stop lockbox has no path post-0.40.0. This is a deliberate trade — the per-capability flexibility was theoretical (no operator was using it productively), and removing it earns a clean mental model.

## Alternatives considered

- **Keep `lockbox leave` for partial-leave (swarmgit without lockbox).** Preserves the per-capability granularity but leaves the lockbox subtree with a residue that contradicts ADR-020's "lockbox is puddle-only" framing. The use case (puddle member who actively wants to drop lockbox but keep swarmgit) is hypothetical; no operator has asked for it. Rejected.
- **Add scoped variants under puddle (`puddle leave-lockbox`).** Keeps lifecycle under the puddle subtree but with awkward compound verbs. No simplification — just renames. Rejected.
- **Auto-init puddle on `lockbox init`.** A previous discussion considered making `lockbox init` auto-create a puddle when missing. Rejected because it couples two trust-anchor-affecting operations in a way that creates surprising failure modes (operator accidentally minting a new puddle pubkey). The current design is the inverse — puddle owns the trust anchor, lockbox follows.

## Related

- [ADR-014](014-puddle-primitive.md) — puddle primitive
- [ADR-016](016-lockbox-membership-on-puddle.md) — lockbox membership on puddle (recipient-resolution decisions stand)
- [ADR-020](020-lockbox-puddle-only.md) — sibling decision; standalone retire
