---
title: "ADR-014: Self-fleet primitive"
---

# ADR-014: Self-fleet primitive

**Status:** Accepted (pending implementation)
**Date:** 2026-04-29

## Context

Vigo's existing trust primitives are envoy-scoped: each enrolled envoy has its own mTLS cert, and per-user state on each envoy (cryptsync `identity.age`, gitmirror sidecars, etc.) is independent. There is no cross-envoy user-identity primitive. The consequence is that "dan-on-laptop" and "dan-on-desktop" are cryptographically distinct identities even though they belong to the same human, which forces every personal-multi-envoy workflow to re-create membership ceremony per-envoy: cryptsync requires `init` + `enroll` + `unlock` on every envoy independently; multi-member gitmirror would require explicit per-pair invites for each personal envoy.

The right primitive is a **per-user fleet identity** — a single keypair representing "dan's fleet of envoys," distributable across the user's own envoys, that subsystems can query as a single trust unit. This ADR defines that primitive. ADR-015 (unified gitmirror substrate) and ADR-016 (cryptsync membership on self-fleet) build on it.

## Decision

### Identity

A self-fleet is owned by one human user across one or more enrolled envoys. The fleet's cryptographic identity is a single ed25519 keypair, generated locally on the first envoy where the user runs `self-fleet init`. The private half is wrapped using the same primitive cryptsync already uses (`Argon2id` parameter set + `XChaCha20-Poly1305`, see `agent/src/swarm/cryptsync/crypt.rs`) under a user-supplied passphrase. The wrapped blob is portable across envoys; the unwrapping passphrase is held only by the user.

The fleet's public half is the **fleet pubkey** — the unit other subsystems and other users see. Subsystems treat "fleet pubkey X" as a trust anchor; what envoys are inside fleet X is invisible at the trust boundary.

### Membership

Membership of an envoy in a self-fleet is decided locally on that envoy: if `~/.vigo-fleet/identity.wrapped` is present and has been unlocked at least once during the current agent runtime, the envoy is a member. The agent advertises fleet pubkey X to peers as part of the existing swarm gossip. There is no central fleet-membership registry; an envoy joins the fleet by holding the wrapped key and knowing the passphrase.

### Distribution

The wrapped key is **never transmitted by Vigo on first bootstrap**. The user manually copies `identity.wrapped` to each envoy they want in their fleet (`scp`, USB, etc.) and runs `self-fleet activate` on the destination, which prompts for the passphrase and unwraps into the agent's session-helper memory.

After cryptsync is enrolled (which itself uses self-fleet for membership per ADR-016), subsequent fleet-key updates can ride cryptsync. First-time bootstrap is unavoidably manual — it is the chicken-and-egg break.

### Session helper

Like cryptsync's session helper, an unlocked self-fleet identity lives in process memory in a `setsid`-detached per-user daemon, gated by `SO_PEERCRED` on a Unix socket at `$XDG_RUNTIME_DIR/vigo/self-fleet.sock`. The daemon answers signing requests for the calling user. Idle timeout (default 60 minutes) and explicit `lock` zero the in-memory key. Parent-PID watchdog terminates the helper on agent restart.

### CLI verbs

- `vigo swarm self-fleet init` — generate keypair, prompt for passphrase, wrap, store at `~/.vigo-fleet/identity.wrapped`. Fails if file already exists.
- `vigo swarm self-fleet activate` — prompt for passphrase, unwrap, hand to session helper. Idempotent if already unlocked.
- `vigo swarm self-fleet deactivate` — zero in-memory key, kill session helper.
- `vigo swarm self-fleet members` — list fleet pubkeys advertised by peers in the swarm gossip (informational; not authoritative).
- `vigo swarm self-fleet rotate-passphrase` — re-wrap the key under a new passphrase. Same key material; subsystem trust anchors unchanged.
- `vigo swarm self-fleet show` — print fleet pubkey fingerprint for use in invites / membership lists.

### Failure modes

- **Passphrase forgotten.** No recovery in v1. Re-init creates a new fleet (different pubkey); existing project tokens / cryptsync recipients become orphaned. Documented loudly. Status output surfaces "fleet locked, recovery requires passphrase or re-init."
- **All wrapped key copies lost across all envoys.** Same as passphrase forgotten — re-init from scratch. Disaster recovery for the user, not silent data loss.
- **Single envoy compromised.** Wrapped blob + passphrase = attacker has the fleet key. Mitigation: short Argon2id verification time on activate (cost factor matched to cryptsync), session-helper idle lock, system-level disk encryption recommended.

## Alternatives Considered

**Per-envoy keypairs with cross-envoy trust attestations.** Each envoy generates its own keypair; envoys mutually sign "this other envoy is also me" attestations. Rejected because it creates O(N²) declarations for an N-envoy personal fleet and provides no UX win over the shared-key model — the security delta is nil since any envoy in the fleet can decrypt content sent to the fleet either way.

**Reuse cryptsync's age identity as the fleet identity.** Tempting because cryptsync already has a per-user wrapped identity. Rejected because age is encryption-only (no signing primitive), and self-fleet's primary use case (signing capability tokens for gitmirror) requires Ed25519. Coupling the two would also mean cryptsync enrollment becomes mandatory for any subsystem using self-fleet, which is undesirable.

**Reuse the agent's mTLS keypair.** Rejected — the mTLS key is per-envoy, not per-user, and is already bound to TLS handshake usage. Conflating it with user-fleet identity creates confusing trust semantics.

**Server-distributed fleet keys.** Server holds wrapped keys, agents fetch on enrollment. Rejected because it puts the wrapped private key briefly in vigosrv memory and on the wire; even encrypted at rest, this weakens "purely user-held" guarantee. Manual scp on first bootstrap is friction but preserves the property.

**Shamir secret sharing of the wrapped key for passphrase-loss recovery.** Considered. Worth doing eventually but adds substantial UX surface (split into K-of-N, distribute shares, recombine ceremony). Defer to v2 unless real users surface the need.

## Consequences

**Files to add (agent — Rust):**
- `agent/src/swarm/self_fleet/` — new subsystem directory, mirroring `agent/src/swarm/cryptsync/` shape.
  - `crypt.rs` — wrap / unwrap (reuse cryptsync's Argon2id + XChaCha20 primitive verbatim).
  - `session.rs` — session helper daemon + Unix socket + SO_PEERCRED + idle timeout (mirror cryptsync's `session.rs`).
  - `cli.rs` — verbs `init|activate|deactivate|members|rotate-passphrase|show`.
  - `mod.rs` — public API surface that other subsystems (gitmirror, cryptsync) call to: get fleet pubkey, sign blob, query "is this fleet pubkey known to local agent."
- `agent/src/main.rs` — wire `swarm self-fleet *` verbs into argv dispatch.

**No server-side changes.** Self-fleet is purely an agent-side primitive in v1.

**No protobuf changes.** Self-fleet doesn't ride gRPC.

**Documentation:**
- `docs/concepts/self-fleet.md` — the model, the bootstrap flow, the recovery posture.
- `docs/reference/cli/agent-swarm-self-fleet.md` — verbs.

**Operational:**
- Agent rebuild + cross-compile + redeploy required.
- Z-level version bump.
- No new dependencies — `ed25519-dalek` and the Argon2id/XChaCha20 stack are already linked for cryptsync.

**Estimated weight:** ~40–80 KiB on the agent binary. Heavy reuse of cryptsync's wrapping code.

**Behavioral consequences:**
- Self-fleet is opt-in. An envoy that never runs `self-fleet init|activate` works exactly as today; only subsystems that consume self-fleet (gitmirror per ADR-015, cryptsync membership per ADR-016) gain new behavior.
- Passphrase forgetting is real disaster recovery for the user. v1 is honest about that — no silent data loss, but no quorum / escrow either.
- Single-fleet-per-user in v1. Multi-fleet (e.g., separate "personal" and "work" identities) is deferred until a real user asks for it.
