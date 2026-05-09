---
title: "ADR-020: Lockbox is puddle-only"
---

# ADR-020: Lockbox is puddle-only

**Status:** Decided 2026-05-07. Implementation in 0.40.0. Supersedes the **dual-mode** branch of [ADR-016](016-lockbox-membership-on-puddle.md) — the recipient-resolution / per-envoy-keys / puddle-cert decisions in ADR-016 stand; what's retired is the parallel `standalone` mode.

**Date:** 2026-05-07

## Context

ADR-016 introduced lockbox-on-puddle as a *parallel* mode to standalone. The split made sense at the time: standalone was the legacy enrollment path, puddle was the new one, and shipping both gave existing users an obvious migration runway. The reserved verbs (`puddle adopt`, `standalone adopt`, `standalone recipients {add,remove,list}`) were placeholders for the migration story.

After six months of dual-mode operation, the standalone path's value-add is thin:

- **Pre-puddle legacy.** The only durable reason standalone existed; per CLAUDE.md "zero users, no migrations, no backward compatibility, pristine codebase is the goal," the migration runway is unnecessary.
- **Independent blast radius.** Theoretical separation between lockbox passphrase and puddle passphrase. In a single-operator threat model, both passphrases live in the same operator's head; if one is leaked, the other probably is too. Thin.
- **No-puddle path.** Operators who didn't want a puddle could still run lockbox. Real, but minor — those operators can use a single-envoy puddle (one extra `puddle init` step at setup) and get functionally identical encryption-at-rest.

What standalone *costs*, on the other hand, is concrete:

- Two enrollment flows in `cli.rs` (`lockbox puddle init` vs `lockbox standalone init`/`enroll`).
- Two unlock paths (passphrase prompt vs puddle session helper challenge).
- Two wrap-key derivation paths (Argon2id over passphrase vs Argon2id over puddle-helper-signed challenge).
- Two recipient-discovery branches (puddle pubkey filter vs pairwise TOFU gossip).
- A `WrapMode` enum threading through `config.rs`, `cli.rs`, `identity_rewrap.rs`, plus ~56 occurrences of `wrap_mode` / `WrapMode::Standalone` / `WrapMode::Puddle` across the agent.
- Doc trees that fork — every CLI ref table, every concept page, every example has to mention both modes.
- A `lockbox: true` per-envoy opt-in flag in usercrates that becomes meaningless once lockbox is mandatory on puddle membership (covered by [ADR-021](021-lockbox-lifecycle-on-puddle.md)).

The maintenance burden compounds with every lockbox feature touched, and the parallel-paths reasoning that justified shipping both modes never converted into a real migration any operator needed.

## Decision

**Lockbox is puddle-only.** Standalone mode is retired in 0.40.0.

### What changes

- **`lockbox standalone *` subtree deleted.** No `lockbox standalone init`, no `lockbox standalone enroll`. Operators running these verbs get an "unknown subcommand" error with a help suggestion pointing at `vigo swarm puddle init`.
- **`WrapMode` enum and `wrap_mode:` config field removed.** The wrapping crypto stays — XChaCha20-Poly1305 over Argon2id-derived key — but the key source is always "puddle-helper-signed challenge over `b\"vigo-lockbox-wrap-v1\"`" (the path slice 4 of ADR-016 introduced for puddle mode).
- **Standalone unlock prompt path removed.** `lockbox unlock` always routes through the puddle session helper. If puddle isn't unlocked, the verb fails with a pointer to `vigo swarm puddle unlock`.
- **Pairwise-gossip recipient resolution removed.** The announce-receive path drops announces with empty `puddle_pubkey` at the front door; they never reach the peer table. The `live_pubkeys_for_puddles` filter no longer needs to handle the "no local puddle" case.
- **`identity_rewrap.rs` simplification.** The `RewrapOutcome::StandaloneSkipped` variant and the `if cfg.wrap_mode != Puddle` guard are removed. ADR-018 puddle-key rotation logic stays intact; the function becomes mode-unaware.
- **No migration verb.** Existing standalone enrollments are not silently migrated. The standalone identity becomes unreachable on the new code; operator action is `lockbox purge` followed by `puddle init` + (for joining an existing puddle) `puddle pair` / `puddle join`. Per CLAUDE.md zero-users, this is the right cost.

### Mode 1 (encryption-at-rest, no peers)

Operators who want lockbox as local encryption-at-rest with no fan-out — the only legitimate use case standalone served outside legacy migration — get there via a **single-envoy puddle**:

```
vigo swarm puddle init      # create puddle keypair (no peers)
vigo swarm puddle unlock    # start session helper; lockbox auto-enrolls
# files dropped in ~/lockbox/ get encrypted, stay local (no peers to fan out to)
```

Functionally identical to standalone-with-no-peers: encryption happens, no fan-out occurs. Operator pays one extra setup step (`puddle init`) and gains a uniform mental model that scales naturally to multi-envoy puddles without a mode switch. Mode 1 → Mode 3 transition (pair an additional envoy in) is friction-free under this model — `vigo swarm puddle pair` from the existing envoy, `vigo swarm puddle join --from <host>` on the new one. Under standalone, the same transition required scrubbing standalone state and re-enrolling under puddle.

### CLI surface (post-0.40.0)

```
vigo swarm lockbox                       (mode-agnostic top-level verbs)
  ├── unlock          daily: unwrap age key, populate session helper
  ├── lock            daily: zero in-memory key
  ├── encrypt <file>  manual encrypt to current recipients
  ├── decrypt <file>  manual decrypt
  ├── reencrypt       re-encrypt corpus to current recipients
  ├── resurrect       override an active tombstone
  ├── purge           local wipe, no gossip (recovery verb)
  └── status          show local state
```

Lifecycle verbs (`init`, `leave`, `forget <host>`) are absorbed into the puddle subtree — see [ADR-021](021-lockbox-lifecycle-on-puddle.md). The puddle and standalone subtree split that ADR-016 documented is collapsed.

## Consequences

- **Single mental model.** "Lockbox is what happens when puddle members participate in encrypted file sync." No mode-determination, no `--help` table comparing modes, no `wrap_mode:` config setting to inspect.
- **Code halved on the wrap-key + recipient-discovery paths.** Concrete reduction: `cli.rs` standalone subtree (~200 lines), `WrapMode` enum + branches (~56 callsites), pairwise-gossip filter logic, identity_rewrap mode guard.
- **Doc trees collapse.** ADR-016's "Mode is determined automatically..." paragraph + the wrap-key comparison table + the standalone verb subtree all retire. CLI ref shrinks materially.
- **Mandatory puddle dependency.** Operators who genuinely don't want a puddle keypair on disk lose that option. The cost is one extra setup step (`puddle init`) and ~1 KB on disk for a wrapped puddle keypair under the user's passphrase. This is the only meaningful loss; the analysis above considers it acceptable in exchange for the simplification.
- **Future lockbox features only handle one mode.** Every feature past 0.40.0 (recipient-resolution improvements, wrap-key rotation work, observability enhancements) writes against a single path.
- **No back-compat shims.** Per CLAUDE.md, the codebase doesn't ship aliases, deprecated branches, or "the old name still works" hacks. Operators who run the retired verbs get a clear error pointing at the new path; nothing more.

## Alternatives considered

- **Keep standalone for the no-puddle use case.** Preserves operator choice but locks in the dual-path maintenance burden indefinitely. Single-envoy puddle covers the same ground with marginally more setup, and the simplification dividend is large. Rejected.
- **Migration verb (`lockbox standalone-to-puddle`).** A one-shot rewrap-and-re-register flow. Adds code surface that exists for a one-time event. Per CLAUDE.md zero-users, rejected.
- **Soft-deprecate standalone (warn but keep working).** Preserves the maintenance burden for an indefinite "planned retirement" window. Rejected.

## Related

- [ADR-014](014-puddle-primitive.md) — the puddle primitive
- [ADR-016](016-lockbox-membership-on-puddle.md) — superseded standalone branch; recipient-resolution and per-envoy-keys decisions stand
- [ADR-021](021-lockbox-lifecycle-on-puddle.md) — lifecycle absorption into puddle (sibling decision in 0.40.0)
