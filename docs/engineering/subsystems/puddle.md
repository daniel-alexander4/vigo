---
title: Puddle
weight: 65
---

# Puddle

Puddle is a per-user puddle identity primitive: a single Ed25519 keypair representing "this user's collection of envoys, treated as one trust unit." Sibling subsystems (gitmirror under [ADR-015](../../adr/015-unified-gitmirror-substrate.md), lockbox membership under [ADR-016](../../adr/016-lockbox-membership-on-puddle.md)) consume it to express membership and drive onboarding without re-doing the per-pair-of-envoys ceremony each subsystem invented in isolation.

[ADR-014](../../adr/014-puddle-primitive.md) is the design of record. This page is a working snapshot of how it's actually built.

## Why a puddle identity exists

Vigo's existing trust primitives are envoy-scoped — each enrolled envoy has its own mTLS cert and per-envoy state for every per-user subsystem. Without a per-user identity above that, "dan-on-laptop" and "dan-on-desktop" are cryptographically distinct, so every personal-multi-envoy workflow has to re-create membership ceremony per envoy. Puddle is the missing primitive: one keypair, multiple envoys, distributable peer-to-peer.

Phase 1 explicitly defers:

- **Multi-puddle per user** — one puddle per user. Multi-puddle ("personal" + "work" identities, puddle selection on every CLI verb) waits for a real demand.
- **Recovery without the passphrase** — no Shamir / escrow / quorum in v1. Passphrase loss = re-init from scratch (new puddle pubkey; existing project tokens / lockbox recipients become orphaned).
- **Cross-envoy gossip of puddle membership** — `members` shows only the local view in v1. ADR-015 / ADR-016 add puddle pubkeys to swarm gossip.

## Identity

The puddle keypair is Ed25519, generated locally on the founder envoy by `vigo swarm puddle init`. The private half is wrapped under the user's passphrase using the same envelope lockbox uses (Argon2id 64 MiB / 3 iter / 1 lane → 32 byte wrap key, then XChaCha20-Poly1305 with a fresh 24-byte nonce). The wrapped blob format is identical bytes to lockbox's signing-key wrap:

```
[1 byte version = 0x01][24 byte nonce][ciphertext + 16 byte tag]
```

The wrapped blob is **portable** across envoys — every envoy in the puddle holds the same bytes for `~/.vigo-puddle/identity.wrapped`. The unwrapping passphrase is held only by the user.

## Membership

An envoy is a puddle member iff:

1. `~/.vigo-puddle/identity.wrapped` exists, and
2. `~/.vigo-puddle/identity.salt` exists, and
3. `~/.vigo-puddle/identity.pub` exists (the bech32-style `ed25519:<hex>` form).

There is no central registry. Sibling subsystems query "is the user puddle-enabled here, and what's the pubkey?" via `puddle::local_puddle_pubkey(home)`.

The session helper additionally drops a `~/.vigo-puddle/session.unlocked` sentinel containing its PID — same shape lockbox uses (ADR-012). Sibling subsystems can use this to detect "is the puddle identity *currently* unlocked?" without speaking the SIGN protocol.

## Distribution: peer-pair (primary)

ADR-014 ratifies peer-pair as the primary onboarding flow; manual scp survives as the air-gapped fallback.

```
                ┌──────────────────┐
                │ founder envoy A  │
                │  puddle member    │
                └────────┬─────────┘
                         │
               1) `pair` issues 8-char code (5-min TTL, single-use)
                         │
                         │ user reads code, dictates to other envoy
                         │
                ┌────────┴─────────┐
                │  joining envoy B │
                │  (no puddle yet)  │
                └────────┬─────────┘
                         │
               2) `join --from A` POSTs code over swarm mTLS (port 1531)
                         │
                         ▼
            POST https://A:1531/swarm/puddle/pair-claim/<user>
            body: {"code": "4827-9163"}
                         │
                         ▼
            handle_pair_claim:
              - reads ~/.vigo-puddle/pair.pending on A (for <user>)
              - constant-time hash compare
              - returns: [salt-len byte][salt][wrapped blob]
              - clears pair.pending (single-use)
                         │
                         ▼
               3) B writes blob+salt+pubkey to ~/.vigo-puddle/,
                  prompts passphrase, verifies unwrap, prints
                  "now run `unlock` to start the session."
```

**Key moves in the wire:** the wrapped blob and the salt. The passphrase **never leaves B**. The pairing code is the human-explicit authorization preventing any other CA-trusted peer from silently requesting the wrapped blob — same model as Signal / Bluetooth pairing.

The pair-claim handler runs in the daemon's swarm-server context (root). The wrapped blob lives in the user's home (root can read it). Authorization is dual: (1) mTLS — the caller is some enrolled envoy in the same Vigo CA; (2) the pairing code — a 5-minute single-use shared secret the user just dictated.

### Pairing code shape

8 ASCII digits in `NNNN-NNNN` form (e.g., `4827-9163`). Hashed with SHA-256 before persistence — `pair.pending` stores `code_hash`, never plaintext. Constant-time hash compare on incoming claims.

10⁸ = 100M code space combined with the 5-min TTL and single-use semantics makes the brute-force-over-the-wire attack infeasible: 10M requests / 5 min would saturate any peer process, and every attempt rides mTLS so each probe is from a fleet-CA-trusted peer (any abuse is forensically attributable).

### Why `join` requires `sudo`

The mTLS key file (`tls.key` under the agent's data dir) is `0600` root-owned. The `join` verb makes an mTLS POST, so it has to read that file — which means it has to run as root. Other puddle verbs (`init`, `pair`, `unlock`, `lock`, `pubkey`, `members`, `rotate-passphrase`, `status`) operate purely on the user's home and don't need root.

After `join` succeeds (running as root via sudo), the user runs `vigo swarm puddle unlock` (no sudo) to start the session helper in their own uid context. Splitting these two phases avoids forking + privilege-dropping in one process.

## Distribution: manual scp (air-gapped fallback)

For envoys with no swarm connectivity to any active puddle member:

```
[A] scp ~/.vigo-puddle/identity.wrapped \
        ~/.vigo-puddle/identity.salt \
        ~/.vigo-puddle/identity.pub \
        usb:/path/   # or USB / sneakernet
[B] cp -r usb:/path/ ~/.vigo-puddle/
[B] vigo swarm puddle unlock
```

The wrapped blob is opaque ciphertext on disk; the security boundary is the passphrase wrapping, not the transport. Manual scp ends up at the same on-disk shape `join` produces.

## Session helper

`vigo swarm puddle unlock` reads the wrapped blob, prompts for the passphrase, unwraps to an `ed25519_dalek::SigningKey`, then spawns a `setsid`-detached per-user daemon that holds the key in process memory and serves SIGN / STATUS / PUBKEY / LOCK over a Unix socket:

- Linux: `$XDG_RUNTIME_DIR/vigo/puddle.sock`, mode 0600, SO_PEERCRED-gated against the calling uid.
- macOS: `$TMPDIR/lockbox-<UID>/puddle.sock` (yes, the parent dir is shared with lockbox — one 0700 dir per uid keeps the layout simple), LOCAL_PEEREID-gated.

Three independent shutdown triggers each scrub the key and exit:

- explicit `LOCK` request,
- the parent shell PID disappearing (10s polling watchdog),
- idle timeout (default 60 minutes; resets on every SIGN / STATUS).

The key never lives on disk in plaintext. The CLI pipes the unwrapped key bytes through the helper's stdin and immediately closes the pipe; both ends go through `Zeroizing` to scrub any in-process leftover.

## State files

User-side, all under `~/.vigo-puddle/` (mode 0700, owned by the user):

| File | Mode | Purpose |
|---|---|---|
| `identity.wrapped` | 0600 | Ed25519 private key wrapped under user passphrase. Portable across envoys. |
| `identity.salt` | 0600 | 16 random bytes, Argon2id input. Portable across envoys. |
| `identity.pub` | 0644 | `ed25519:<hex>` puddle pubkey, one line. Public; sibling subsystems read this. |
| `pair.pending` | 0600 | Active pairing-code session (`code_hash` + `expires_at`). Removed on first claim or TTL expiry. |
| `session.unlocked` | 0600 | Session-helper PID. Written on unlock; removed on lock/idle/parent-gone. Siblings probe + `pid_alive` to detect lock state. |

Daemon-side state: none today. The pair-claim handler is stateless — it reads `pair.pending` directly under the target user's home.

## CLI verbs

```
vigo swarm puddle init                  # founder; once per puddle ever
vigo swarm puddle pair                  # issue 8-char code (5-min TTL)
sudo vigo swarm puddle join --from <h>  # consume code; persist wrapped blob
vigo swarm puddle rekey-pair [--close]  # ADR-018: rotate puddle keypair, issue claim code
                                            #         (24h graceful retirement window)
sudo vigo swarm puddle rekey --from <h> # ADR-018: claim new wrapped blob, migrate this envoy
vigo swarm puddle unlock                # start session
vigo swarm puddle lock                  # scrub key
vigo swarm puddle pubkey                # print puddle pubkey
vigo swarm puddle members               # show this envoy's view
vigo swarm puddle rotate-passphrase     # re-wrap locally (does not change puddle pubkey)
vigo swarm puddle status                # init / unlock / pair-pending / rekey state
```

See [agent-swarm-puddle](../../reference/cli/agent-swarm-puddle.md) for the full reference.

## Failure modes

- **Passphrase forgotten.** No recovery in v1. Re-init creates a new puddle (different pubkey); existing project tokens / lockbox recipients become orphaned. Documented loudly. `status` surfaces "puddle locked, recovery requires passphrase or re-init."
- **All wrapped key copies lost across all envoys.** Same as passphrase forgotten — re-init from scratch. Disaster recovery for the user, not silent data loss.
- **Single envoy compromised.** Wrapped blob + passphrase = attacker has the puddle key. Mitigations: short Argon2id verification time on unlock (cost factor matched to lockbox), session-helper idle lock, system-level disk encryption strongly recommended.
- **Pair-claim code brute force.** 100M code space + 5-min TTL + single-use + mTLS-only callers. Every probe is forensically attributable.

## Code map

- `agent/src/swarm/puddle/mod.rs` — public API (`local_puddle_pubkey`, `is_initialized`).
- `agent/src/swarm/puddle/config.rs` — paths under `~/.vigo-puddle/`.
- `agent/src/swarm/puddle/cli.rs` — verb dispatcher; status surface enrichment for retired-puddles and offline-envoy hint.
- `agent/src/swarm/puddle/codes.rs` — generic 8-digit claim-code primitives (shared by pair + rekey).
- `agent/src/swarm/puddle/pair.rs` — pair-specific path bindings + HTTP claim handler.
- `agent/src/swarm/puddle/rekey.rs` — `RetiredFleetEntry` log + active-session predicate + post-rotation lockbox sync + rekey-claim HTTP handler (ADR-018).
- `agent/src/swarm/puddle/session.rs` — session helper daemon.
- `agent/src/swarm/lockbox/identity_rewrap.rs` — atomic re-wrap of the per-envoy age key from one puddle keypair's wrap-key to another (ADR-018 prereq).
- `agent/src/swarm/server.rs` — wires `POST /swarm/puddle/pair-claim/<user>` and `POST /swarm/puddle/rekey-claim/<user>` to the respective handlers.
- `agent/src/main.rs` — argv dispatch for `swarm puddle *` and the hidden `puddle-session-daemon` verb.

## See also

- [ADR-014](../../adr/014-puddle-primitive.md) — the design.
- [ADR-015](../../adr/015-unified-gitmirror-substrate.md) — unified gitmirror substrate built on puddle (next).
- [ADR-016](../../adr/016-lockbox-membership-on-puddle.md) — lockbox membership on puddle (after 015).
- [ADR-017](../../adr/017-no-puddle-level-revoke.md) — why there is no puddle-level revoke.
- [ADR-018](../../adr/018-puddle-rekey.md) — design of the rekey flow.
- [Lockbox](lockbox.md) — shares the wrap envelope and the session-helper shape.
