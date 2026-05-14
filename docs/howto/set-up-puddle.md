# Set up Puddle

A **puddle** is the per-user identity that ties together "this user's collection of envoys, treated as one trust unit." It's the prerequisite for lockbox (per-user encrypted directory) and gitback (puddle-keyed git repos) — both subsystems consume puddle membership to express who can read / write.

## Enable it

Puddle is per-user, not per-fleet. Grant a user the `puddle: true` flag in their usercrate:

```yaml
# /srv/vigo/stacks/stockpile/usercrates/dan.vgo
resources:
  - type: user
    username: dan
    home: /home/dan
    shell: /bin/bash
    puddle: true              # enables puddle + (as of 0.40.0) lockbox
    gitback: true             # opt-in for git-over-swarm too
    authorized_keys: |
      ssh-ed25519 AAAA... operator@laptop
```

Publish (`sudo vigocli config publish`); the next agent convergence creates the necessary directories and dependency-installs.

## Use it

On the user's first envoy (the **founder envoy**):

```bash
vigo swarm puddle init       # generate keypair, set passphrase
vigo swarm puddle status     # confirm
```

On every additional envoy where the user wants the same puddle identity:

```bash
# On the founder envoy:
vigo swarm puddle pair       # prints a 5-minute single-use code

# On the new envoy:
vigo swarm puddle join --from <founder-host>   # claims the pair code over swarm mTLS
vigo swarm puddle unlock                       # enter passphrase, bring the local session up
```

The puddle's full verb tree: `init,pair,join,rekey-pair,rekey,unlock,lock,pubkey,members,rotate-passphrase,status,name {set,clear,show}`. Day-to-day, the operator only touches `unlock` (every login) and `status` / `members` when curious.

## How it works

Puddle is a per-user identity primitive — a single Ed25519 keypair representing "this user's collection of envoys, treated as one trust unit." Sibling subsystems (gitback under [ADR-015](../adr/015-unified-gitback-substrate.md), lockbox membership under [ADR-016](../adr/016-lockbox-membership-on-puddle.md)) consume it to express membership and drive onboarding without re-doing the per-pair-of-envoys ceremony each subsystem invented in isolation.

### Why a puddle identity exists

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
- idle timeout (default 24 hours; resets on every SIGN / STATUS; override per-unlock with `vigo swarm puddle unlock --idle-mins <N>`).

The key never lives on disk in plaintext. The CLI pipes the unwrapped key bytes through the helper's stdin and immediately closes the pipe; both ends go through `Zeroizing` to scrub any in-process leftover.

## State files

User-side, all under `~/.vigo-puddle/` (mode 0700, owned by the user):

| File | Mode | Purpose |
|---|---|---|
| `identity.wrapped` | 0600 | Ed25519 private key wrapped under user passphrase. Portable across envoys. |
| `identity.salt` | 0600 | 16 random bytes, Argon2id input. Portable across envoys. |
| `identity.pub` | 0644 | `ed25519:<hex>` puddle pubkey, one line. Public; sibling subsystems read this. |
| `pair.pending` | 0600 | Active pairing-code session (`code_hash` + `expires_at`). Removed on first claim or TTL expiry. |
| `rekey.pending` | 0600 | Active rekey-claim session (same JSON shape as `pair.pending`). |
| `retired_puddles.json` | 0600 | Rotation log (active + closed). Consumed by `status`; not gossiped. |
| `session.unlocked` | 0600 | Session-helper PID. Written on unlock; removed on lock/idle/parent-gone. Siblings probe + `pid_alive` to detect lock state. |
| `name-claim.json` | 0644 | Signed friendly-name claim (ADR-022): `{body:{puddle_pubkey, name, claimed_at, supersedes?}, signature}`. Public-by-construction. Written by `name set`, removed by `name clear`, read by the `gitback` traits collector. |

Daemon-side state: the pair/rekey-claim handlers are stateless (they read `pair.pending` / `rekey.pending` directly under the target user's home). One daemon-owned per-envoy cache: `/var/lib/vigo/swarm/puddle/names.json` (0644) — the fleet friendly-name map, refreshed from `GET /api/v1/swarm/puddle/names` on agent startup + every 15 min by the `names_bootstrap` loop, never on the check-in path. Read by the `git-remote-gitback` resolver (via the helper daemon) and by `vigo swarm puddle name set`'s collision check.

## Friendly names (ADR-022)

`gitback://<project_id>/<repo>` is self-certifying — `project_id = sha256(founder_puddle_pubkey ‖ repo)` — but unrememberable. A **name claim** binds a human name to a puddle pubkey so `gitback://<name>/<repo>` resolves as `sha256(<that pubkey> ‖ <repo>)`. The claim is a small signed blob (domain `gitback-name-v1\0`, hand-rolled canonical encoding like `gitback::tokens` / `gitback::envelopes`) at `~/.vigo-puddle/name-claim.json`; `name set` requires the puddle unlocked (the session helper signs).

- **Propagation is via traits, not gossip.** The `gitback` traits collector reports each user's claim verbatim → `gitbackmesh` joins them into a fleet `name → claims` map (deduped per pubkey, collisions kept) → `GET /api/v1/swarm/puddle/names` → each agent caches it. The server **publishes, never arbitrates** — this is the same passive-aggregator role `gitbackmesh` already plays for project rosters, and it's off the check-in hot path.
- **First-come, cooperatively enforced; ambiguous-on-collision.** `name set` refuses a name another puddle holds in the agent's current view (kills the accidental collision); it can't beat the propagation window or a deliberate squatter. Two unrelated claims for one name → resolvers error and require the hex form. No automatic tiebreaker. `re-found` re-issues the claim with a `supersedes` rekey-delegation chain so a resolver can pick the descendant — a bare rekey doesn't touch the claim.
- **A name is a hint, not a trust anchor.** Spoofable by squatting, stated loudly. The unforgeable form is always `gitback://<project_id>/<repo>`. The `git-remote-gitback` resolver prints the resolved `(project_id, founder pubkey)` so a vigilant operator can spot a mismatch.

See [ADR-022](../adr/022-gitback-friendly-names.md) for the full design + Zooko-triangle tradeoffs, and [agent-swarm-puddle § Friendly names](../reference/vigocli/agent-swarm-puddle.md#friendly-names-adr-022) for the operator-facing walkthrough.

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
vigo swarm puddle name set <name>       # ADR-022: claim a friendly name for this puddle (unlocked)
vigo swarm puddle name clear [--yes]    # ADR-022: drop the claim (breaks gitback://<name>/… URLs)
vigo swarm puddle name show             # ADR-022: local claim + fleet view of it
```

See [agent-swarm-puddle](../reference/vigocli/agent-swarm-puddle.md) for the full reference.

## Failure modes

- **Passphrase forgotten.** No recovery in v1. Re-init creates a new puddle (different pubkey); existing project tokens / lockbox recipients become orphaned. Documented loudly. `status` surfaces "puddle locked, recovery requires passphrase or re-init."
- **All wrapped key copies lost across all envoys.** Same as passphrase forgotten — re-init from scratch. Disaster recovery for the user, not silent data loss.
- **Single envoy compromised.** Wrapped blob + passphrase = attacker has the puddle key. Mitigations: short Argon2id verification time on unlock (cost factor matched to lockbox), session-helper idle lock, system-level disk encryption strongly recommended.
- **Pair-claim code brute force.** 100M code space + 5-min TTL + single-use + mTLS-only callers. Every probe is forensically attributable.

## Code map

- `agent/src/swarm/puddle/mod.rs` — public API (`local_puddle_pubkey`, `is_initialized`, `local_name_claim`).
- `agent/src/swarm/puddle/config.rs` — paths under `~/.vigo-puddle/`.
- `agent/src/swarm/puddle/cli.rs` — verb dispatcher (incl. `name {set,clear,show}`); status surface enrichment; `write_user_file_0644`.
- `agent/src/swarm/puddle/codes.rs` — generic 8-digit claim-code primitives (shared by pair + rekey).
- `agent/src/swarm/puddle/pair.rs` — pair-specific path bindings + HTTP claim handler.
- `agent/src/swarm/puddle/rekey.rs` — `RetiredFleetEntry` log + active-session predicate + post-rotation lockbox sync + rekey-claim HTTP handler (ADR-018).
- `agent/src/swarm/puddle/session.rs` — session helper daemon.
- `agent/src/swarm/puddle/name.rs` — `NameClaim` (canonical encoding, issue/verify, persistence) + `resolve_claims` (supersedes-chain walk) (ADR-022).
- `agent/src/swarm/puddle/names_cache.rs` — on-disk shape of the fleet name map; `verified_claims` / `claimed_by_other`.
- `agent/src/swarm/puddle/names_bootstrap.rs` — periodic fetch of `GET /api/v1/swarm/puddle/names` → local cache (mirrors `gitback::pubkey_bootstrap`).
- `server/swarm/gitbackmesh/gitbackmesh.go` — `FleetNames` + `NamesSnapshot` / `groupNameClaims`; `server/api/swarm_gitback.go` — `GET /api/v1/swarm/puddle/names`.
- `agent/src/swarm/gitback/helper.rs` — `RESOLVE-NAME` socket verb; `agent/src/swarm/gitback/remote_helper.rs` — `gitback://<name>/<repo>` URL branch.
- `agent/src/swarm/lockbox/identity_rewrap.rs` — atomic re-wrap of the per-envoy age key from one puddle keypair's wrap-key to another (ADR-018 prereq).
- `agent/src/swarm/server.rs` — wires `POST /swarm/puddle/pair-claim/<user>` and `POST /swarm/puddle/rekey-claim/<user>` to the respective handlers.
- `agent/src/main.rs` — argv dispatch for `swarm puddle *` and the hidden `puddle-session-daemon` verb.

## See also

- [ADR-014](../adr/014-puddle-primitive.md) — the design.
- [ADR-015](../adr/015-unified-gitback-substrate.md) — unified gitback substrate built on puddle (next).
- [ADR-016](../adr/016-lockbox-membership-on-puddle.md) — lockbox membership on puddle (after 015).
- [ADR-017](../adr/017-no-puddle-level-revoke.md) — why there is no puddle-level revoke.
- [ADR-018](../adr/018-puddle-rekey.md) — design of the rekey flow.
- [ADR-022](../adr/022-gitback-friendly-names.md) — friendly names (`name set/clear/show`, the fleet name map).
- [Gitback](set-up-gitback.md) — the URLs that friendly names resolve to.
- [Lockbox](set-up-lockbox.md) — shares the wrap envelope and the session-helper shape.
