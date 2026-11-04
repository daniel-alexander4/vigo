---
title: vigo swarm puddle
weight: 26
---

# `vigo swarm puddle`

`vigo swarm puddle` is the user-facing CLI for the [puddle](../../howto/swarm/set-up-puddle.md) primitive — a per-user Ed25519 puddle identity that subsystems like gitback (ADR-015) and lockbox membership (ADR-016) consume as a single trust unit. State lives under `~/.vigo-puddle/`. No Vigo-server contact for any verb on this page.

## Invocation

Most verbs run as the invoking user against `~/.vigo-puddle/`. The one exception is `join`, which has to read the agent's mTLS key (0600 root-owned) to call out to a peer envoy — `join` requires `sudo`. The rest of the verbs do not.

## Permission model

- **Usercrate authorization (0.41.0+).** Every verb on this page refuses unless the invoking user has `puddle: true` set on the user resource in their usercrate. The agent's user executor writes the resolved flags to `/var/lib/vigo/usercrate-policy/<user>.json` on each reconcile pass; the CLI reads it as a precondition. Refusal points at the cache path so the operator knows whether to enroll the user in a usercrate or wait for the next check-in. Both layers — envoy-level (`swarm.puddle.enabled` pattern list in server.yaml) and user-level (this flag) — must say yes.
- All state is the user's. Identity material (`identity.wrapped`, `identity.salt`) is `0600`; the public key (`identity.pub`) is `0644`. The dot dir itself is `0700`.
- The session helper enforces `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS) on its socket so only the same uid can issue SIGN / STATUS / LOCK.
- The pair-claim HTTP handler trusts callers reaching the swarm peer port (mTLS — same Vigo CA every agent already trusts) **and** requires a matching pairing code that the user just dictated. Either alone is insufficient.

## Verb reference

| Verb | Behavior |
|---|---|
| `init` | Founder envoy, once per puddle ever. Generates a fresh Ed25519 keypair, prompts (with confirm) for the puddle passphrase, wraps the private half via Argon2id + XChaCha20-Poly1305 with a fresh 16-byte salt, writes `identity.wrapped` / `identity.salt` / `identity.pub`. Refuses to overwrite an existing puddle on this envoy. |
| `pair` | On a puddle member, issue a 5-minute single-use 8-character pairing code (`NNNN-NNNN`). Five wrong codes against a session cancel it; re-run `pair` for a new one. Drops `~/.vigo-puddle/pair.pending` (storing the SHA-256 of the code, never plaintext) so the daemon-side pair-claim handler can validate the joining envoy's request. Re-running overwrites any existing pending session. |
| `join --from <host>` | **Requires sudo.** New envoy, once per envoy ever. Prompts for the pairing code, POSTs it to `https://<host>:1531/swarm/puddle/pair-claim/<user>` over mTLS, receives the wrapped blob and salt, prompts for the puddle passphrase, verifies the unwrap, persists `~/.vigo-puddle/` files. Tells the user to run `unlock` next (no helper spawn — runs in user context). |
| `leave [--yes]` | (0.39.15) Leave the puddle on this envoy. Signs a lockbox LEAVE envelope (queued via `~/.lockbox/leave.pending` for the daemon to fan out to live peers within ~30s), locks the puddle session helper, then scrubs `~/.vigo-puddle/`, `~/.lockbox/`, `~/lockbox/`, and `~/.vigo-gitback/`. The local scrub is synchronous; peer notification is asynchronous. Refuses without `--yes`. To re-join later, pair from a current puddle member and `puddle join` on this envoy. Gitback bare repos under `/var/lib/vigo/swarm/gitback/` are root-owned and survive a user-context leave; admin-side cleanup is a separate path. |
| `rekey-pair [--close] [--window-secs <N>] [--force-orphan-projects]` | (ADR-018 / ADR-019) On a current puddle member, rotate the puddle keypair and issue a 5-minute claim code so surviving envoys can migrate. Re-prompts for the puddle passphrase. Runs an ADR-019 pre-flight check that aborts the rotation if any founded gitback project's delegation chain has hit `DELEGATION_MAX_LEN` (5). Atomic-renames `identity.{wrapped,salt,pub}` to `.pre-rekey-<ts>` backups, runs lockbox identity-rewrap, clears stale TOFU pins on local lockbox state, runs the gitback sync (signs + appends a delegation envelope under OLD for every founded project, rewrites `project.json::members[]` OLD → NEW), restarts the session helper, records a 24h graceful retirement entry in `retired_puddles.json`. Re-running inside the active window only reissues a fresh claim code — keypair and on-disk state untouched. `--close` ends the active window; `--window-secs <N>` overrides the 24h default. `--force-orphan-projects` proceeds even if a founded project is at the delegation cap; affected projects get `project.json::tombstoned_at` set on disk (helper daemon refuses git operations on them, CLI verbs skip them) — operator clears via `gitback project re-found <handle>` or drops via `project leave`. |
| `rekey --from <host> [--force-orphan-projects]` | **Requires sudo.** (ADR-018 / ADR-019) On a surviving envoy, claim the new wrapped puddle blob from `<host>`, migrate this envoy onto it. Asks for the puddle passphrase (used both to unwrap the OLD local key for lockbox rewrap and to unwrap the NEW wrapped blob the founder issued under the same passphrase). Same ADR-019 pre-flight check as `rekey-pair`. Atomic-renames identity files, calls lockbox identity-rewrap, clears local TOFU pins, signs + appends its own delegation envelope under OLD for every founded project (each envoy maintains its own self-consistent delegation chain), rewrites local member entries OLD → NEW, locks the session helper. Refuses if the unwrapped blob carries the same puddle pubkey already held locally. `--force-orphan-projects` matches the founder-side flag for symmetry. |
| `unlock [--idle-mins <N>]` | Read the wrapped blob, prompt for the passphrase, unwrap, hand the key to the session helper. Helper holds the key in RAM until logout, lock, or idle timeout (24 h default; the timer resets on each use; `--idle-mins <N>` overrides it for this unlock, `N` ≥ 1). Once the helper is up, also auto-bootstraps lockbox (see below) and, per ADR-023, auto-adopts the gitback projects this puddle founded — mints founder-bootstrap state for every project the swarm is announcing where `sha256(local_puddle_pubkey ‖ handle) == project_id` and there's no local state yet, so `dr_scope: swarm-wide` repos reach this envoy without per-project commands. Both side effects are best-effort: a failure warns and the operator can fix it without re-unlocking. |
| `lock` | Scrub the held key and stop the session helper. Idempotent (no-op if no helper is running). Auto-fires on parent-shell exit and idle timeout. |
| `pubkey` | Print the local puddle pubkey in `ed25519:<hex>` form. Used for invites, capability lists, dictation between envoys. |
| `members` | Show this envoy's view of puddle membership. Phase 1 surfaces only the local envoy's pubkey; cross-envoy gossip lands with ADR-015 / ADR-016. |
| `rotate-passphrase` | Prompt the current passphrase, then a new one (with confirm), re-wrap the local copy of the key under the new passphrase. **This envoy only** — every other puddle envoy retains the old passphrase until rotated separately. The puddle pubkey is unchanged; downstream subsystem trust anchors are unaffected. |
| `status` | Show local puddle state: initialized, puddle pubkey, pair pending, session helper, active rekey window (if any), retired-puddles history, hint when peers are announcing under a different puddle pubkey. |
| `name set <name>` | (ADR-022) Claim `<name>` for this puddle. Requires the puddle unlocked (the session helper signs). Writes a signed `name-claim.json` recording `{puddle_pubkey, name, claimed_at, supersedes?}`. Refuses if the cached fleet name map already shows `<name>` claimed by a different puddle (the *accidental*-collision check — it can't beat the propagation window or a deliberate squatter). Afterwards `gitback://<name>/<repo>` resolves to `gitback://sha256(your-pubkey ‖ <repo>)/<repo>`. Re-running with the same `<name>` re-issues (preserving any `supersedes` chain); with a different name, replaces the old claim. `<name>` follows the gitback project-handle rules and must not look like a 64-hex project_id. |
| `name clear [--yes]` | Drop this puddle's name claim. Warns loudly and refuses without `--yes`: every `gitback://<name>/…` URL anyone wrote down stops resolving, and if another puddle later claims the name those URLs would resolve to *their* projects. Claims do not expire on their own; `clear` is the only way to give a name back. |
| `name show` | Print this puddle's name claim (name, pubkey, claimed-at, supersedes depth, signature OK?) and the fleet view of it from the cached name map (`/var/lib/vigo/swarm/puddle/names.json`) — yours/unambiguous, claimed-by-someone-else, or COLLISION. |

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
✓ Unlocked. Puddle session helper running until logout, lock, or 24h idle.

Bootstrapping lockbox (puddle-mode wrap, no extra passphrase)...
wrote /home/dan/.lockbox/identity.age (0600, wrapped)
wrote /home/dan/.lockbox/identity.salt (0600)
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
  Lockbox rewrap: ok (backup at /home/dan/.lockbox/identity.age.pre-rekey-1730000000)

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
  Lockbox rewrap: ok (backup at /home/dan/.lockbox/identity.age.pre-rekey-1730000060)

Run `vigo swarm puddle unlock` (no sudo) to start the session helper under the new puddle identity.
```

**Issuing additional claim codes within the 24h window:**

Re-running `rekey-pair` on the founder inside an active rotation window reissues only the claim code. The keypair, identity files, lockbox state, and retirement entry are untouched.

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

- Each envoy's per-envoy lockbox `identity.age` (re-wrapped under the new puddle-derived key, with a 30-day backup retained at `.pre-rekey-<ts>`).
- Lockbox ciphertext under `~/lockbox/` (untouched on disk; recipients get re-evaluated on the next encrypt).
- **Founded gitback project URLs, bare repos, and pre-rekey tokens** (ADR-019, shipped at 0.35.18–0.35.21). `project_id` stays anchored to the genesis founder pubkey baked into `sha256(founder_puddle_pubkey ‖ handle)`, so `gitback://<project_id>/<handle>` URLs survive the rotation. The on-disk delegation chain at `~/.vigo-gitback/<pid>/delegations.json` bridges the genesis pubkey to the post-rekey signing identity — pre-rekey tokens still verify under genesis's authority window, post-rekey admin tokens verify under the new founder pubkey via the chained delegations. The post-rotation hook also rebuilds `tokens.json` to `[bootstrap, fresh_admin_under_NEW]` so subsequent `gitback project invite` / `re-invite-all` work without manual intervention. After 5 chained rekeys (`DELEGATION_MAX_LEN = 5`) the next rekey is refused unless `--force-orphan-projects` is passed.

**What does NOT survive a rekey:**

- Envoys offline during the rotation window — they keep the OLD puddle identity. `puddle status` on those envoys surfaces a hint when peers' announces show a divergent puddle pubkey; operator runs `rekey --from <peer>` to migrate.
- During the rotation rollout (between founder rekey and last surviving-envoy rekey), the founder's lockbox recipient set excludes unrekeyed peers. Files written during this window land at a smaller recipient set than steady state.
- For multi-puddle gitback projects (members in foreign puddles), cross-puddle members need a one-time `gitback project rejoin` ceremony under a fresh out-of-band capability code from the founder. The founder issues these in batch via `vigo swarm gitback project re-invite-all`, prints the JSON invitations to stdout, communicates each block OOB to the matching member puddle — same trust hop as the original `project invite` / `join` flow. Until each cross-puddle member rejoins, their helper daemon will reject the founder's post-rekey pushes (`SignerOutsideFounderWindow` error) and `gitback status` flags the divergence. Single-puddle personal-DR projects (founder is the sole member) survive transparently with zero post-rekey ceremony.

**Why the friction is intentional.** Each surviving envoy still needs a fresh out-of-band 5-min claim code from the trusted founder. There is no chain-of-trust shortcut from the OLD puddle key — the threat model includes a stolen device that holds the old key and could otherwise sign a forged "rekey to my pubkey instead" envelope. Pairing-code dictation over voice / SMS / in-person is the same security posture as the original `pair` flow.

**Manual scp fallback (air-gapped envoy):**

```
[laptop] $ scp ~/.vigo-puddle/identity.{wrapped,salt,pub} \
              dan@air-gapped:/home/dan/.vigo-puddle/
[air-gapped] $ vigo swarm puddle unlock
puddle passphrase: ****
✓ Unlocked. Puddle session helper running until logout, lock, or 24h idle.
```

**Lockbox auto-bootstrap (ADR-016 slice 4).** When `unlock` runs and `~/.lockbox/identity.age` is absent, the verb auto-runs `lockbox puddle init` inline using the just-spawned session helper to derive the wrap key (no second passphrase prompt — the same puddle identity that protects puddle protects lockbox). Suppressed once lockbox is already enrolled. Failure to bootstrap is non-fatal: a warning surfaces and the operator can re-run `vigo swarm lockbox puddle init` manually. `join` does not auto-run lockbox (the session helper isn't running yet at that point); the next `unlock` after `join` handles it instead.

**Gitback project auto-adoption (ADR-023).** After the lockbox step, `unlock` reads the agent's `gitback-announced.json` cache (the catch-up reactor refreshes it from the LAN `GITBACKPROJECT:` announces every ~45 s) and, for each announced project where `sha256(local_puddle_pubkey ‖ handle) == project_id` and `~/.vigo-gitback/<pid>/` is absent, mints founder-bootstrap state — the same admin token chain + `project.json` `gitback project init` produces. The DR catch-up reactor then pulls the bare repo on its next tick. This is how `dr_scope: swarm-wide` repos reach a founder's *other* envoys (`project init` only writes state on the founding envoy). It adopts only projects this puddle *founded* with its *current* key — a post-rekey founded project stays anchored to the genesis pubkey, so use `gitback project rejoin` for those; non-founder memberships need an invitation. Each adoption prints `Adopted gitback project '<handle>'`; failures warn and don't undo the unlock.

## Friendly names (ADR-022)

`gitback://<project_id>/<repo>` is self-certifying — `project_id = sha256(founder_puddle_pubkey ‖ repo)` so it can't be squatted and resolves identically everywhere — but it's a 64-char hex string nobody can type or remember. `name set` claims a human-meaningful name for your puddle so `gitback://<name>/<repo>` works alongside the hex form. Resolution: `<name>` → puddle pubkey (via the claim) → `project_id = sha256(pubkey ‖ <repo>)` → proceed exactly as a hex URL would.

```
[laptop] $ vigo swarm puddle unlock
puddle passphrase: ****
✓ Unlocked. …

[laptop] $ vigo swarm puddle name set dan
✓ Claimed puddle name 'dan'
  Puddle pubkey: ed25519:8711b12bac759fea30363f0ed4ab95b4…
  Resolves:      gitback://dan/<repo>  →  gitback://8711b12bac759fea…/<repo>  (project_id = sha256(pubkey ‖ repo))

# works from this envoy immediately, and from other envoys once the fleet name map refreshes:
$ git clone gitback://dan/vigo-web
git-remote-gitback: resolved gitback://dan/vigo-web → gitback://e3b0c44298fc…b855/vigo-web (founder ed25519:8711b12bac75…0a9aea8)
```

Properties — read these before relying on it:

- **Resolves immediately from the claiming envoy.** A push/clone of `gitback://<your-name>/<repo>` from the same envoy works the instant `name set` returns — the helper daemon's resolver falls back to your own `~/.vigo-puddle/name-claim.json` when the fleet name map hasn't picked up the claim yet. Other envoys resolve it once their map refreshes (their next check-in, then ~15 min). The hex form is always unambiguous everywhere.
- **First-come, cooperatively enforced.** `name set` refuses a name another puddle already holds *in the agent's current fleet view* — that kills the accidental collision (Alice and Bob both reaching for `mainbox`). It does **not** beat the propagation window (two puddles can both check "free" and claim within seconds) and does **nothing** against a deliberate squatter.
- **A friendly name is a hint, not a trust anchor.** If an attacker claims `acme` before the real Acme does, `gitback://acme/billing` cleanly resolves to the attacker's repo with no warning (unless the real Acme also has a claim — then it's a collision → resolution errors). Inherent to decentralized naming. The clone output prints the resolved `(project_id, founder pubkey)` so a vigilant operator can spot a mismatch; the unforgeable form is always `gitback://<project_id>/<repo>`.
- **Collisions degrade to ambiguous, never to wrong.** A resolver seeing two unrelated claims for `dan` errors and tells you to use the hex form until a human runs `name clear` on one. No automatic tiebreaker.
- **`re-found` follows.** A bare puddle rekey doesn't touch the claim (the friendly URL keeps resolving to your pre-rekey projects). A `gitback project re-found` mints a fresh project anchor, so it auto-re-issues the claim pointing at the new anchor with a `supersedes` chain proving descent — `gitback://<name>/<repo>` transparently follows it; the hex `gitback://<old_pid>/<repo>` breaks (as it always has on re-found).
- **Repo handles are uncontested within a puddle.** `project_id = sha256(pubkey ‖ handle)` already guarantees one puddle can't have two `vigo`s — there's no claiming for repo handles, ever. This is GitHub's model: puddle name ≈ `github.com/<user>`, repo handle ≈ the repo name.
- **Propagation.** Claims ride the `gitback` trait → the server aggregates them into the fleet name map (`GET /api/v1/swarm/puddle/names`, view it with `vigocli swarm puddle names list`) → each agent caches it at `/var/lib/vigo/swarm/puddle/names.json` on startup + every 15 min. Until a peer's agent has fetched a refresh that includes your claim, `gitback://<name>/<repo>` won't resolve there — but the hex form always does. The server *publishes* the map; it never arbitrates.

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
  name-claim.json      # signed friendly-name claim (ADR-022): {body:{puddle_pubkey,
                       # name, claimed_at, supersedes?}, signature}. 0644 (public-by-
                       # construction). Written by `name set`, removed by `name clear`,
                       # read by the gitback traits collector → fleet name map.
  identity.wrapped.pre-rekey-<unix_ts>
  identity.salt.pre-rekey-<unix_ts>
  identity.pub.pre-rekey-<unix_ts>
                       # 30-day backups left after `rekey-pair` / `rekey`. Same retention
                       # as lockbox's identity.age.pre-rekey-<ts>.
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

The session helper's idle timeout defaults to **24 hours**; override it per-unlock with `vigo swarm puddle unlock --idle-mins <N>` (`N` minutes, ≥ 1). The timer resets on every SIGN / STATUS request, so an actively-used puddle stays unlocked regardless. There's no `~/.vigo-puddle/config.yaml` persistent override today (lockbox's separate session helper has one via `idle_timeout_minutes`; the puddle helper doesn't) — the per-unlock flag is the knob.

## Limits

- One puddle per user. Multi-puddle (separate "personal" / "work" identities) is deferred to v2.
- Re-init from scratch on passphrase loss. No Shamir / escrow / quorum recovery.
- Cross-envoy gossip of puddle membership is not in v1; `members` shows only the local view.
- Linux + macOS only. Windows port follows the platform-cadence rule (demand-driven; see CLAUDE.md).

## See also

- [Puddle subsystem](../../howto/swarm/set-up-puddle.md) — design + code map.
- ADR-014 — design of record.
- ADR-015 — first consumer of puddle.
- ADR-016 — second consumer.
- ADR-017 — why there is no `revoke` verb.
- ADR-018 — design of the `rekey-pair` / `rekey` verbs.
- ADR-022 — design of the `name` verbs + the fleet name map.
- [`vigo swarm lockbox`](agent-swarm-lockbox.md) — shares the wrap envelope + session-helper pattern.
- [`vigocli swarm puddle`](swarm-puddle.md) — admin-side cross-envoy puddle ops (`evict`, `names list`).
