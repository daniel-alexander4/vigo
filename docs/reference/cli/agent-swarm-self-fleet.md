---
title: vigo swarm self-fleet
weight: 26
---

# `vigo swarm self-fleet`

`vigo swarm self-fleet` is the user-facing CLI for the [self-fleet](../../engineering/subsystems/self-fleet.md) primitive — a per-user Ed25519 fleet identity that subsystems like gitmirror (ADR-015) and cryptsync membership (ADR-016) consume as a single trust unit. State lives under `~/.vigo-fleet/`. No Vigo-server contact for any verb on this page.

## Invocation

Most verbs run as the invoking user against `~/.vigo-fleet/`. The one exception is `join`, which has to read the agent's mTLS key (0600 root-owned) to call out to a peer envoy — `join` requires `sudo`. The rest of the verbs do not.

## Permission model

- All state is the user's. Identity material (`identity.wrapped`, `identity.salt`) is `0600`; the public key (`identity.pub`) is `0644`. The dot dir itself is `0700`.
- The session helper enforces `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS) on its socket so only the same uid can issue SIGN / STATUS / LOCK.
- The pair-claim HTTP handler trusts callers reaching the swarm peer port (mTLS — same Vigo CA every agent already trusts) **and** requires a matching pairing code that the user just dictated. Either alone is insufficient.

## Verb reference

| Verb | Behavior |
|---|---|
| `init` | Founder envoy, once per fleet ever. Generates a fresh Ed25519 keypair, prompts (with confirm) for the fleet passphrase, wraps the private half via Argon2id + XChaCha20-Poly1305 with a fresh 16-byte salt, writes `identity.wrapped` / `identity.salt` / `identity.pub`. Refuses to overwrite an existing fleet on this envoy. |
| `pair` | On a fleet member, issue a 5-minute single-use 8-character pairing code (`NNNN-NNNN`). Drops `~/.vigo-fleet/pair.pending` (storing the SHA-256 of the code, never plaintext) so the daemon-side pair-claim handler can validate the joining envoy's request. Re-running overwrites any existing pending session. |
| `join --from <host>` | **Requires sudo.** New envoy, once per envoy ever. Prompts for the pairing code, POSTs it to `https://<host>:1531/swarm/self-fleet/pair-claim/<user>` over mTLS, receives the wrapped blob and salt, prompts for the fleet passphrase, verifies the unwrap, persists `~/.vigo-fleet/` files. Tells the user to run `unlock` next (no helper spawn — runs in user context). |
| `unlock` | Read the wrapped blob, prompt for the passphrase, unwrap, hand the key to the session helper. Helper holds the key in RAM until logout, lock, or idle timeout (60 min default). |
| `lock` | Scrub the held key and stop the session helper. Idempotent (no-op if no helper is running). Auto-fires on parent-shell exit and idle timeout. |
| `pubkey` | Print the local fleet pubkey in `ed25519:<hex>` form. Used for invites, capability lists, dictation between envoys. |
| `members` | Show this envoy's view of fleet membership. Phase 1 surfaces only the local envoy's pubkey; cross-envoy gossip lands with ADR-015 / ADR-016. |
| `rotate-passphrase` | Prompt the current passphrase, then a new one (with confirm), re-wrap the local copy of the key under the new passphrase. **This envoy only** — every other fleet envoy retains the old passphrase until rotated separately. The fleet pubkey is unchanged; downstream subsystem trust anchors are unaffected. |
| `status` | Show local self-fleet state: initialized? fleet pubkey? pair pending? session helper running? |

## Bootstrap walkthrough

**Founder envoy (first one):**

```
$ vigo swarm self-fleet init
self-fleet passphrase: ****
confirm passphrase: ****
wrote /home/dan/.vigo-fleet/identity.wrapped (0600, wrapped)
wrote /home/dan/.vigo-fleet/identity.salt (0600)
wrote /home/dan/.vigo-fleet/identity.pub (0644)

fleet pubkey: ed25519:9f3a...c1d8

This envoy is now the founder of your self-fleet. To add more envoys:
  1) on this envoy:        `vigo swarm self-fleet pair`
  2) on the joining envoy: `vigo swarm self-fleet join --from laptop`
```

**Adding a second envoy via peer-pair:**

```
[laptop] $ vigo swarm self-fleet pair

Pairing code:  4827-9163
Valid for:     5 minutes (300 seconds)

On the joining envoy, run:
    vigo swarm self-fleet join --from laptop
then enter this code when prompted, then enter your fleet passphrase.

The code is single-use; re-run `pair` to issue another.
```

```
[desktop] $ sudo vigo swarm self-fleet join --from laptop
Pairing with laptop for user dan.
pairing code (NNNN-NNNN): 4827-9163
self-fleet passphrase: ****

✓ Joined fleet ed25519:9f3a...c1d8.
  wrote /home/dan/.vigo-fleet/identity.wrapped (0600, wrapped)
  wrote /home/dan/.vigo-fleet/identity.salt (0600)
  wrote /home/dan/.vigo-fleet/identity.pub (0644)

Run `vigo swarm self-fleet unlock` (no sudo) to activate the session on this envoy.
```

```
[desktop] $ vigo swarm self-fleet unlock
self-fleet passphrase: ****
✓ Unlocked. Self-fleet session helper running until logout, lock, or 60 min idle.

Bootstrapping cryptsync (fleet-mode wrap, no extra passphrase)...
wrote /home/dan/.cryptsync/identity.age (0600, wrapped)
wrote /home/dan/.cryptsync/identity.salt (0600)
…
✓ Issued fleet-membership cert (expires in 30 days)
```

**Manual scp fallback (air-gapped envoy):**

```
[laptop] $ scp ~/.vigo-fleet/identity.{wrapped,salt,pub} \
              dan@air-gapped:/home/dan/.vigo-fleet/
[air-gapped] $ vigo swarm self-fleet unlock
self-fleet passphrase: ****
✓ Unlocked. Self-fleet session helper running until logout, lock, or 60 min idle.
```

**Cryptsync auto-bootstrap (ADR-016 slice 4).** When `unlock` runs and `~/.cryptsync/identity.age` is absent, the verb auto-runs `cryptsync fleet init` inline using the just-spawned session helper to derive the wrap key (no second passphrase prompt — the same fleet identity that protects self-fleet protects cryptsync). Suppressed once cryptsync is already enrolled. Failure to bootstrap is non-fatal: a warning surfaces and the operator can re-run `vigo swarm cryptsync fleet init` manually. `join` does not auto-run cryptsync (the session helper isn't running yet at that point); the next `unlock` after `join` handles it instead.

## File layout

```
~/.vigo-fleet/
  identity.wrapped     # Ed25519 private key wrapped (binary). 0600.
  identity.salt        # 16-byte Argon2id salt. 0600.
  identity.pub         # ed25519:<hex> pubkey, one line + newline. 0644.
  pair.pending         # active pairing-code session (JSON: code_hash + expires_at).
                       # written by `pair`, read+cleared by the daemon's pair-claim handler.
  session.unlocked     # session helper PID, present only while unlocked. Siblings probe this.
```

Mode 0700 on `~/.vigo-fleet/` itself, owned by the invoking user.

## Wire interaction

The only over-the-wire surface is the `join` verb, which talks to one route on the swarm peer port:

```
POST https://<host>:1531/swarm/self-fleet/pair-claim/<user>
Content-Type: application/json
{ "code": "4827-9163" }
```

mTLS-required (Vigo CA). Server-side validation:

1. The user `<user>` exists, has a `~/.vigo-fleet/pair.pending` sentinel, and the sentinel hasn't expired.
2. SHA-256 of the canonicalized incoming code (whitespace + dashes stripped, uppercased) matches the stored hash, constant-time compare.

On success: 200 with body `[1 byte salt-len][salt bytes][wrapped blob]`. The sentinel is cleared **before** the response is returned, so the code is single-use even if the response is interrupted and the peer retries.

Failure status codes:

| Status | Meaning |
|---|---|
| 400 | Malformed path or claim body |
| 401 | Code mismatch (don't clear — let the user retry within the TTL) |
| 404 | No active pair session for that user |
| 410 | Pair session expired (sentinel cleared) |
| 500 | Local read or filesystem issue |

## Configuration

No `~/.vigo-fleet/config.yaml` today. The session helper's idle timeout is hard-coded at 60 minutes. If a per-user override surfaces real demand, add a config the same shape cryptsync uses; until then defaults are the contract.

## Limits

- One fleet per user. Multi-fleet (separate "personal" / "work" identities) is deferred to v2.
- Re-init from scratch on passphrase loss. No Shamir / escrow / quorum recovery.
- Cross-envoy gossip of fleet membership is not in v1; `members` shows only the local view.
- Linux + macOS only. Windows port follows the platform-cadence rule (demand-driven; see CLAUDE.md).

## See also

- [Self-fleet subsystem](../../engineering/subsystems/self-fleet.md) — design + code map.
- [ADR-014](../../adr/014-self-fleet-primitive.md) — design of record.
- [ADR-015](../../adr/015-unified-gitmirror-substrate.md) — first consumer of self-fleet.
- [ADR-016](../../adr/016-cryptsync-membership-on-self-fleet.md) — second consumer.
- [`vigo swarm cryptsync`](agent-swarm-cryptsync.md) — shares the wrap envelope + session-helper pattern.
