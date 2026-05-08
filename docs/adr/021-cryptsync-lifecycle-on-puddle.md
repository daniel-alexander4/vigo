---
title: "ADR-021: Cryptsync lifecycle absorbed into puddle"
---

# ADR-021: Cryptsync lifecycle absorbed into puddle

**Status:** Decided 2026-05-07. Implementation in 0.40.0 alongside [ADR-020](020-cryptsync-puddle-only.md).

**Date:** 2026-05-07

## Context

After [ADR-020](020-cryptsync-puddle-only.md) retires standalone mode, cryptsync's mesh becomes identical to the puddle. The two concepts that ADR-016 kept distinct — "puddle membership" (trust unit) and "cryptsync mesh" (encrypted-sync participants) — collapse into one set: the same puddle members. The subset relationship that allowed for "puddle member who opted out of cryptsync via `cryptsync: true = false` in usercrate" is the only thing keeping the cryptsync subtree distinct, and that flexibility costs more than it buys.

The cryptsync subtree today carries three lifecycle verbs that exist as residue from the standalone-mode era:

- `cryptsync init` — enroll this envoy for cryptsync. Today it's the per-envoy bootstrap; under puddle mode it's the moment the per-envoy age key gets generated.
- `cryptsync leave` — sign a LEAVE envelope, gossip to peers, scrub local cryptsync state. The envoy stays in the puddle.
- `cryptsync forget <host>` — locally remove a peer from `peers.json` and recipients. Per-host eviction without leaving cryptsync.

All three operate on **per-capability lifecycle within the puddle**. With cryptsync now mandatory on puddle membership, the per-capability layer disappears: leaving cryptsync without leaving the puddle is no longer a supported topology, and a peer evicted from cryptsync is meaningfully evicted from the puddle's encrypted-sync circle (which, under ADR-020, is the puddle itself).

The verb tree should reflect this. Lifecycle belongs to puddle; the cryptsync subtree should describe content operations on files and keys, not membership transitions.

## Decision

**Cryptsync's lifecycle layer is fully absorbed into puddle.** The cryptsync subtree shrinks to **content-domain verbs only**.

### What moves to puddle

- `cryptsync init` — eliminated. Cryptsync enrollment becomes an internal step of `puddle init` and `puddle join`. After puddle membership is established, the agent generates the per-envoy age key, writes `~/.cryptsync/identity.age`, seeds `recipients.txt` with the local pubkey, and the envoy starts participating in cryptsync. No separate verb to run.
- `cryptsync leave` — eliminated. Replaced by **`puddle leave`**: leave the puddle on this envoy, which signs a LEAVE envelope (reusing cryptsync's existing apply_leave plumbing for the cross-envoy cleanup), gossips it to puddle peers, then scrubs `~/.vigo-puddle/`, `~/.cryptsync/`, `~/cryptsync/`, and `~/.vigo-gitmirror/`. Local-only when no peers exist (single-envoy puddle).
- `cryptsync forget <host>` — eliminated. Replaced by **`puddle evict <host>`**: cross-envoy admin op to evict a hostname from the puddle. Triggers apply_leave on the target host (cleans up cryptsync state, regenerates recipients), drops from peer tables on remaining members. The existing `vigocli swarm cryptsync evict` admin path is renamed to dispatch this verb.

### What stays in cryptsync

Content-domain verbs only:

```
vigo swarm cryptsync
  ├── unlock          daily: unwrap age key via puddle helper
  ├── lock            daily: zero in-memory key
  ├── encrypt <file>  manual encrypt to current recipients
  ├── decrypt <file>  manual decrypt
  ├── reencrypt       re-encrypt corpus to current recipients
  ├── resurrect       override an active tombstone
  ├── purge           local wipe of cryptsync content + identity (no gossip)
  └── status          show cryptsync-specific local state
```

`purge` retains its meaning as a **content-recovery verb**: wipe local cryptsync state without gossiping. Useful when an envoy's cryptsync corpus is corrupted and the operator wants to reset without leaving the puddle. Distinct from `puddle leave` (which gossips a clean exit).

### Puddle's verb tree (post-absorption)

```
vigo swarm puddle
  ├── init                 create puddle keypair (auto-enrolls cryptsync)
  ├── pair                 issue 5-minute single-use pair code
  ├── join --from <host>   claim pair code (auto-enrolls cryptsync)
  ├── leave                leave puddle on this envoy + cleanup
  ├── unlock               start session helper
  ├── lock                 stop session helper
  ├── status               show puddle + cryptsync + project list state
  ├── members              list puddle members
  ├── evict <host>         admin: cross-envoy evict
  ├── pubkey               print local puddle pubkey
  ├── rotate-passphrase    rotate the passphrase wrapping the puddle keypair
  ├── rekey-pair           ADR-018: issue rekey pair code
  └── rekey                ADR-018: claim rekey pair code
```

`pubkey` already exists; `members`, `unlock`, `lock`, `pair`, `join`, `init`, `status`, `rotate-passphrase`, `rekey-pair`, `rekey` already exist. New verbs introduced by 0.40.0: **`leave`** and **`evict`**.

### Per-envoy opt-out is removed

The usercrate `cryptsync: true` flag is retired. Cryptsync is mandatory on puddle membership; per-envoy opt-out is gone. An operator who wants an envoy in the puddle but not running cryptsync should not put that envoy in the puddle — `cryptsync` is no longer separable from puddle membership.

The cost of forced enrollment: a wrapped age key on disk (~1 KB), an empty `~/cryptsync/` directory, and peer announces that fan out nothing if no files are written. Trivial.

### Gitmirror lifecycle is NOT absorbed

Gitmirror operates at a different granularity than cryptsync. Cryptsync is "one mesh per puddle"; gitmirror is "many projects per puddle, each with its own membership stack and fanout policy." Project lifecycle (`project init`, `project join`, `project leave`, `project invite`, etc.) lives at the project level — below puddle in the trust hierarchy but with its own scope axis. Folding `gitmirror project *` verbs under `puddle` would either misalign (puddle shouldn't own per-project state) or just rename verbs without simplifying anything.

What gitmirror gets from this work: the puddle session helper (started by `puddle unlock`) becomes the canonical "am I authorized to do puddle-anchored things" gate. `git push vigo://...` rides on `puddle unlock` like cryptsync does. Already true today; this ADR formalizes the coupling.

## Consequences

- **Cryptsync subtree is content-only.** No more "lifecycle vs daily-use" split inside cryptsync. The verbs that survive all operate on files or keys, not on membership.
- **Puddle is the single lifecycle home for everything puddle-anchored.** Operators learning the system see a clear hierarchy: `swarm` → `puddle` → `{cryptsync, gitmirror}`. Lifecycle questions go to puddle; content questions go to the relevant subsystem.
- **Operator UX simplification.** "How do I leave the puddle?" → `puddle leave`. "How do I evict a peer?" → `puddle evict <host>`. Single-step for either operation, with cryptsync state cleanup happening as a side effect.
- **Cryptsync mesh = puddle.** No subset distinction; no "cryptsync member who isn't a puddle member" case to reason about. Test surface narrows accordingly.
- **`puddle leave` and `puddle evict` are new verb implementations**, not renames. They reuse the cryptsync envelope/gossip plumbing for cross-envoy cleanup but carry additional cleanup responsibilities (puddle keypair, gitmirror project state). Estimated implementation effort: ~half-day for `puddle leave` (reuse most of `cryptsync leave`'s code); ~half-day for `puddle evict` (reuse `apply_leave` chain).
- **No `cryptsync: true` opt-in flag.** Usercrate schema simplifies; modlint's `cryptsyncWithoutHomeRule` retires.
- **No partial-leave granularity.** An operator who wants to keep gitmirror but stop cryptsync has no path post-0.40.0. This is a deliberate trade — the per-capability flexibility was theoretical (no operator was using it productively), and removing it earns a clean mental model.

## Alternatives considered

- **Keep `cryptsync leave` for partial-leave (gitmirror without cryptsync).** Preserves the per-capability granularity but leaves the cryptsync subtree with a residue that contradicts ADR-020's "cryptsync is puddle-only" framing. The use case (puddle member who actively wants to drop cryptsync but keep gitmirror) is hypothetical; no operator has asked for it. Rejected.
- **Add scoped variants under puddle (`puddle leave-cryptsync`).** Keeps lifecycle under the puddle subtree but with awkward compound verbs. No simplification — just renames. Rejected.
- **Auto-init puddle on `cryptsync init`.** A previous discussion considered making `cryptsync init` auto-create a puddle when missing. Rejected because it couples two trust-anchor-affecting operations in a way that creates surprising failure modes (operator accidentally minting a new puddle pubkey). The current design is the inverse — puddle owns the trust anchor, cryptsync follows.

## Related

- [ADR-014](014-puddle-primitive.md) — puddle primitive
- [ADR-016](016-cryptsync-membership-on-puddle.md) — cryptsync membership on puddle (recipient-resolution decisions stand)
- [ADR-020](020-cryptsync-puddle-only.md) — sibling decision; standalone retire
