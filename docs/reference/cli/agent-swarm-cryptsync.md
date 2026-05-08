
# vigo swarm cryptsync CLI Reference

`vigo swarm cryptsync` is the user-facing interface to [cryptsync](../../engineering/subsystems/cryptsync.md), the per-user encrypted P2P directory. It runs inside the `vigo` agent binary, operates on files under the invoking user's home, and does not require root. The agent daemon is never contacted directly by the CLI — `unlock` spawns its own session helper daemon, and `encrypt`/`decrypt` talk to that helper over a local Unix socket.

## Invocation

```
vigo swarm cryptsync <verb> [args]
```

Every verb runs under the invoking user's uid. No root. The agent daemon does not need to be running for `init`, `enroll`, `status`, or `encrypt` — only for fan-out of the written ciphertext to other envoys.

## Permission model

- `init` and `enroll` write `identity.age` + `identity.salt` + `recipients.txt` with mode 0600 under `~/.cryptsync/`, owned by the invoking user. The cryptsync password wraps the age identity with Argon2id — the agent running as root cannot decrypt these files without the password.
- `unlock` spawns the session helper daemon. The helper is `setsid`-detached so it outlives the shell that ran `unlock`. It holds the unwrapped key in its own process memory and serves DECRYPT/STATUS/LOCK over a Unix socket in `$XDG_RUNTIME_DIR/vigo/` (Linux) or `$TMPDIR/cryptsync-<UID>.sock` (macOS), mode 0600, `SO_PEERCRED`-gated so only the same uid can connect.
- `encrypt` reads `recipients.txt` (public keys only, no secret material) and writes ciphertext into `~/cryptsync/`. Does not require `unlock` — encryption is a pure pubkey operation.
- Files written into `~/cryptsync/` — by `encrypt`, `reencrypt`, or by an incoming peer push — land as **mode 0600 owned by the invoking user**. The agent binary lives in `/usr/local/sbin/vigo` and writes happen in a root context, so each write path drops ownership back to the user (via atomic temp + `chmod 0600` + `chown` + rename) before the file is exposed under its final name. No file in `~/cryptsync/` should ever be world-readable or root-owned; if you find one, it predates 0.30.24 — `vigo swarm cryptsync reencrypt` rewrites every file through the corrected path.
- Files written into `~/.cryptsync/` — `identity.age`, `identity.salt`, `recipients.txt`, `config.yaml`, the `regen.pending` / `leave.pending` sentinels — land as **mode 0600 owned by the invoking user** for the same reason. `~/.cryptsync/` itself is mode 0700 owned by the user. The decrypt `-o <path>` plaintext output lands as **mode 0600 owned by the invoking user** even when the destination is outside `~/cryptsync/`. Behavior is robust against env-stripping `sudo` policies that reset `HOME`/`USER` to root's values: the resolution chain prefers real `getuid()` (preserved through setuid), then `SUDO_USER` (sudo's authoritative original-invoker record), then the `USER` env var.
- `decrypt` sends ciphertext bytes to the running session helper and receives plaintext back. Requires a live helper (i.e. `unlock` has been run).
- `lock` shuts down the helper and scrubs the key. Auto-fires on logout, idle timeout, or explicit invocation.

## Commands

Per [ADR-020](../../adr/020-cryptsync-puddle-only.md) + [ADR-021](../../adr/021-cryptsync-lifecycle-on-puddle.md) (0.40.0), cryptsync is puddle-only and its lifecycle layer absorbed into puddle. The subtree below is content-domain only: encrypt, decrypt, push, list, status, reencrypt, resurrect, purge, plus `puddle init / renew-cert` for recovery. Lifecycle verbs `init / leave / forget <host>` retired — use `vigo swarm puddle init` (founder), `vigo swarm puddle leave` (exit), and `vigocli swarm puddle evict <host>` (admin-side eviction) instead.

### Content-domain verbs

| Verb | Purpose |
|---|---|
| `unlock` | Prompt password, spawn session helper that holds the unwrapped identity. After the helper is up, walks `~/cryptsync/` and encrypts any plaintext that landed while locked (the **unlock sweep**, ADR-012). |
| `lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/cryptsync/` to every current recipient. (Equivalent to dropping the file directly into `~/cryptsync/` while unlocked — the watcher auto-encrypts new plaintext per ADR-012.) Refuses with a tombstone-preflight error if the target name has an active tombstone — see `resurrect`. |
| `resurrect <file> [--as <name>]` | Override an active tombstone for the target name and re-encrypt in one step. Drops a `~/.cryptsync/resurrect.pending` sentinel; the daemon signs a `ResurrectEnvelope`, persists a local resurrect record, and fans out to live peers within ~1-2s on its dedicated 1s polling tick. The CLI awaits the daemon's sentinel removal (60s ceiling) before invoking the encrypt path so peers have the resurrect record before the ciphertext arrives. Refuses if the target name has no active tombstone (use `encrypt` directly). See [Resurrecting a deleted filename](../../engineering/subsystems/cryptsync.md#resurrecting-a-deleted-filename). |
| `decrypt <file> -o <path>` | Decrypt from `~/cryptsync/` to a user-chosen path. Requires `unlock`. `-o` is mandatory. |
| `purge --yes` | Power-user wipe of ALL local cryptsync state for this user (`~/.cryptsync/` + `~/cryptsync/`). Does NOT gossip; peers age the envoy out via the staleness rule. Recovery / decommission verb. For the canonical exit (with peer notification + puddle + gitmirror cleanup), use `vigo swarm puddle leave` instead. |
| `reencrypt` | Force re-encrypt of every file in `~/cryptsync/` to the current recipient set. Requires `unlock`. |
| `status` | Show local state: enrolled, recipient count, file-count breakdown (ciphertext / plaintext / failed), session helper status. Under puddle membership, the `recipients:` line expands whenever an expected member is missing from the live recipient set — `N of M puddle members — <host> silent for <duration>` (singular) or `N of M puddle members — K peers silent (longest <duration>)` (plural). The `M` denominator is sourced from `puddle_members.json` (the append-only "ever-known members" table introduced in 0.39.12) and survives `prune_stale`, so the gap line keeps rendering with a real silent-for duration even after the absent envoy has been dropped from `peers.json`. Members are added on first sighting under the local user's puddle pubkey; they're removed only by an authoritative LEAVE (`vigo swarm puddle leave` / `vigocli swarm puddle evict`). |

### Lifecycle (puddle subtree)

| Verb | Purpose |
|---|---|
| `puddle init` | First-time setup of this user's per-envoy age key under puddle membership (ADR-016, ADR-020). Refuses if `vigo swarm puddle init` hasn't been run for this user. Auto-issues a puddle-membership certificate (see below) so the very first announce carries cryptographic proof of puddle membership. Usually auto-run by `puddle unlock` — manual invocation is for recovery. |
| `puddle renew-cert` | Re-issue the puddle-membership certificate. Default lifetime 30 days; certs expire and need renewal. Run from any envoy where puddle is unlocked — the renewed cert ships in the next announce tick. |

The wrapped age identity at `~/.cryptsync/identity.age` is wrapped under a key derived from the puddle identity via `session::request_sign(b"vigo-cryptsync-wrap-v1")` (deterministic Ed25519 signature over a fixed challenge), passed through Argon2id. `unlock` requires the puddle session helper to be running — there is no per-envoy cryptsync passphrase prompt. If the session helper isn't running, `unlock` fails with `run vigo swarm puddle unlock first` rather than silently falling back to anything else.

**Auto-bootstrap on `puddle unlock`.** When `vigo swarm puddle unlock` runs and `~/.cryptsync/identity.age` is absent, the verb auto-runs `cryptsync puddle init` inline using the just-unlocked session helper to derive the wrap key. The user gets a working encrypted-sync surface as a single side effect of unlocking — no second passphrase prompt. Failure to bootstrap (e.g. puddle not initialized, or `cryptsync puddle init` errors) doesn't undo the unlock; the warning surfaces and the operator can re-run `cryptsync puddle init` manually.

Per-envoy age keys are deliberately preserved — see ADR-016 for the rationale (per-envoy compartmentalization stays intact at the age-key level even though the wrap key in puddle mode is shared).

### Trust-on-first-use puddle-pubkey pinning

A peer's `puddle_pubkey` is pinned the first time it's seen. Subsequent announces from the same hostname under a different puddle pubkey are dropped at upsert time (the daemon warn-logs the rejection with both the pinned and announced values). This guards against same-LAN spoofing — once `host-X` is observed claiming `fleet-pk-1`, a different envoy claiming to be `host-X` under `fleet-pk-2` is silently ignored.

Operator recovery for a legitimate puddle rotation (rare):

```
$ vigo swarm cryptsync forget <hostname>
```

clears the pinned entry. The next announce from that hostname re-pins under the new puddle pubkey.

### Fleet membership certificate

Each puddle-mode envoy holds a puddle-membership certificate at `~/.cryptsync/puddle_cert.json`. The cert binds the envoy's age pubkey to the user's puddle pubkey via an Ed25519 signature; receivers verify the signature against the announce's claimed `puddle_pubkey` before accepting the fleet claim. This closes the same-LAN first-contact race that TOFU pinning has on its own — only the holder of the fleet's private key can mint a verifiable claim.

Cert shape:

```json
{
  "version":           1,
  "envoy_age_pubkey":  "age1...",
  "puddle_pubkey":      "<64-hex>",
  "issued_at":         <unix-seconds>,
  "expires_at":        <unix-seconds>,
  "signature":         "<128-hex>"
}
```

**Lifetime.** 30 days by default. The cert is auto-issued by `cryptsync puddle init`; renew with `cryptsync puddle renew-cert` whenever puddle is unlocked. `cryptsync status` prints "expires in N days" / "EXPIRED N days ago" so a renewal coming up is visible.

**Verification on the receive side.** Every cert claim is checked against the announce: `envoy_age_pubkey` must match `Announcement.pubkey`, `puddle_pubkey` must match `Announcement.puddle_pubkey`, the signature must verify against `puddle_pubkey`, and `expires_at` must be in the future. Any failure → the daemon warn-logs and drops the fleet claim entirely (peer slots into standalone filtering — still reachable, just not in the fleet's recipient set).

**Operator triage.** When peers warn-log a cert expiry against this envoy: run `cryptsync puddle renew-cert` (requires `puddle unlock` first). When peers warn-log a cert mismatch (rare): the user rotated puddle on this envoy — clear the pin on each peer with `vigo swarm cryptsync forget <hostname>`, run `cryptsync puddle renew-cert`, and let TOFU re-pin under the new puddle pubkey.

### `vigo swarm cryptsync leave`

Self-unenrollment. Three-stage handoff:

1. **CLI** (user context) validates enrollment, reads the local age pubkey from `recipients.txt`, writes `~/.cryptsync/leave.pending` containing the pubkey + hostname + leave time + flags, exits.
2. **Daemon reactor** (running as root, ticks every ~30 s) polls for the sentinel. When present: loads the daemon Ed25519 signing key, builds a signed [LEAVE envelope](../../engineering/subsystems/cryptsync.md#signed-leave-envelope), fans it out to every live peer (+ cross-subnet peers). Persists the record to `/var/lib/vigo/swarm/cryptsync/<user>/leaves.json`. Scrubs `~/.cryptsync/` (default) and `~/cryptsync/` (with `--purge`). Removes the sentinel.
3. **Peer envoys** receive the envelope via `POST /cryptsync/leaves/<user>`, verify the signer is the subject (cryptographic self-leave-only), drop the pubkey from `peers.json`, regenerate `recipients.txt`, drop `reencrypt.pending`, persist.

**Flags:**
- `--purge` — also delete `~/cryptsync/` ciphertext on scrub. Default: keep ciphertext (it's already orphaned — no identity to decrypt).
- `--keep-identity` — keep `~/.cryptsync/` wrapped identity so a subsequent `vigo swarm cryptsync enroll` can reuse it. Rarely wanted; default is scrub.
- `--yes` — skip confirmation.

**Offline peers:** a peer that was offline during the fan-out misses the envelope but naturally drops the leaving envoy within 90 s via the peer-staleness rule in `state::prune_stale`. The admin path [`vigocli swarm cryptsync evict`](swarm-cryptsync.md) is the tool for forcing eviction when a host is already gone and the user can't run `leave` from it.

### `vigo swarm cryptsync purge`

Power-user counterpart to `leave`. Wipes `~/.cryptsync/` and `~/cryptsync/` for the calling user immediately and unconditionally (after `--yes`). Does **not** drop a sentinel; the daemon never sees the departure, so peers do not get a signed LEAVE envelope — they age the envoy out via the staleness rule (~90 s) instead.

```
vigo swarm cryptsync purge --yes
```

**When to use it.** Decommissioning an envoy where you don't care about clean gossip and want every cryptsync byte gone right now. For a graceful departure where peers should learn of it, run `leave` first.

**Behaviour.**
- Without `--yes`: prints what would be wiped and exits without touching anything.
- With `--yes`: removes both `~/.cryptsync/` and `~/cryptsync/`. If `leave.pending` or `reencrypt.pending` existed at purge time, prints a follow-up note that peers will not learn of the departure unless announced out-of-band.
- The root-owned per-user mirror under `/var/lib/vigo/swarm/cryptsync/<user>/` is **not** touched (the calling user can't write there); it ages out as the daemon stops seeing this envoy.

## File layout

```
~/.cryptsync/
  identity.age     # wrapped age private key (binary: [version][nonce][ciphertext+tag])
  identity.salt    # 16-byte random Argon2id salt
  recipients.txt   # every enrolled envoy's pubkey for this user, one per line
  config.yaml      # user-managed: cross_subnet_peers, idle_timeout_minutes
  enroll.pending   # sentinel dropped by the user executor; removed on first enroll
  regen.pending    # sentinel dropped by `vigo swarm cryptsync enroll` after seeding self
                   # so the daemon folds existing live peers into recipients.txt
                   # within one reactor tick (≤30 s). Removed by the daemon.
  tombstones.json  # daemon-written user-readable mirror of the tombstone table
  leaves.json      # daemon-written user-readable mirror of the LEAVE record table
  peers.json       # daemon-written user-readable mirror of the live peer table
  puddle_members.json # daemon-written user-readable mirror of the append-only
                   # puddle-members table (drives the `recipients:` gap line in
                   # `cryptsync status` past prune)
  session.unlocked # sentinel containing the session helper's PID; written on
                   # unlock, removed on lock/idle/parent-gone. Watcher reads
                   # this to gate auto-encrypt-on-write (ADR-012).
  staging/         # transient ciphertext stagers used by the auto-encrypt
                   # path. Empty under normal operation; orphaned files from
                   # a crash are 0600 user-owned and safe to delete.

~/cryptsync/
  foo.pdf          # age ciphertext at rest
  notes.md         # age ciphertext at rest
  big.bin.20260429194712.failed
                   # auto-encrypt failed unrecoverably for big.bin; rename
                   # back to big.bin to retry. The watcher's resolve_event
                   # filter ignores the .<ts>.failed shape so it doesn't loop.
```

Plus agent-owned state at `/var/lib/vigo/swarm/cryptsync/<user>/{peers,tombstones,leaves}.json` (root-owned, 0600) — the canonical tables the daemon maintains. `tombstones.json` and `leaves.json` are mirrored to `~/.cryptsync/` (user-owned, 0600) on every save so `vigo swarm cryptsync list` and `status` can read them without sudo. `peers.json` has no user-side mirror today; the user CLI doesn't display peer state.

## Typical flow

**Primary envoy, one-time:**
```
$ vigo swarm cryptsync init
cryptsync password: ****
confirm password:   ****
wrote ~/.cryptsync/identity.age (0600, wrapped)
wrote ~/.cryptsync/identity.salt (0600)
wrote ~/.cryptsync/recipients.txt
created ~/cryptsync (0700)

public key: age1abc...
```

**Secondary envoy (after usercrate applied with `cryptsync: true`):**
```
$ ssh dan@laptop.home
$ vigo swarm cryptsync status       # detects enroll.pending, runs enrollment interactively
cryptsync password: ****
...
```

**Daily use:**
```
# Either explicit:
$ vigo swarm cryptsync encrypt ~/Downloads/tax.pdf
✓ Encrypted to 3 recipient(s); wrote ~/cryptsync/tax.pdf

# Or drop-and-forget (watcher auto-encrypts in place while unlocked):
$ cp ~/Downloads/tax.pdf ~/cryptsync/

$ vigo swarm cryptsync unlock
cryptsync password: ****
✓ Unlocked. Session helper running until logout, lock, or 60 min idle.
cryptsync sweep: 1 file(s) encrypted, 0 failed for dan

$ vigo swarm cryptsync decrypt tax.pdf -o /tmp/tax.pdf
✓ Wrote 847293 bytes to /tmp/tax.pdf

$ vigo swarm cryptsync lock
✓ Locked. Key scrubbed.
```

The sweep line after `unlock` reports any plaintext files encrypted on the way in — files that were dropped while the session was locked. While unlocked, the watcher auto-encrypts new plaintext immediately; the sweep only has work when something landed during the locked window. See [Auto-Encrypt-on-Write](../../engineering/subsystems/cryptsync.md#auto-encrypt-on-write) for the full rules (1 GiB cap, three-class failure handling, `<orig>.<ts>.failed` recovery).

**Listing files:**
```
$ vigo swarm cryptsync list
  FILENAME               SIZE        MODIFIED            STATUS
  notes.md               3.4 KiB     4m ago              live
  tax-2025.pdf           847 KiB     2d ago              live
  old-report.pdf         —           43d ago             tombstoned (47d left)
```

Flags:
- `--long` / `-l` — add SHA-256 column (ciphertext hash) for live files
- `--tombstones` — show only deletion records, skip live files
- `--sort=name` — alphabetical; default is mtime descending

`ls` is an accepted alias for `list`.

**Deleting a file from the mesh:**
```
$ vigo swarm cryptsync delete tax.pdf
✓ Removed /home/dan/cryptsync/tax.pdf; tombstone will propagate on next watcher cycle.
```

`vigo swarm cryptsync delete` is equivalent to `rm ~/cryptsync/<name>` — both trigger the watcher's Remove-event path, which signs a DELETE envelope and fans it out to every peer. Receivers record a tombstone (retention 90 days by default) and remove their local copy. Any future push of the same filename while the tombstone is active is rejected with 410 Gone. To reuse a deleted filename before the tombstone ages out, encrypt under a new name.

**Revoking an envoy:**
```
$ vigo swarm cryptsync forget laptop.home
✓ Forgot laptop.home. Recipients regenerated.
  Run `vigo swarm cryptsync reencrypt` to re-encrypt existing files
  so the revoked host cannot decrypt them from its cached ciphertext.

$ vigo swarm cryptsync unlock
cryptsync password: ****
$ vigo swarm cryptsync reencrypt
✓ Re-encrypted 47 file(s) to 2 recipient(s); skipped 0
```

## Configuration

`~/.cryptsync/config.yaml` is optional:

```yaml
cross_subnet_peers:
  - envoy-london.internal   # cross-subnet peers unreachable via LAN multicast
idle_timeout_minutes: 60    # session helper auto-lock (default 60)
```

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable server-side) |
| Content scope | Regular files in top-level `~/cryptsync/` only |
| Filename encryption | No — filenames are plaintext on disk and on the wire |
| Platforms | Linux + macOS (Phase 1) |

## See also

- [cryptsync concept](../../engineering/subsystems/cryptsync.md) — architecture, threat model, relationship to filesync
- [Filesync](../../engineering/subsystems/filesync.md) — the plaintext sibling
