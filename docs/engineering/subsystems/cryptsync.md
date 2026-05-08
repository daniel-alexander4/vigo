
# Cryptsync — Per-User Encrypted P2P Directory

> **Heads up — major reshape in 0.40.0.** Standalone mode retires ([ADR-020](../../adr/020-cryptsync-puddle-only.md)) and cryptsync's lifecycle layer (`init`, `leave`, `forget`) absorbs into puddle ([ADR-021](../../adr/021-cryptsync-lifecycle-on-puddle.md)). Cryptsync becomes "a capability of puddle membership" with a content-only verb tree. The narrative below describes the pre-0.40.0 dual-mode codebase; sections will be reworked when the implementation lands. See [`swarm-puddle-architecture.svg`](../img/swarm-puddle-architecture.svg) for the post-0.40.0 hierarchy.

Cryptsync keeps `~/cryptsync/` in sync across every envoy where a user is enrolled, **ciphertext at rest** everywhere. Drop a plaintext file on the primary, cryptsync encrypts it to every peer's public key and fans out the ciphertext. Unlock with the user's cryptsync password on any envoy to decrypt.

Cryptsync is the encryption-first sibling of [Filesync](filesync.md):

| | Filesync | Cryptsync |
|---|---|---|
| At-rest state | Plaintext | **Ciphertext** everywhere |
| Key material | N/A | Per-envoy age identity, password-wrapped |
| Read access | Any user login | Requires `vigo swarm cryptsync unlock` (password) |
| Fan-out trigger | Directory scan every 30s | Filesystem watch + explicit CLI |
| Membership | Any user on the LAN with `~/filesync/` | `cryptsync: true` on the user's usercrate |

Pick filesync for ambient work files you want across machines. Pick cryptsync for anything you'd want to stay unreadable on a stolen envoy.

Cryptsync is one of four content subsystems that ride the [swarm](swarm.md) substrate. The server plays no role in content or keys — cryptsync reuses swarm's mTLS peer channel (port 1531) for ciphertext transfers and runs its own multicast group (`224.0.0.44:1533`) for announcements.

## How It Works

Cryptsync is a **pure peer-to-peer** feature. The Vigo server plays no role in content or keys. Envoys discover each other via UDP multicast and exchange ciphertext directly over mTLS on swarm's peer port.

1. **Enroll.** On each envoy that should hold the user's encrypted files, the user's usercrate applies `cryptsync: true`. The user executor drops `~/.cryptsync/enroll.pending`. The next time the user runs any `vigo swarm cryptsync *` verb, the CLI prompts for the cryptsync password and generates a fresh age identity on that envoy. The identity is wrapped with the password (Argon2id + XChaCha20-Poly1305) and stored at `~/.cryptsync/identity.age` with mode 0600. The public key is written to `~/.cryptsync/recipients.txt` and begins appearing in UDP announcements.
2. **Discover.** Every 30 seconds, each enrolled envoy multicasts a compact announcement on `224.0.0.44:1533` — `{machine, user, pubkey, emitted_at, port}`. Other envoys running cryptsync for the same user receive the announcement and add the peer to `/var/lib/vigo/swarm/cryptsync/<user>/peers.json`. `~/.cryptsync/recipients.txt` is regenerated from the peer table so future encrypts reach the new peer automatically. When the announcer's puddle pubkey matches the local user's, the peer is also recorded in `/var/lib/vigo/swarm/cryptsync/<user>/puddle_members.json` — the append-only "ever-known members" table that backs the long-term-absence gap line in `cryptsync status`. Unlike `peers.json` (which `prune_stale` evicts after `STALE_SECS=90s`), `puddle_members.json` shrinks only on authoritative removal (`apply_leave` paths, including admin evict and self-leave), so the membership denominator and per-peer silent-for duration keep working hours and days into a peer's absence.
3. **Encrypt.** Two equivalent paths: explicit (`vigo swarm cryptsync encrypt ~/Downloads/foo.pdf`) or **drop-and-forget** — copy a plaintext file into `~/cryptsync/` and the watcher auto-encrypts it in place. Either way the CLI/daemon reads `recipients.txt`, age-encrypts the plaintext to every listed public key, and writes the ciphertext atomically to `~/cryptsync/foo.pdf`. The output lands as **mode 0600 owned by the invoking user** even though the binary runs in a root context — `chmod 0600` + `chown` to the user (resolved via `SUDO_USER` or `getuid()`) happens before the atomic rename exposes the final name. No password prompt — encryption uses public keys only. See **Auto-encrypt-on-write** below for the drop-and-forget rules.
4. **Fan out.** The agent daemon watches `~/cryptsync/` via inotify (Linux) / kqueue (macOS). On a new or modified ciphertext file, the pusher POSTs the ciphertext to every live peer on `https://<peer>:1531/cryptsync/push/<user>/<filename>`, plus every host listed in the user's `cross_subnet_peers:` config. Pushes run in parallel with bounded concurrency (4 at a time).
5. **Receive.** On the peer side, the `/cryptsync/push/...` HTTPS handler writes the ciphertext atomically into the target user's `~/cryptsync/<filename>` as **mode 0600 owned by the destination user**. The handler runs in the agent daemon's root context but the same temp + `chmod 0600` + `chown` + rename sequence used by `encrypt` drops ownership to the destination user before exposure, so every envoy ends up with identical bytes on disk under identical perms.
6. **Decrypt.** On any enrolled envoy, `vigo swarm cryptsync unlock` prompts for the password, unwraps the age identity into a per-user session helper daemon, and holds the key in RAM until the session ends or the idle timer fires. `vigo swarm cryptsync decrypt <name> -o <path>` streams the ciphertext through the helper's DECRYPT socket and writes plaintext to a user-chosen path.
7. **Revoke.** `vigo swarm cryptsync forget <hostname>` removes the peer's pubkey from `recipients.txt` so future encrypts exclude it. `vigo swarm cryptsync reencrypt` re-encrypts every existing file in `~/cryptsync/` to the reduced recipient set, cutting off access to any ciphertext the revoked envoy will cache afterward.

## Commands

| Command | Behavior |
|---|---|
| `vigo swarm cryptsync init` | First-time setup on the primary envoy. Prompts password (confirm), generates wrapped identity, creates `~/cryptsync/`. |
| `vigo swarm cryptsync enroll` | Per-envoy setup. Normally triggered by the `enroll.pending` sentinel on first `vigo swarm cryptsync *` invocation; can be run manually. Idempotent. |
| `vigo swarm cryptsync unlock` | Prompt password, start the session helper daemon. |
| `vigo swarm cryptsync lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `vigo swarm cryptsync encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/cryptsync/`. Does not require `unlock`. |
| `vigo swarm cryptsync decrypt <file> -o <path>` | Decrypt from `~/cryptsync/` to a user-chosen path. Requires `unlock`. `-o` is mandatory — no default destination. |
| `vigo swarm cryptsync forget <hostname>` | Revoke an envoy from the recipient set. |
| `vigo swarm cryptsync leave [--purge] [--keep-identity] [--yes]` | Self-unenroll: drop a `leave.pending` sentinel; the daemon signs + fans out a LEAVE envelope, then scrubs local state per the flags. |
| `vigo swarm cryptsync purge --yes` | Power-user wipe of `~/.cryptsync/` + `~/cryptsync/` for the calling user. Distinct from `leave --purge` — does NOT gossip; peers age this envoy out via the staleness rule. |
| `vigo swarm cryptsync reencrypt` | Force re-encrypt of every file in `~/cryptsync/` to the current recipient set. Requires `unlock`. |
| `vigo swarm cryptsync status` | Show local state: enrolled, recipients, file count breakdown (ciphertext/plaintext/failed), session helper status. |

## Auto-Encrypt-on-Write

The watcher daemon auto-encrypts plaintext files that land in `~/cryptsync/`. Every drop produces ciphertext-at-rest under correct operation; the user does not need to remember to call `vigo swarm cryptsync encrypt` first. ADR-012 captures the design.

**When the session is unlocked and the file is at most 1 GiB:** the watcher stages the plaintext to `~/.cryptsync/staging/<uuid>`, encrypts to the current recipient set, and atomically renames the result back over the original path. The natural inotify event from the rename then fires the normal push fan-out. No persistent queue — the directory itself is the queue.

**When the session is locked:** the watcher emits a single actionable WARN naming the file and the recovery action (`vigo swarm cryptsync unlock`), leaves the plaintext in place, and adds nothing to disk. The next `vigo swarm cryptsync unlock` walks `~/cryptsync/` once and encrypts every file failing the age-magic check (the **unlock sweep**). Already-ciphertext files no-op naturally.

**When the file is over 1 GiB:** the watcher emits one WARN noting the size cap and leaves the plaintext alone. The 1 GiB ceiling matches `MAX_PUSH_BYTES` so a file that can't be transmitted never burns CPU on encryption.

**Failure handling** classifies any encrypt error into three buckets:

| Class | Examples | Behavior |
|---|---|---|
| Terminal-actionable | `recipients.txt` empty, disk full, permission denied, source vanished mid-read | One specific WARN, leave plaintext, no retry. The next watcher event after the user fixes the cause retries naturally. |
| Terminal-unrecoverable | age library refuses input, key corrupt | One ERROR, rename source to `<filename>.<unix_ts>.failed`, no retry. |
| Transient | I/O error, signal-interrupted | Retry 3× in-thread (100 ms / 500 ms / 2 s). On exhaustion, treat as unrecoverable and rename. |

Recovery from a `.failed` artifact: rename it back to its original name. The watcher sees a fresh event and the encrypt fires from clean state. The watcher's `resolve_event` filter ignores `<orig>.<unix_ts>.failed` artifacts (numeric timestamp segment required) so a permanent failure never loops back into the pipeline. User-named `*.failed` files (no timestamp segment) still sync as normal content.

**Liveness gating** uses `~/.cryptsync/session.unlocked` — a sentinel written by the session helper at startup with its PID, removed on shutdown. The watcher (root context) reads the file and verifies the PID is alive to decide "session unlocked." Stale sentinels (helper crashed without cleanup) read as locked, which matches the user-visible reality.

**Status output** breaks the file count into subcounts so the user can see at a glance what the watcher did:

```
files:            5
  ciphertext:     3
  plaintext:      1   (run 'vigo swarm cryptsync unlock' to encrypt and sync)
  failed:         1
                  vigo-lic-backup.tar.gz.20260429.failed
```

Counts derive from a single `read_dir` scan + a one-line magic peek per file at status-call time. No persistent state.

**Default-on, no flag.** Auto-encrypt-on-write is the default behavior. There is no `auto_encrypt:` toggle in `~/.cryptsync/config.yaml` or `server.yaml` — a flag would re-create the original UX trap (operator forgets to flip it; user sees broken behavior).

**Plaintext never crosses the wire.** The watcher's age-magic gate is the transmission boundary. Auto-encrypt produces ciphertext locally before any push fires; the pusher reads only ciphertext-magic-verified bytes.

## Usercrate Integration

```yaml
- type: user
  name: dan
  home: /home/dan
  shell: /bin/bash
  cryptsync: true        # enroll this user in cryptsync on this envoy
```

`cryptsync: true` tells the user executor to drop `~/.cryptsync/enroll.pending` after user creation. On first login the user runs any `vigo swarm cryptsync *` verb and completes enrollment interactively (one password prompt). The field is idempotent — flipping it true on an already-enrolled user is a no-op.

The cryptsync password is **not** the login password. Phase 1 has no PAM integration. In **standalone mode** the user sets a separate cryptsync password at enrollment and types it again at each `unlock`. In **puddle mode** (ADR-016 slice 4) the wrap key is derived from the user's puddle identity instead — the cryptsync password ceremony is collapsed into the puddle one and `unlock` requires no extra prompt.

## Key Wrapping

- **Identity**: fresh age X25519 keypair generated locally on each envoy, never transmitted or regenerated.
- **KDF**: Argon2id (memory=64 MiB, iterations=3, parallelism=1, salt=16 random bytes).
- **AEAD**: XChaCha20-Poly1305 with a 24-byte random nonce per wrap.
- **Blob format** at `~/.cryptsync/identity.age`: `[1 byte version = 0x01][24 byte nonce][ciphertext + 16 byte auth tag]`. Mode 0600, user-owned.

### Wrap modes (`wrap_mode` in `~/.cryptsync/config.yaml`)

- **`standalone`** — wrap-key input is the per-envoy passphrase you typed at `cryptsync standalone init / enroll`. `unlock` prompts for it. Independent of puddle.
- **`puddle`** — wrap-key input is the 64-byte Ed25519 signature returned by `puddle::session::request_sign(b"vigo-cryptsync-wrap-v1")` (deterministic per RFC 8032), passed through Argon2id at the same parameters as standalone. `unlock` calls the running puddle session helper instead of prompting. If the helper isn't running, `unlock` fails clean with `run vigo swarm puddle unlock first` rather than silently falling back to a prompt.

`puddle init` writes `wrap_mode: puddle` and `standalone init` / `standalone enroll` write `wrap_mode: standalone` into the seeded `config.yaml` template. Pre-0.36.x configs without the field load as `standalone` for back-compat. Both modes share the same on-disk blob format and the same Argon2id stretch — switching modes requires re-enrolling (`leave` then `init` again under the desired mode), since the wrap key changes.

The agent daemon (running as root) never holds the unwrapped identity. Only the per-user session helper does, only while unlocked, only in RAM.

## Auto-bootstrap from puddle

`vigo swarm puddle unlock` checks `~/.cryptsync/identity.age` after spawning its own session helper. If absent, it auto-runs `cryptsync puddle init` inline — the just-spawned puddle helper provides the wrap key, no second passphrase. Failure is non-fatal; the operator can re-run `cryptsync puddle init` manually. `puddle join` only persists the wrapped puddle blob; the auto-bootstrap fires on the next `unlock` (when the session helper actually starts).

## Session Helper

`vigo swarm cryptsync unlock` spawns a detached per-user daemon process (`setsid` away from the login shell) that holds the unwrapped age identity in memory and serves DECRYPT/STATUS/LOCK requests over a Unix socket:

- **Socket**: `$XDG_RUNTIME_DIR/vigo/cryptsync.sock` (Linux) or `$TMPDIR/cryptsync-<UID>.sock` (macOS), mode 0600.
- **Authentication**: `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS). Only connections from the same UID are accepted.
- **Parent-PID watchdog**: polls the login shell's PID every 10s and exits when it's gone.
- **Idle timeout**: default 60 minutes, configurable via `idle_timeout_minutes` in `~/.cryptsync/config.yaml`. Resets on every DECRYPT/STATUS.
- **Log**: `$XDG_RUNTIME_DIR/vigo/cryptsync.log`.

Explicit `vigo swarm cryptsync lock` sends a LOCK command. The helper zeroizes the key in memory and exits. Socket and PID file are unlinked.

## Cross-Subnet Peers

Multicast doesn't cross subnets. For envoys on different networks, list them explicitly in `~/.cryptsync/config.yaml`:

```yaml
cross_subnet_peers:
  - envoy-london.internal
  - envoy-tokyo.internal
idle_timeout_minutes: 60
```

The agent announces to these hosts via `POST /cryptsync/announce` on port 1531 and fans out ciphertext via `POST /cryptsync/push/...` the same way as LAN peers. Ports 1531 and 1533 must be reachable; the same fleet CA must be trusted on both sides.

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable) |
| Content supported | Regular files in `~/cryptsync/` only (no subdirectories, no symlinks) |
| Filenames | Plaintext on the wire and on disk — only content is encrypted |
| Platforms | Linux + macOS (Phase 1) |
| Password recovery | None — lose the password on every envoy and the data is unrecoverable |
| Keys per envoy | One age identity per user per envoy |

**Disk-pressure guardrail.** Before every `POST /cryptsync/push/{user}/{filename}`, the sender probes the recipient at `GET /health/storage?for=cryptsync&user=<user>` (60 s cache per peer+user) and skips the push if the recipient reports `free_pct <= 10`. The receiver's push handler re-checks `~<user>/cryptsync` on entry and returns **507 Insufficient Storage** before reading a byte of the body, closing the race between probe and arrival. Skipped pushes are no-ops — no retry backoff, no failure counter; the next watcher event re-probes and sends when the peer recovers. See [Disk Space in swarm operations](../../operations/swarm.md#disk-space) for the fleet-wide policy.

## Deletion propagation (tombstones)

Deletion is a first-class mesh operation, not a local-only `rm`. When the operator removes a file from `~/cryptsync/` on any envoy — via `vigo swarm cryptsync delete <name>` or plain `rm` — the watcher fires a Delete event, the pusher signs a DELETE envelope and fans it out to every peer, and each peer records a **tombstone** and removes its local copy. Tombstones outlive the delete by `tombstone_retention_days` (default **90 days**) so that an envoy that was offline at delete time can't resurrect the file when it reconnects with a stale copy.

**Flow:**

1. `vigo swarm cryptsync delete foo.pdf` (or `rm ~/cryptsync/foo.pdf`) on envoy A.
2. Watcher catches the Remove event, emits a `DeleteJob { user, filename, deletion_time }`.
3. Pusher loads A's Ed25519 signing key (`/var/lib/vigo/swarm/cryptsync/<user>/signing.key`, root-owned), builds a signed `DeleteEnvelope`, and POSTs it to every live LAN peer at `https://<peer>:1531/cryptsync/tombstones/<user>/<filename>` plus every `cross_subnet_peers:` host.
4. Receiver validates the envelope against the sender's known signing pubkey from its local `peers.json`. Unknown sender or mismatched signature → 403 Forbidden.
5. On valid envelope: receiver records a tombstone in `/var/lib/vigo/swarm/cryptsync/<user>/tombstones.json` (root-owned canonical) and mirrors the table to `~<user>/.cryptsync/tombstones.json` (user-owned 0600) so `vigo swarm cryptsync list` / `status` running as the unprivileged user can see it. Removes the local ciphertext file if present, returns 200 OK. The same daemon-side-canonical + user-side-mirror pattern applies to `leaves.json`.
6. Any future PUSH of the same filename from any peer → 410 Gone (anti-resurrection) until either a `cryptsync resurrect` envelope supersedes the tombstone or the tombstone ages past retention.
7. Any future PULL of the tombstoned filename → 410 Gone. The puller records the tombstone locally on receiving this response — lazy catch-up for the rare code path that reaches pull.

**Catch-up after partition:** every announcement carries a 16-byte `tombstones_digest` (truncated SHA-256 over sorted `filename|deletion_time` entries) **and** a 16-byte `leaves_digest` (the analogous digest over `(age_pubkey, leave_time)` pairs from `LeavesTable`). A peer that observes a digest mismatch on either pulls the full table via `GET /cryptsync/tombstones/<user>` or `GET /cryptsync/leaves/<user>` and merges it. This is the common-case recovery path for "envoy was offline during a delete or LEAVE, comes back up with stale state." For tombstone catch-up the receiver also drops any local files newly covered by tombstones; for leaves catch-up it runs `apply_leave` on each newly-merged entry so the peer table prune + recipients regen + reencrypt sentinel fire just as they would have on the original LEAVE envelope. No peer-initiated work is required to converge.

### Signing keys

Each envoy generates an Ed25519 keypair *the first time the cryptsync daemon observes enrollment for that user*. The raw private key lives at `/var/lib/vigo/swarm/cryptsync/<user>/signing.key` (root-owned, 0600); the matching pubkey is at `signing.pub` (0644 text) and advertised on every announcement so peers can record it in their `peers.json` for signature verification.

The signing key is **daemon-owned, not password-wrapped**. Unlike the age identity (which gates content decryption — losing it is catastrophic and permanent), the signing key only authorizes destructive operations on behalf of this envoy. Recovery from a compromised signing key is fast: peers run `vigo swarm cryptsync forget <stolen-host>` on every other envoy, which removes the compromised pubkey from their `peers.json`, and subsequent DELETE envelopes from the compromised host fail validation. This tradeoff is deliberate — wrapping the signing key would force the user to be unlocked for deletions to propagate, which breaks the pusher's reactive model.

### Resurrecting a deleted filename

While a tombstone is active, re-pushing that filename is rejected with 410 Gone on every receiver. The tombstone is a strict block, not an mtime-compared guard — cryptsync's wire format doesn't currently carry a push-time mtime, so the receiver has no way to distinguish "legitimate re-publish" from "stale peer resurrecting old bytes."

The encrypt-time preflight (`cmd_encrypt`, `auto_encrypt`) refuses **before** doing any encryption work when the target name is effectively tombstoned. Without this, `cryptsync encrypt` would silently produce ciphertext that every peer rejects with 410 — wasted CPU, destroyed plaintext, no operator-visible signal. The preflight fails with a message naming the override path:

```
filename `foo.pdf` is tombstoned (87d remaining in retention).
Run `vigo swarm cryptsync resurrect <file> [--as <name>]` to override and re-encrypt.
```

The override paths, in order of preference:

- **`vigo swarm cryptsync resurrect <file> [--as <name>]`** — operator override. Drops a `~/.cryptsync/resurrect.pending` sentinel; the daemon picks it up on its dedicated 1s tick, signs a `ResurrectEnvelope` with the local Ed25519 signing key, persists a `ResurrectRecord` in the per-user resurrects store, and fans the envelope out to every live peer. The CLI awaits the daemon's sentinel removal (60s ceiling) before invoking the encrypt path, so by the time the watcher fires the PushJob, every reachable peer holds the resurrect record and the `effective_tombstone` gate returns None — the push lands instead of 410'ing. Offline peers catch up via `resurrects_digest` mismatch on their next announce-receive.
- **Rename** the new file (`vigo swarm cryptsync encrypt foo-v2.pdf`) — different filename, no tombstone, no gate. Trivially works.
- **Wait out the tombstone.** Default retention is 90 days; after that, GC drops the tombstone and the resurrect record together (both age out at the same `tombstone_retention_days` window) and the original filename is freely usable again.

Mechanism: the gate is `tombstones::effective_tombstone(name, &tombstones, &resurrects)` — the tombstone for `name` is "effective" iff `tombstone.deletion_time > resurrect.resurrect_time` (or no resurrect exists). Tie-breaker: equal times go to the resurrect, so an operator-driven override always wins simultaneity. Subsequent re-DELETE on the same name simply lands a new tombstone with `deletion_time > resurrect_time` and the gate flips back. Per-filename last-write-wins.

Catch-up: announcements carry a third 16-byte digest, `resurrects_digest`, alongside `tombstones_digest` and `leaves_digest`. Receivers with a digest mismatch pull the full resurrects table via `GET /swarm/cryptsync/resurrects/<user>` and merge — same shape as tombstone catch-up. The `RESURRECT` POST handler, the resurrects table type, and the GC tick all mirror their tombstone equivalents.

## Leaving cryptsync

Two paths exist for removing a host from a user's cryptsync circle — both land on the same local-apply function (`apply_leave` in `agent/src/cryptsync/mod.rs`) but differ in how the agent learns about the leave.

### Path A — self-leave (user-triggered, P2P)

User runs `vigo swarm cryptsync leave` on the departing envoy. The CLI drops a `~/.cryptsync/leave.pending` sentinel; the daemon reactor picks it up on its next announce tick (~30 s), builds a **signed LEAVE envelope** (`agent/src/cryptsync/leaves.rs`), fans it out to every live peer via `POST /cryptsync/leaves/<user>`, persists the record to `/var/lib/vigo/swarm/cryptsync/<user>/leaves.json`, and scrubs local state per the sentinel's flags.

### Signed LEAVE envelope

The envelope is a direct analogue of the DELETE tombstone envelope:

```rust
struct LeaveEnvelope {
    user: String,
    age_pubkey_leaving: String,        // the subject's age pubkey — the authoritative key
    hostname_leaving: String,          // for audit/routing
    leave_time: i64,
    signer_sign_pubkey: String,        // audit; not used for verification
    signature: String,                 // ed25519 over canonical(user|age_pubkey|leave_time)
}
```

**Self-leave enforcement**: on receipt, the agent verifies the signature against the Ed25519 signing pubkey registered for `hostname_leaving` in its local `peers.json`. Signer must be the subject. A peer cannot sign a LEAVE for another peer — cross-member eviction via envelope is cryptographically impossible.

Records are keyed by `age_pubkey_leaving`, not hostname: a host that re-enrolls generates a fresh age identity, so a replayed old LEAVE targets only the old pubkey and can't affect the reborn host.

Retention: 90 days (matches tombstones). A digest of the sorted `(age_pubkey, leave_time)` pairs piggybacks on future announcement payloads so offline-during-leave peers can catch up on reconnect via `GET /cryptsync/leaves/<user>` — scheduled for Phase B.1.

### Path B — admin eviction (server-triggered, control plane)

Admin runs `vigocli swarm cryptsync evict --host <h> {--user <u> | --all-users}`. The CLI queries `/api/v1/swarm/cryptsync/fleet` to find affected users, then dispatches a shell command via `POST /api/v1/task`:

```
vigo swarm cryptsync _apply-evict --user <u> --hostname <h>
```

Each enrolled agent receives the task, resolves the target's age pubkey from its own peer table, calls `apply_leave` directly (no envelope, no fan-out — the agent is authoritative for its own state). Offline agents pick up the task on next check-in via normal task-dispatch retry.

No signed envelope; authority is end-to-end mTLS + admin token at the control plane, not a P2P signature. This is the right shape for "gone host, can't self-leave" scenarios where no-one can produce the subject's signature.

**Self-evict scrub:** the dispatch goes to `target: "*"` so the evicted host receives `_apply-evict` for itself. Peer tables never include self, so the standard pubkey-lookup path would short-circuit as a no-op and leave the host's identity intact — blocking subsequent `vigo swarm cryptsync enroll` (which bails when `~/.cryptsync/identity.age` already exists). The agent detects the self case (local `/etc/hostname` matches the `--hostname` argument) and instead scrubs `~/.cryptsync/identity.age`, `identity.salt`, `recipients.txt`, and `config.yaml` — same set as a `!keep_identity` self-leave. `~/cryptsync/` ciphertext is preserved (operator can `rm -rf` for full wipe). Re-enroll then proceeds cleanly.

### Local scrub on self-leave

`vigo swarm cryptsync leave` defaults to **delete identity, keep ciphertext**. `~/.cryptsync/` is removed (wrapped identity, salt, recipients, config); `~/cryptsync/` ciphertext is left in place but orphaned — the host no longer has an identity to decrypt it locally. Flags:

- `--purge` — also delete `~/cryptsync/` ciphertext.
- `--keep-identity` — keep `~/.cryptsync/` (rare; used when planning to re-enroll with the same keys).

### Reencrypt queueing on peers

When a LEAVE lands on a peer, the agent drops `~<user>/.cryptsync/reencrypt.pending`. Existing ciphertext in `~/cryptsync/` was wrapped for N recipients including the evicted one; it remains decryptable by the evicted host unless re-wrapped. The session helper drains the sentinel on its next tick (if the user is unlocked) or on the next `vigo swarm cryptsync unlock` invocation — reencryption is eventually consistent and user-interactive by design.

### State persistence — per-user serialization

`TombstoneTable`, `LeavesTable`, and `PeerTable` all follow a load → mutate → save pattern. Two concurrent paths against the same user (e.g., the watcher fanning out N DeleteJobs from a single `vigo swarm cryptsync leave --purge`, or simultaneous DELETE envelopes from different peers) would race their load-modify-save pairs — both load identical state, each upserts a different entry, the second save() overwrites the first, and the loser's update is silently lost. The atomic temp+rename guards readers against partial writes but doesn't serialize concurrent writers.

`agent/src/cryptsync/locks.rs` exposes a `user_lock(table, user)` helper backed by three per-user lock maps (one per table type). Each table type has a `modify(user, |&mut Self| -> bool)` constructor that holds the per-user lock for the entire load+mutate+save+mirror sequence. Granularity is per-user so user A's tombstone activity doesn't block user B's, and per-table-type so a tombstone modify on user dan doesn't block a peer-table modify on user dan. Save still calls `write_user_mirror_json` inside the lock so canonical and user-side mirror stay in lockstep. Holding the lock across local file I/O is intentional — saves are rare and brief (milliseconds on local disk), and a copy-then-CAS shape would add code without a real win.

## Operability

Cryptsync's kill switches are defined fleet-wide in `swarm.cryptsync:` (server.yaml). The two knobs:

- **`enabled: false`** halts the cryptsync announcer, listener, and push handler on every envoy (gates wired through `Substrate::Cryptsync` in `agent/src/swarm/gates.rs`). Already-wrapped identity material under `~user/.cryptsync/` and ciphertext under `~user/cryptsync/` are preserved — re-enabling resumes operation without re-enrollment.
- **`blacklist: [pat, …]`** is destructive. Adding a host pattern dispatches `vigo swarm cryptsync _apply-blacklist --hostname <h>` to the entire fleet: the blacklisted host scrubs its `~user/.cryptsync/{identity.age,identity.salt,recipients.txt,config.yaml}` for every locally-enrolled user, and every other envoy drops the blacklisted host's pubkey from each user's peer table + recipient list. The destructive semantic mirrors a forced LEAVE — peers cannot fan out a signed envelope on the gone host's behalf, so admin authority via the mTLS control plane substitutes.

Both changes record tamper-evident audit-chain entries (`swarm.cryptsync.enabled`, `swarm.cryptsync.blacklist`) under `actor=config.reload` at publish time. See [swarm Operability](swarm.md#operability--kill-switches) for the full kill-switch table.

## Threat Model

**Cryptsync protects against:**
- Root compromise on any envoy *without* the user's cryptsync password. The attacker sees the wrapped `identity.age` + ciphertext only.
- Physical theft of an envoy with no active unlock session.
- Network eavesdropping (mTLS transport + age content encryption — defense in depth).
- Revoked envoys: files encrypted after `forget` cannot be decrypted by the revoked host.

**Cryptsync does not protect against:**
- Root compromise on an envoy with an active unlocked session — root can read the session helper's memory.
- Keyloggers or compromised shells capturing the password.
- Weak passwords (Argon2id slows brute force but doesn't prevent it).
- Filename, size, or mtime leakage — these are not encrypted (deliberate, matches filesync's boundary).

Lose the password on every envoy and the data is unrecoverable. Phase 1 has no key escrow.

## Observability (`/cryptsync` UI)

Every enrolled envoy ships a `cryptsync` trait on each check-in carrying per-user state: file count, `recent_files: [last 20 by mtime]`, ciphertext SHA-256 digest, tombstone count + recent deletions, known peers (hostname + pubkey + sign_pubkey), and the current session-helper-unlocked signal. The server-side aggregator (`server/cryptsyncmesh/`) joins these across the fleet to power the `/cryptsync` page.

The `/filesync`, `/cryptsync`, `/gitmirror` pages and every mesh endpoint (`/api/v1/swarm/filesync/*`, `/api/v1/swarm/cryptsync/*`, `/api/v1/swarm/gitmirror/fleet`, and the UI fragment drill-downs) are admin-only — fleet-wide per-user file lists aren't appropriate for non-admin roles. Viewer and compliance-role users get a 403 on direct access and don't see the nav links. The legacy `/mesh` URL 301-redirects to `/filesync` (or `/cryptsync` with `?tab=crypt`) for one release while muscle memory migrates.

Operator-facing signals surfaced on that page:

- **Presence matrix** — per (user × envoy × filename) cells showing live / tombstoned / missing, with divergence flagged when shas disagree
- **Recipient drift** — two envoys in the same user's mesh with different peer sets. Common cause: one host missed a recent enrollment and files encrypted there won't decrypt on the unknown peer
- **Active sessions** — which envoys currently hold an unlocked identity in RAM
- **Three-state envoy reporting** — reporting (trait fresh) / stale (trait older than 5 min) / not_reporting (no trait — rolling upgrade or dead envoy), visually distinct so you don't confuse "not upgraded yet" with "offline"

The drill-down endpoint `GET /api/v1/swarm/cryptsync/envoy/{id}/users/{user}/files` returns the full file list (not the bounded 20) for one envoy cell when an operator clicks through.

Ciphertext SHA-256 is cached per-(filename, mtime, size) at `/var/lib/vigo/swarm/cryptsync/<user>/sha_cache.json` so multi-GiB files aren't re-hashed every trait cycle.

## Relationship to Other Subsystems

- **Filesync** is the plaintext sibling. Cryptsync does not replace it; pick whichever matches the threat model.
- **Swarm** handles fleet-wide blob distribution (Vigo-managed content). Cryptsync rides on the same mTLS peer server (port 1531) but uses its own routes, its own multicast group (224.0.0.44:1533), and its own peer table — no shared state.
- **Gitmirror** is the git-repo sibling for personal DR. Similar opt-in pattern; different subsystem entirely.
