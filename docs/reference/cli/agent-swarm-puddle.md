---
title: vigo swarm puddle
weight: 26
---

# `vigo swarm puddle`

`vigo swarm puddle` is the user-facing CLI for the [puddle](../../engineering/subsystems/puddle.md) primitive — a per-user Ed25519 puddle identity that subsystems like gitmirror (ADR-015) and cryptsync membership (ADR-016) consume as a single trust unit. State lives under `~/.vigo-puddle/`. No Vigo-server contact for any verb on this page.

## Invocation

Most verbs run as the invoking user against `~/.vigo-puddle/`. The one exception is `join`, which has to read the agent's mTLS key (0600 root-owned) to call out to a peer envoy — `join` requires `sudo`. The rest of the verbs do not.

## Permission model

- All state is the user's. Identity material (`identity.wrapped`, `identity.salt`) is `0600`; the public key (`identity.pub`) is `0644`. The dot dir itself is `0700`.
- The session helper enforces `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS) on its socket so only the same uid can issue SIGN / STATUS / LOCK.
- The pair-claim HTTP handler trusts callers reaching the swarm peer port (mTLS — same Vigo CA every agent already trusts) **and** requires a matching pairing code that the user just dictated. Either alone is insufficient.

## Verb reference

| Verb | Behavior |
|---|---|
| `init` | Founder envoy, once per puddle ever. Generates a fresh Ed25519 keypair, prompts (with confirm) for the puddle passphrase, wraps the private half via Argon2id + XChaCha20-Poly1305 with a fresh 16-byte salt, writes `identity.wrapped` / `identity.salt` / `identity.pub`. Refuses to overwrite an existing puddle on this envoy. |
| `pair` | On a puddle member, issue a 5-minute single-use 8-character pairing code (`NNNN-NNNN`). Drops `~/.vigo-puddle/pair.pending` (storing the SHA-256 of the code, never plaintext) so the daemon-side pair-claim handler can validate the joining envoy's request. Re-running overwrites any existing pending session. |
| `join --from <host>` | **Requires sudo.** New envoy, once per envoy ever. Prompts for the pairing code, POSTs it to `https://<host>:1531/swarm/puddle/pair-claim/<user>` over mTLS, receives the wrapped blob and salt, prompts for the puddle passphrase, verifies the unwrap, persists `~/.vigo-puddle/` files. Tells the user to run `unlock` next (no helper spawn — runs in user context). |
| `leave [--yes]` | (0.39.15) Leave the puddle on this envoy. Signs a cryptsync LEAVE envelope (queued via `~/.cryptsync/leave.pending` for the daemon to fan out to live peers within ~30s), locks the puddle session helper, then scrubs `~/.vigo-puddle/`, `~/.cryptsync/`, `~/cryptsync/`, and `~/.vigo-gitmirror/`. The local scrub is synchronous; peer notification is asynchronous. Refuses without `--yes`. To re-join later, pair from a current puddle member and `puddle join` on this envoy. Gitmirror bare repos under `/var/lib/vigo/swarm/gitmirror/` are root-owned and survive a user-context leave; admin-side cleanup is a separate path. |
| `rekey-pair [--close] [--window-secs <N>] [--force-orphan-projects]` | (ADR-018 / ADR-019) On a current puddle member, rotate the puddle keypair and issue a 5-minute claim code so surviving envoys can migrate. Re-prompts for the puddle passphrase. Runs an ADR-019 pre-flight check that aborts the rotation if any founded gitmirror project's delegation chain has hit `DELEGATION_MAX_LEN` (5). Atomic-renames `identity.{wrapped,salt,pub}` to `.pre-rekey-<ts>` backups, runs cryptsync identity-rewrap, clears stale TOFU pins on local cryptsync state, runs the gitmirror sync (signs + appends a delegation envelope under OLD for every founded project, rewrites `project.json::members[]` OLD → NEW), restarts the session helper, records a 24h graceful retirement entry in `retired_puddles.json`. Re-running inside the active window only reissues a fresh claim code — keypair and on-disk state untouched. `--close` ends the active window; `--window-secs <N>` overrides the 24h default. `--force-orphan-projects` proceeds even if a founded project is at the delegation cap; affected projects get `project.json::tombstoned_at` set on disk (helper daemon refuses git operations on them, CLI verbs skip them) — operator clears via `gitmirror project re-found <handle>` or drops via `project leave`. |
| `rekey --from <host> [--force-orphan-projects]` | **Requires sudo.** (ADR-018 / ADR-019) On a surviving envoy, claim the new wrapped puddle blob from `<host>`, migrate this envoy onto it. Asks for the puddle passphrase (used both to unwrap the OLD local key for cryptsync rewrap and to unwrap the NEW wrapped blob the founder issued under the same passphrase). Same ADR-019 pre-flight check as `rekey-pair`. Atomic-renames identity files, calls cryptsync identity-rewrap, clears local TOFU pins, signs + appends its own delegation envelope under OLD for every founded project (each envoy maintains its own self-consistent delegation chain), rewrites local member entries OLD → NEW, locks the session helper. Refuses if the unwrapped blob carries the same puddle pubkey already held locally. `--force-orphan-projects` matches the founder-side flag for symmetry. |
| `unlock` | Read the wrapped blob, prompt for the passphrase, unwrap, hand the key to the session helper. Helper holds the key in RAM until logout, lock, or idle timeout (60 min default). |
| `lock` | Scrub the held key and stop the session helper. Idempotent (no-op if no helper is running). Auto-fires on parent-shell exit and idle timeout. |
| `pubkey` | Print the local puddle pubkey in `ed25519:<hex>` form. Used for invites, capability lists, dictation between envoys. |
| `members` | Show this envoy's view of puddle membership. Phase 1 surfaces only the local envoy's pubkey; cross-envoy gossip lands with ADR-015 / ADR-016. |
| `rotate-passphrase` | Prompt the current passphrase, then a new one (with confirm), re-wrap the local copy of the key under the new passphrase. **This envoy only** — every other puddle envoy retains the old passphrase until rotated separately. The puddle pubkey is unchanged; downstream subsystem trust anchors are unaffected. |
| `status` | Show local puddle state: initialized, puddle pubkey, pair pending, session helper, active rekey window (if any), retired-puddles history, hint when peers are announcing under a different puddle pubkey. |

## Bootstrap walkthrough

**Founder envoy (first one):**

```
$ vigo swarm puddle init
puddle passphrase: ****
confirm passphrase: ****
wrote /home/dan/.vigo-puddle/identity.wrapped (0600, wrapped)
wrote /home/dan/.vigo-puddle/identity.salt (0600)
wrote /home/dan/.vigo-puddle/identity.pub (0644)

puddle pubkey: ed25519:9f3a...c1d8

This envoy is now the founder of your puddle. To add more envoys:
  1) on this envoy:        `vigo swarm puddle pair`
  2) on the joining envoy: `vigo swarm puddle join --from laptop`
```

**Adding a second envoy via peer-pair:**

```
[laptop] $ vigo swarm puddle pair

Pairing code:  4827-9163
Valid for:     5 minutes (300 seconds)

On the joining envoy, run:
    vigo swarm puddle join --from laptop
then enter this code when prompted, then enter your puddle passphrase.

The code is single-use; re-run `pair` to issue another.
```

```
[desktop] $ sudo vigo swarm puddle join --from laptop
Pairing with laptop for user dan.
pairing code (NNNN-NNNN): 4827-9163
puddle passphrase: ****

✓ Joined puddle ed25519:9f3a...c1d8.
  wrote /home/dan/.vigo-puddle/identity.wrapped (0600, wrapped)
  wrote /home/dan/.vigo-puddle/identity.salt (0600)
  wrote /home/dan/.vigo-puddle/identity.pub (0644)

Run `vigo swarm puddle unlock` (no sudo) to activate the session on this envoy.
```

```
[desktop] $ vigo swarm puddle unlock
puddle passphrase: ****
✓ Unlocked. Puddle session helper running until logout, lock, or 60 min idle.

Bootstrapping cryptsync (puddle-mode wrap, no extra passphrase)...
wrote /home/dan/.cryptsync/identity.age (0600, wrapped)
wrote /home/dan/.cryptsync/identity.salt (0600)
…
✓ Issued puddle-membership cert (expires in 30 days)
```

## Rekey walkthrough (ADR-018)

When the operator needs to rotate the puddle keypair — stolen-laptop response, hygiene rotation, founder envoy decommissioning — `rekey-pair` rotates the keypair on a trusted founder envoy and `rekey` migrates each surviving envoy. The 5-min claim code per envoy is the trust anchor; the 24h graceful retirement window only governs how long the founder will keep issuing fresh claim codes without re-rotating the keypair.

**Founder envoy:**

```
[laptop] $ vigo swarm puddle rekey-pair
Rotating puddle keypair. The puddle passphrase is required to authorize this.
puddle passphrase: ****

✓ Puddle keypair rotated.
  Old puddle pubkey: ed25519:9f3a…c1d8
  New puddle pubkey: ed25519:7a2b…f04e
  Backups (30d):    /home/dan/.vigo-puddle/identity.wrapped.pre-rekey-1730000000, …
  Cryptsync rewrap: ok (backup at /home/dan/.cryptsync/identity.age.pre-rekey-1730000000)

  Rekey window:     24h (86400s) before founder closes the session
  Claim code:       4827-9163
  Code valid for:   5 minutes (300 seconds)

On each surviving envoy, run:
    sudo vigo swarm puddle rekey --from laptop
then enter the claim code when prompted, then enter your puddle passphrase.
```

**Surviving envoy:**

```
[desktop] $ sudo vigo swarm puddle rekey --from laptop
Rekeying with laptop for user dan.
This requires the OLD puddle passphrase to unwrap the current key, then the SAME
passphrase to unwrap the new key the founder issued.
puddle passphrase: ****
rekey claim code (NNNN-NNNN): 4827-9163

✓ Migrated to new puddle keypair.
  Old puddle pubkey: ed25519:9f3a…c1d8
  New puddle pubkey: ed25519:7a2b…f04e
  Backups (30d):    /home/dan/.vigo-puddle/identity.wrapped.pre-rekey-1730000060, …
  Cryptsync rewrap: ok (backup at /home/dan/.cryptsync/identity.age.pre-rekey-1730000060)

Run `vigo swarm puddle unlock` (no sudo) to start the session helper under the new puddle identity.
```

**Issuing additional claim codes within the 24h window:**

Re-running `rekey-pair` on the founder inside an active rotation window reissues only the claim code. The keypair, identity files, cryptsync state, and retirement entry are untouched.

```
[laptop] $ vigo swarm puddle rekey-pair
Reissued rekey claim code (rotation already in progress)
  Claim code:      6231-7720
  Code valid for:  5 minutes (300 seconds)
  Window remaining: 23h 47m before founder closes the rekey session
```

**Closing the window early:**

```
[laptop] $ vigo swarm puddle rekey-pair --close
✓ Rekey window closed. Surviving envoys can no longer claim under the
  retired puddle identity. Run `rekey-pair` again to start a new rotation.
```

**What gets preserved across a rekey:**

- Each envoy's per-envoy cryptsync `identity.age` (re-wrapped under the new puddle-derived key, with a 30-day backup retained at `.pre-rekey-<ts>`).
- Cryptsync ciphertext under `~/cryptsync/` (untouched on disk; recipients get re-evaluated on the next encrypt).
- **Founded gitmirror project URLs, bare repos, and pre-rekey tokens** (ADR-019, shipped at 0.35.18–0.35.21). `project_id` stays anchored to the genesis founder pubkey baked into `sha256(founder_puddle_pubkey)`, so `vigo://<project_id>/<handle>` URLs survive the rotation. The on-disk delegation chain at `~/.vigo-gitmirror/<pid>/delegations.json` bridges the genesis pubkey to the post-rekey signing identity — pre-rekey tokens still verify under genesis's authority window, post-rekey admin tokens verify under the new founder pubkey via the chained delegations. The post-rotation hook also rebuilds `tokens.json` to `[bootstrap, fresh_admin_under_NEW]` so subsequent `gitmirror project invite` / `re-invite-all` work without manual intervention. After 5 chained rekeys (`DELEGATION_MAX_LEN = 5`) the next rekey is refused unless `--force-orphan-projects` is passed.

**What does NOT survive a rekey:**

- Envoys offline during the rotation window — they keep the OLD puddle identity. `puddle status` on those envoys surfaces a hint when peers' announces show a divergent puddle pubkey; operator runs `rekey --from <peer>` to migrate.
- During the rotation rollout (between founder rekey and last surviving-envoy rekey), the founder's cryptsync recipient set excludes unrekeyed peers. Files written during this window land at a smaller recipient set than steady state.
- For multi-puddle gitmirror projects (members in foreign puddles), cross-puddle members need a one-time `gitmirror project rejoin` ceremony under a fresh out-of-band capability code from the founder. The founder issues these in batch via `vigo swarm gitmirror project re-invite-all`, prints the JSON invitations to stdout, communicates each block OOB to the matching member puddle — same trust hop as the original `project invite` / `join` flow. Until each cross-puddle member rejoins, their helper daemon will reject the founder's post-rekey pushes (`SignerOutsideFounderWindow` error) and `gitmirror status` flags the divergence. Single-puddle personal-DR projects (founder is the sole member) survive transparently with zero post-rekey ceremony.

**Why the friction is intentional.** Each surviving envoy still needs a fresh out-of-band 5-min claim code from the trusted founder. There is no chain-of-trust shortcut from the OLD puddle key — the threat model includes a stolen device that holds the old key and could otherwise sign a forged "rekey to my pubkey instead" envelope. Pairing-code dictation over voice / SMS / in-person is the same security posture as the original `pair` flow.

**Manual scp fallback (air-gapped envoy):**

```
[laptop] $ scp ~/.vigo-puddle/identity.{wrapped,salt,pub} \
              dan@air-gapped:/home/dan/.vigo-puddle/
[air-gapped] $ vigo swarm puddle unlock
puddle passphrase: ****
✓ Unlocked. Puddle session helper running until logout, lock, or 60 min idle.
```

**Cryptsync auto-bootstrap (ADR-016 slice 4).** When `unlock` runs and `~/.cryptsync/identity.age` is absent, the verb auto-runs `cryptsync puddle init` inline using the just-spawned session helper to derive the wrap key (no second passphrase prompt — the same puddle identity that protects puddle protects cryptsync). Suppressed once cryptsync is already enrolled. Failure to bootstrap is non-fatal: a warning surfaces and the operator can re-run `vigo swarm cryptsync puddle init` manually. `join` does not auto-run cryptsync (the session helper isn't running yet at that point); the next `unlock` after `join` handles it instead.

## File layout

```
~/.vigo-puddle/
  identity.wrapped     # Ed25519 private key wrapped (binary). 0600.
  identity.salt        # 16-byte Argon2id salt. 0600.
  identity.pub         # ed25519:<hex> pubkey, one line + newline. 0644.
  pair.pending         # active pairing-code session (JSON: code_hash + expires_at).
                       # written by `pair`, read+cleared by the daemon's pair-claim handler.
  rekey.pending        # active rekey-claim session (same JSON shape as pair.pending).
                       # written by `rekey-pair`, read+cleared by the rekey-claim handler.
  retired_puddles.json  # historical log of rotations (active + closed). 0600.
                       # consumed by `status` for visibility; not gossiped between envoys.
  identity.wrapped.pre-rekey-<unix_ts>
  identity.salt.pre-rekey-<unix_ts>
  identity.pub.pre-rekey-<unix_ts>
                       # 30-day backups left after `rekey-pair` / `rekey`. Same retention
                       # as cryptsync's identity.age.pre-rekey-<ts>.
  session.unlocked     # session helper PID, present only while unlocked. Siblings probe this.
```

Mode 0700 on `~/.vigo-puddle/` itself, owned by the invoking user.

## Wire interaction

Two routes on the swarm peer port. Both use mTLS (Vigo CA), the same JSON request body shape, and the same response packing:

```
POST https://<host>:1531/swarm/puddle/pair-claim/<user>
POST https://<host>:1531/swarm/puddle/rekey-claim/<user>
Content-Type: application/json
{ "code": "4827-9163" }
```

Server-side validation, identical for both:

1. The user `<user>` exists, has the appropriate sentinel (`pair.pending` or `rekey.pending`), and the sentinel hasn't expired.
2. SHA-256 of the canonicalized incoming code (whitespace + dashes stripped, uppercased) matches the stored hash, constant-time compare.

`rekey-claim` additionally rejects with 410 Gone when no active rekey session exists in `retired_puddles.json`, even when a stale `rekey.pending` survives.

On success: 200 with body `[1 byte salt-len][salt bytes][wrapped blob]`. The sentinel is cleared **before** the response is returned, so the code is single-use even if the response is interrupted and the peer retries.

Failure status codes:

| Status | Meaning |
|---|---|
| 400 | Malformed path or claim body |
| 401 | Code mismatch (don't clear — let the user retry within the TTL) |
| 404 | No active pair / rekey session for that user |
| 410 | Pair session expired, or rekey session past the 24h graceful window |
| 500 | Local read or filesystem issue |

## Configuration

No `~/.vigo-puddle/config.yaml` today. The session helper's idle timeout is hard-coded at 60 minutes. If a per-user override surfaces real demand, add a config the same shape cryptsync uses; until then defaults are the contract.

## Limits

- One puddle per user. Multi-puddle (separate "personal" / "work" identities) is deferred to v2.
- Re-init from scratch on passphrase loss. No Shamir / escrow / quorum recovery.
- Cross-envoy gossip of puddle membership is not in v1; `members` shows only the local view.
- Linux + macOS only. Windows port follows the platform-cadence rule (demand-driven; see CLAUDE.md).

## See also

- [Puddle subsystem](../../engineering/subsystems/puddle.md) — design + code map.
- [ADR-014](../../adr/014-puddle-primitive.md) — design of record.
- [ADR-015](../../adr/015-unified-gitmirror-substrate.md) — first consumer of puddle.
- [ADR-016](../../adr/016-cryptsync-membership-on-puddle.md) — second consumer.
- [ADR-017](../../adr/017-no-puddle-level-revoke.md) — why there is no `revoke` verb.
- [ADR-018](../../adr/018-puddle-rekey.md) — design of the `rekey-pair` / `rekey` verbs.
- [`vigo swarm cryptsync`](agent-swarm-cryptsync.md) — shares the wrap envelope + session-helper pattern.
