---
title: "ADR-014: Puddle primitive"
---

# ADR-014: Puddle primitive

**Status:** Complete (shipped 0.34.8). Multi-puddle per user, Shamir/escrow recovery, and cross-envoy gossip of puddle membership remain explicitly deferred per the Decision section's scope gates. Puddle keypair rotation, originally implied as "re-init from scratch," is now addressed by [ADR-018](018-puddle-rekey.md) (rekey verbs preserve lockbox state across rotation; gitmirror founder-rekey still pending its own ADR).
**Date:** 2026-04-29
**Updated 2026-04-29:** Distribution section restructured to put peer-pair as the primary onboarding path (manual scp survives as air-gapped fallback). CLI verbs renamed for clarity (`activate` split into `unlock` + `join`; `deactivate` → `lock`; `show` → `pubkey`; new `pair` verb). Resolved scope gates recorded in the Decision section: single puddle per user (multi-puddle deferred to v2), three sequential releases (014 → 015 → 016), re-init from scratch on passphrase loss (no Shamir/escrow in v1).

## Context

Vigo's existing trust primitives are envoy-scoped: each enrolled envoy has its own mTLS cert, and per-user state on each envoy (lockbox `identity.age`, gitmirror sidecars, etc.) is independent. There is no cross-envoy user-identity primitive. The consequence is that "dan-on-laptop" and "dan-on-desktop" are cryptographically distinct identities even though they belong to the same human, which forces every personal-multi-envoy workflow to re-create membership ceremony per-envoy: lockbox requires `init` + `enroll` + `unlock` on every envoy independently; multi-member gitmirror would require explicit per-pair invites for each personal envoy.

The right primitive is a **per-user puddle identity** — a single keypair representing "dan's puddle of envoys," distributable across the user's own envoys, that subsystems can query as a single trust unit. This ADR defines that primitive. ADR-015 (unified gitmirror substrate) and ADR-016 (lockbox membership on puddle) build on it.

## Decision

### Identity

A puddle is owned by one human user across one or more enrolled envoys. The puddle's cryptographic identity is a single ed25519 keypair, generated locally on the first envoy where the user runs `puddle init`. The private half is wrapped using the same primitive lockbox already uses (`Argon2id` parameter set + `XChaCha20-Poly1305`, see `agent/src/swarm/lockbox/crypt.rs`) under a user-supplied passphrase. The wrapped blob is portable across envoys; the unwrapping passphrase is held only by the user.

The puddle's public half is the **puddle pubkey** — the unit other subsystems and other users see. Subsystems treat "puddle pubkey X" as a trust anchor; what envoys are inside puddle X is invisible at the trust boundary.

### Membership

Membership of an envoy in a puddle is decided locally on that envoy: if `~/.vigo-puddle/identity.wrapped` is present and has been unlocked at least once during the current agent runtime, the envoy is a member. The agent advertises puddle pubkey X to peers as part of the existing swarm gossip. There is no central puddle-membership registry; an envoy joins the puddle by holding the wrapped key and knowing the passphrase.

### Distribution

Two onboarding paths. **Peer-pair is the primary path; manual scp is the air-gapped fallback.**

**Peer-pair (primary).** A new envoy joins by pairing with an already-active puddle envoy over the existing swarm mTLS substrate (port 1531):

1. On the active envoy: `vigo swarm puddle pair` — prints a 5-minute single-use pairing code, listens for one incoming pair request from a peer.
2. On the new envoy: `vigo swarm puddle join --from <active-host>` — opens an mTLS connection to the active envoy (both endpoints already mutually authenticated by the Vigo CA), presents the pairing code, receives the **wrapped** `identity.wrapped` blob, prompts for the passphrase locally, unwraps, hands the key to the session helper.

The wrapped blob on the wire is the same opaque ciphertext that sits on disk; the security boundary is the Argon2id+XChaCha20 wrapping with the user's passphrase, not the transport. The passphrase **never leaves the destination envoy**. The pairing code is the human-explicit authorization preventing any other envoy in the swarm from silently requesting the wrapped blob (same model as Signal/Bluetooth pairing).

Optional audit-log entries flow through the existing audit hash chain on next check-in: `puddle.pair-issued` (with destination envoy fingerprint) on the source side, `puddle.pair-completed` on the destination side. The server never sees the blob — it learns the pair *happened*, consistent with Vigo's audit posture for envoy-side operations.

**Manual scp (air-gapped fallback).** For envoys with no swarm connectivity to any existing puddle member, the user copies `~/.vigo-puddle/identity.wrapped` out of band (`scp`, USB, etc.) and runs `vigo swarm puddle unlock` on the destination, which prompts for the passphrase and unwraps locally. No `--from` flag in this path.

### Session helper

Like lockbox's session helper, an unlocked puddle identity lives in process memory in a `setsid`-detached per-user daemon, gated by `SO_PEERCRED` on a Unix socket at `$XDG_RUNTIME_DIR/vigo/puddle.sock`. The daemon answers signing requests for the calling user. Idle timeout (default 60 minutes) and explicit `lock` zero the in-memory key. Parent-PID watchdog terminates the helper on agent restart.

### CLI verbs

Lifecycle:

- `vigo swarm puddle init` — generate keypair, prompt for passphrase, wrap, store at `~/.vigo-puddle/identity.wrapped`. Fails if file already exists. **Once per puddle, ever.** (Founder envoy only.)
- `vigo swarm puddle pair` — on an already-unlocked envoy: print a 5-minute single-use pairing code, listen for one incoming pair request. Used to onboard a new envoy into an existing puddle.
- `vigo swarm puddle join --from <host>` — on a new envoy: connect to `<host>` over swarm mTLS, present the pairing code, receive the wrapped blob, prompt for passphrase, unwrap, populate session helper. **Once per envoy, ever.** Auto-runs `lockbox puddle init` on success (per ADR-016).

Daily:

- `vigo swarm puddle unlock` — unwrap local `identity.wrapped`, hand key to session helper. Prompts for passphrase. Idempotent if already unlocked. Auto-runs `lockbox puddle init` if lockbox state is absent (per ADR-016).
- `vigo swarm puddle lock` — zero in-memory key, kill session helper. Wrapped blob on disk untouched.

Informational / maintenance:

- `vigo swarm puddle pubkey` — print this envoy's active puddle pubkey fingerprint for use in invites / membership lists.
- `vigo swarm puddle members` — list puddle pubkeys advertised by peers in the swarm gossip (informational; not authoritative).
- `vigo swarm puddle rotate-passphrase` — re-wrap the key under a new passphrase. Same key material; subsystem trust anchors unchanged.

### Failure modes

- **Passphrase forgotten.** No recovery in v1. Re-init creates a new puddle (different pubkey); existing project tokens / lockbox recipients become orphaned. Documented loudly. Status output surfaces "puddle locked, recovery requires passphrase or re-init."
- **All wrapped key copies lost across all envoys.** Same as passphrase forgotten — re-init from scratch. Disaster recovery for the user, not silent data loss.
- **Single envoy compromised.** Wrapped blob + passphrase = attacker has the puddle key. Mitigation: short Argon2id verification time on unlock (cost factor matched to lockbox), session-helper idle lock, system-level disk encryption recommended.

### Resolved scope gates

The following defaults from the original draft were ratified before implementation:

- **Single puddle per user in v1.** Multi-puddle (separate "personal" and "work" identities, puddle selection at every CLI verb) is deferred to v2 unless a real user surfaces the need.
- **Three sequential releases.** ADR-014, then ADR-015, then ADR-016. Each shippable independently; each later one depends on the previous. Bounded blast radius per release.
- **Re-init from scratch on passphrase loss.** No Shamir secret sharing, no escrow, no quorum recovery in v1. Documented loudly; deferred unless real demand surfaces.

## Alternatives Considered

**Per-envoy keypairs with cross-envoy trust attestations.** Each envoy generates its own keypair; envoys mutually sign "this other envoy is also me" attestations. Rejected because it creates O(N²) declarations for an N-envoy personal puddle and provides no UX win over the shared-key model — the security delta is nil since any envoy in the puddle can decrypt content sent to the puddle either way.

**Reuse lockbox's age identity as the puddle identity.** Tempting because lockbox already has a per-user wrapped identity. Rejected because age is encryption-only (no signing primitive), and puddle's primary use case (signing capability tokens for gitmirror) requires Ed25519. Coupling the two would also mean lockbox enrollment becomes mandatory for any subsystem using puddle, which is undesirable.

**Reuse the agent's mTLS keypair.** Rejected — the mTLS key is per-envoy, not per-user, and is already bound to TLS handshake usage. Conflating it with user-puddle identity creates confusing trust semantics.

**Server-distributed puddle keys.** Server holds wrapped keys, agents fetch on enrollment. Rejected because it puts the wrapped private key briefly in vigosrv memory and on the wire; even encrypted at rest, this weakens "purely user-held" guarantee. The peer-pair flow (added in the 2026-04-29 amendment) keeps the blob entirely peer-to-peer over agent-to-agent mTLS — server is never in the path — and preserves the property without the manual-scp friction.

**Shamir secret sharing of the wrapped key for passphrase-loss recovery.** Considered. Worth doing eventually but adds substantial UX surface (split into K-of-N, distribute shares, recombine ceremony). Defer to v2 unless real users surface the need.

## Consequences

**Files to add (agent — Rust):**
- `agent/src/swarm/puddle/` — new subsystem directory, mirroring `agent/src/swarm/lockbox/` shape.
  - `crypt.rs` — wrap / unwrap (reuse lockbox's Argon2id + XChaCha20 primitive verbatim).
  - `session.rs` — session helper daemon + Unix socket + SO_PEERCRED + idle timeout (mirror lockbox's `session.rs`).
  - `pair.rs` — pairing-code issuance + peer-pair handshake over swarm mTLS (port 1531). Pairing code: 8-character format (e.g., `4827-9163`), constant-time compare, single-use, 5-minute TTL.
  - `cli.rs` — verbs `init|pair|join|unlock|lock|pubkey|members|rotate-passphrase`.
  - `mod.rs` — public API surface that other subsystems (gitmirror, lockbox) call to: get puddle pubkey, sign blob, query "is this puddle pubkey known to local agent."
- `agent/src/main.rs` — wire `swarm puddle *` verbs into argv dispatch.

**Server-side changes:** none in the data path. Optional audit-log entry types `puddle.pair-issued` and `puddle.pair-completed` flow through the existing audit hash chain on next check-in; server learns the pair *happened* but never sees the wrapped blob.

**No protobuf changes.** Puddle doesn't ride gRPC; the pair handshake rides the existing swarm peer-port HTTPS surface.

**Documentation:**
- `docs/concepts/puddle.md` — the model, the bootstrap flow, the recovery posture.
- `docs/reference/cli/agent-swarm-puddle.md` — verbs.

**Operational:**
- Agent rebuild + cross-compile + redeploy required.
- Z-level version bump.
- No new dependencies — `ed25519-dalek` and the Argon2id/XChaCha20 stack are already linked for lockbox.

**Estimated weight:** ~50–100 KiB on the agent binary (peer-pair adds a small handshake module on top of the original ~40–80 KiB estimate). Heavy reuse of lockbox's wrapping code.

**Behavioral consequences:**
- Puddle is opt-in. An envoy that never runs `puddle init|join` works exactly as today; only subsystems that consume puddle (gitmirror per ADR-015, lockbox membership per ADR-016) gain new behavior.
- Passphrase forgetting is real disaster recovery for the user. v1 is honest about that — no silent data loss, but no quorum / escrow either.
- Single-puddle-per-user in v1. Multi-puddle (e.g., separate "personal" and "work" identities) is deferred until a real user asks for it.
- Peer-pair is the primary onboarding path. Air-gapped envoys still have `unlock` after manual scp as the fallback, but the typical flow is two commands and a passphrase, no scp.
