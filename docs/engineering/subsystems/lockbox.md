
# Lockbox — Per-User Encrypted P2P Directory

> **Status (0.40.0):** Lockbox is puddle-only ([ADR-020](../../adr/020-lockbox-puddle-only.md)) and its lifecycle layer has absorbed into puddle ([ADR-021](../../adr/021-lockbox-lifecycle-on-puddle.md)). The lockbox verb tree is content-only — `encrypt / decrypt / push / list / status / reencrypt / resurrect / purge` plus `puddle init / renew-cert` for recovery. Lifecycle verbs (`init / leave / forget`) retired in favor of `vigo swarm puddle leave` and `vigocli swarm puddle evict`. See [`swarm-puddle-architecture.svg`](../img/swarm-puddle-architecture.svg) for the hierarchy.

Lockbox keeps `~/lockbox/` in sync across every envoy where a user is enrolled, **ciphertext at rest** everywhere. Drop a plaintext file on the primary, lockbox encrypts it to every peer's public key and fans out the ciphertext. Unlock with the user's lockbox password on any envoy to decrypt.

Lockbox is the encryption-first sibling of [Longdrawer](longdrawer.md):

| | Longdrawer | Lockbox |
|---|---|---|
| At-rest state | Plaintext | **Ciphertext** everywhere |
| Key material | N/A | Per-envoy age identity, password-wrapped |
| Read access | Any user login | Requires `vigo swarm lockbox unlock` (password) |
| Fan-out trigger | Directory scan every 30s | Filesystem watch + explicit CLI |
| Membership | Any user on the LAN with `~/longdrawer/` | `lockbox: true` on the user's usercrate |

Pick longdrawer for ambient work files you want across machines. Pick lockbox for anything you'd want to stay unreadable on a stolen envoy.

Lockbox is one of four content subsystems that ride the [swarm](swarm.md) substrate. The server plays no role in content or keys — lockbox reuses swarm's mTLS peer channel (port 1531) for ciphertext transfers and runs its own multicast group (`224.0.0.44:1533`) for announcements.

## How It Works

Lockbox is a **pure peer-to-peer** feature. The Vigo server plays no role in content or keys. Envoys discover each other via UDP multicast and exchange ciphertext directly over mTLS on swarm's peer port.

1. **Enroll.** On each envoy that should hold the user's encrypted files, the user's usercrate applies `lockbox: true`. The user executor drops `~/.lockbox/enroll.pending`. The next time the user runs any `vigo swarm lockbox *` verb, the CLI prompts for the lockbox password and generates a fresh age identity on that envoy. The identity is wrapped with the password (Argon2id + XChaCha20-Poly1305) and stored at `~/.lockbox/identity.age` with mode 0600. The public key is written to `~/.lockbox/recipients.txt` and begins appearing in UDP announcements.
2. **Discover.** Every 30 seconds, each enrolled envoy multicasts a compact announcement on `224.0.0.44:1533` — `{machine, user, pubkey, emitted_at, port}`. Other envoys running lockbox for the same user receive the announcement and add the peer to `/var/lib/vigo/swarm/lockbox/<user>/peers.json`. `~/.lockbox/recipients.txt` is regenerated from the peer table so future encrypts reach the new peer automatically. When the announcer's puddle pubkey matches the local user's, the peer is also recorded in `/var/lib/vigo/swarm/lockbox/<user>/puddle_members.json` — the append-only "ever-known members" table that backs the long-term-absence gap line in `lockbox status`. Unlike `peers.json` (which `prune_stale` evicts after `STALE_SECS=90s`), `puddle_members.json` shrinks only on authoritative removal (`apply_leave` paths, including admin evict and self-leave), so the membership denominator and per-peer silent-for duration keep working hours and days into a peer's absence.
3. **Encrypt.** Two equivalent paths: explicit (`vigo swarm lockbox encrypt ~/Downloads/foo.pdf`) or **drop-and-forget** — copy a plaintext file into `~/lockbox/` and the watcher auto-encrypts it in place. Either way the CLI/daemon reads `recipients.txt`, age-encrypts the plaintext to every listed public key, and writes the ciphertext atomically to `~/lockbox/foo.pdf`. The output lands as **mode 0600 owned by the invoking user** even though the binary runs in a root context — `chmod 0600` + `chown` to the user (resolved via `SUDO_USER` or `getuid()`) happens before the atomic rename exposes the final name. No password prompt — encryption uses public keys only. See **Auto-encrypt-on-write** below for the drop-and-forget rules.
4. **Fan out.** The agent daemon watches `~/lockbox/` via inotify (Linux) / kqueue (macOS). On a new or modified ciphertext file, the pusher POSTs the ciphertext to every live peer on `https://<peer>:1531/lockbox/push/<user>/<filename>`, plus every host listed in the user's `cross_subnet_peers:` config. Pushes run in parallel with bounded concurrency (4 at a time).
5. **Receive.** On the peer side, the `/lockbox/push/...` HTTPS handler writes the ciphertext atomically into the target user's `~/lockbox/<filename>` as **mode 0600 owned by the destination user**. The handler runs in the agent daemon's root context but the same temp + `chmod 0600` + `chown` + rename sequence used by `encrypt` drops ownership to the destination user before exposure, so every envoy ends up with identical bytes on disk under identical perms.
6. **Decrypt.** On any enrolled envoy, `vigo swarm lockbox unlock` prompts for the password, unwraps the age identity into a per-user session helper daemon, and holds the key in RAM until the session ends or the idle timer fires. `vigo swarm lockbox decrypt <name> -o <path>` streams the ciphertext through the helper's DECRYPT socket and writes plaintext to a user-chosen path.
7. **Revoke.** `vigo swarm lockbox forget <hostname>` removes the peer's pubkey from `recipients.txt` so future encrypts exclude it. `vigo swarm lockbox reencrypt` re-encrypts every existing file in `~/lockbox/` to the reduced recipient set, cutting off access to any ciphertext the revoked envoy will cache afterward.

## Commands

| Command | Behavior |
|---|---|
| `vigo swarm lockbox init` | First-time setup on the primary envoy. Prompts password (confirm), generates wrapped identity, creates `~/lockbox/`. |
| `vigo swarm lockbox enroll` | Per-envoy setup. Normally triggered by the `enroll.pending` sentinel on first `vigo swarm lockbox *` invocation; can be run manually. Idempotent. |
| `vigo swarm lockbox unlock` | Prompt password, start the session helper daemon. |
| `vigo swarm lockbox lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `vigo swarm lockbox encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/lockbox/`. Does not require `unlock`. |
| `vigo swarm lockbox decrypt <file> -o <path>` | Decrypt from `~/lockbox/` to a user-chosen path. Requires `unlock`. `-o` is mandatory — no default destination. |
| `vigo swarm lockbox forget <hostname>` | Revoke an envoy from the recipient set. |
| `vigo swarm lockbox leave [--purge] [--keep-identity] [--yes]` | Self-unenroll: drop a `leave.pending` sentinel; the daemon signs + fans out a LEAVE envelope, then scrubs local state per the flags. |
| `vigo swarm lockbox purge --yes` | Power-user wipe of `~/.lockbox/` + `~/lockbox/` for the calling user. Distinct from `leave --purge` — does NOT gossip; peers age this envoy out via the staleness rule. |
| `vigo swarm lockbox reencrypt` | Force re-encrypt of every file in `~/lockbox/` to the current recipient set. Requires `unlock`. |
| `vigo swarm lockbox status` | Show local state: enrolled, recipients, file count breakdown (ciphertext/plaintext/failed), session helper status. |

## Auto-Encrypt-on-Write

The watcher daemon auto-encrypts plaintext files that land in `~/lockbox/`. Every drop produces ciphertext-at-rest under correct operation; the user does not need to remember to call `vigo swarm lockbox encrypt` first. ADR-012 captures the design.

**When the session is unlocked and the file is at most 1 GiB:** the watcher stages the plaintext to `~/.lockbox/staging/<uuid>`, encrypts to the current recipient set, and atomically renames the result back over the original path. The natural inotify event from the rename then fires the normal push fan-out. No persistent queue — the directory itself is the queue.

**When the session is locked:** the watcher emits a single actionable WARN naming the file and the recovery action (`vigo swarm lockbox unlock`), leaves the plaintext in place, and adds nothing to disk. The next `vigo swarm lockbox unlock` walks `~/lockbox/` once and encrypts every file failing the age-magic check (the **unlock sweep**). Already-ciphertext files no-op naturally.

**When the file is over 1 GiB:** the watcher emits one WARN noting the size cap and leaves the plaintext alone. The 1 GiB ceiling matches `MAX_PUSH_BYTES` so a file that can't be transmitted never burns CPU on encryption.

**Failure handling** classifies any encrypt error into three buckets:

| Class | Examples | Behavior |
|---|---|---|
| Terminal-actionable | `recipients.txt` empty, disk full, permission denied, source vanished mid-read | One specific WARN, leave plaintext, no retry. The next watcher event after the user fixes the cause retries naturally. |
| Terminal-unrecoverable | age library refuses input, key corrupt | One ERROR, rename source to `<filename>.<unix_ts>.failed`, no retry. |
| Transient | I/O error, signal-interrupted | Retry 3× in-thread (100 ms / 500 ms / 2 s). On exhaustion, treat as unrecoverable and rename. |

Recovery from a `.failed` artifact: rename it back to its original name. The watcher sees a fresh event and the encrypt fires from clean state. The watcher's `resolve_event` filter ignores `<orig>.<unix_ts>.failed` artifacts (numeric timestamp segment required) so a permanent failure never loops back into the pipeline. User-named `*.failed` files (no timestamp segment) still sync as normal content.

**Liveness gating** uses `~/.lockbox/session.unlocked` — a sentinel written by the session helper at startup with its PID, removed on shutdown. The watcher (root context) reads the file and verifies the PID is alive to decide "session unlocked." Stale sentinels (helper crashed without cleanup) read as locked, which matches the user-visible reality.

**Status output** breaks the file count into subcounts so the user can see at a glance what the watcher did:

```
files:            5
  ciphertext:     3
  plaintext:      1   (run 'vigo swarm lockbox unlock' to encrypt and sync)
  failed:         1
                  vigo-lic-backup.tar.gz.20260429.failed
```

Counts derive from a single `read_dir` scan + a one-line magic peek per file at status-call time. No persistent state.

**Default-on, no flag.** Auto-encrypt-on-write is the default behavior. There is no `auto_encrypt:` toggle in `~/.lockbox/config.yaml` or `server.yaml` — a flag would re-create the original UX trap (operator forgets to flip it; user sees broken behavior).

**Plaintext never crosses the wire.** The watcher's age-magic gate is the transmission boundary. Auto-encrypt produces ciphertext locally before any push fires; the pusher reads only ciphertext-magic-verified bytes.

## Usercrate Integration

```yaml
- type: user
  name: dan
  home: /home/dan
  shell: /bin/bash
  lockbox: true        # enroll this user in lockbox on this envoy
```

`lockbox: true` tells the user executor to drop `~/.lockbox/enroll.pending` after user creation. On first login the user runs any `vigo swarm lockbox *` verb and completes enrollment interactively (one password prompt). The field is idempotent — flipping it true on an already-enrolled user is a no-op.

The lockbox password ceremony is collapsed into the puddle one (per [ADR-020](../../adr/020-lockbox-puddle-only.md), 0.39.13): the wrap key is derived from the user's puddle identity, so `unlock` requires no extra prompt — the puddle session helper must be running.

## Key Wrapping

- **Identity**: fresh age X25519 keypair generated locally on each envoy, never transmitted or regenerated.
- **KDF**: Argon2id (memory=64 MiB, iterations=3, parallelism=1, salt=16 random bytes).
- **AEAD**: XChaCha20-Poly1305 with a 24-byte random nonce per wrap.
- **Blob format** at `~/.lockbox/identity.age`: `[1 byte version = 0x01][24 byte nonce][ciphertext + 16 byte auth tag]`. Mode 0600, user-owned.
- **Wrap-key input**: the 64-byte Ed25519 signature returned by `puddle::session::request_sign(b"vigo-lockbox-wrap-v1")` (deterministic per RFC 8032), passed through Argon2id. `unlock` calls the running puddle session helper to source it. If the helper isn't running, `unlock` fails clean with `run vigo swarm puddle unlock first` rather than silently falling back to anything else.

The agent daemon (running as root) never holds the unwrapped identity. Only the per-user session helper does, only while unlocked, only in RAM.

## Auto-bootstrap from puddle

`vigo swarm puddle unlock` checks `~/.lockbox/identity.age` after spawning its own session helper. If absent, it auto-runs `lockbox puddle init` inline — the just-spawned puddle helper provides the wrap key, no second passphrase. Failure is non-fatal; the operator can re-run `lockbox puddle init` manually. `puddle join` only persists the wrapped puddle blob; the auto-bootstrap fires on the next `unlock` (when the session helper actually starts).

## Session Helper

`vigo swarm lockbox unlock` spawns a detached per-user daemon process (`setsid` away from the login shell) that holds the unwrapped age identity in memory and serves DECRYPT/STATUS/LOCK requests over a Unix socket:

- **Socket**: `$XDG_RUNTIME_DIR/vigo/lockbox.sock` (Linux) or `$TMPDIR/lockbox-<UID>.sock` (macOS), mode 0600.
- **Authentication**: `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS). Only connections from the same UID are accepted.
- **Parent-PID watchdog**: polls the login shell's PID every 10s and exits when it's gone.
- **Idle timeout**: default 60 minutes, configurable via `idle_timeout_minutes` in `~/.lockbox/config.yaml`. Resets on every DECRYPT/STATUS.
- **Log**: `$XDG_RUNTIME_DIR/vigo/lockbox.log`.

Explicit `vigo swarm lockbox lock` sends a LOCK command. The helper zeroizes the key in memory and exits. Socket and PID file are unlinked.

## Cross-Subnet Peers

Multicast doesn't cross subnets. For envoys on different networks, list them explicitly in `~/.lockbox/config.yaml`:

```yaml
cross_subnet_peers:
  - envoy-london.internal
  - envoy-tokyo.internal
idle_timeout_minutes: 60
```

The agent announces to these hosts via `POST /lockbox/announce` on port 1531 and fans out ciphertext via `POST /lockbox/push/...` the same way as LAN peers. Ports 1531 and 1533 must be reachable; the same fleet CA must be trusted on both sides.

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable) |
| Content supported | Regular files in `~/lockbox/` only (no subdirectories, no symlinks) |
| Filenames | Plaintext on the wire and on disk — only content is encrypted |
| Platforms | Linux + macOS (Phase 1) |
| Password recovery | None — lose the password on every envoy and the data is unrecoverable |
| Keys per envoy | One age identity per user per envoy |

**Disk-pressure guardrail.** Before every `POST /lockbox/push/{user}/{filename}`, the sender probes the recipient at `GET /health/storage?for=lockbox&user=<user>` (60 s cache per peer+user) and skips the push if the recipient reports `free_pct <= 10`. The receiver's push handler re-checks `~<user>/lockbox` on entry and returns **507 Insufficient Storage** before reading a byte of the body, closing the race between probe and arrival. Skipped pushes are no-ops — no retry backoff, no failure counter; the next watcher event re-probes and sends when the peer recovers. See [Disk Space in swarm operations](../../operations/swarm.md#disk-space) for the fleet-wide policy.

## Deletion propagation (tombstones)

Deletion is a first-class mesh operation, not a local-only `rm`. When the operator removes a file from `~/lockbox/` on any envoy — via `vigo swarm lockbox delete <name>` or plain `rm` — the watcher fires a Delete event, the pusher signs a DELETE envelope and fans it out to every peer, and each peer records a **tombstone** and removes its local copy. Tombstones outlive the delete by `tombstone_retention_days` (default **90 days**) so that an envoy that was offline at delete time can't resurrect the file when it reconnects with a stale copy.

**Flow:**

1. `vigo swarm lockbox delete foo.pdf` (or `rm ~/lockbox/foo.pdf`) on envoy A.
2. Watcher catches the Remove event, emits a `DeleteJob { user, filename, deletion_time }`.
3. Pusher loads A's Ed25519 signing key (`/var/lib/vigo/swarm/lockbox/<user>/signing.key`, root-owned), builds a signed `DeleteEnvelope`, and POSTs it to every live LAN peer at `https://<peer>:1531/lockbox/tombstones/<user>/<filename>` plus every `cross_subnet_peers:` host.
4. Receiver validates the envelope against the sender's known signing pubkey from its local `peers.json`. Unknown sender or mismatched signature → 403 Forbidden.
5. On valid envelope: receiver records a tombstone in `/var/lib/vigo/swarm/lockbox/<user>/tombstones.json` (root-owned canonical) and mirrors the table to `~<user>/.lockbox/tombstones.json` (user-owned 0600) so `vigo swarm lockbox list` / `status` running as the unprivileged user can see it. Removes the local ciphertext file if present, returns 200 OK. The same daemon-side-canonical + user-side-mirror pattern applies to `leaves.json`.
6. Any future PUSH of the same filename from any peer → 410 Gone (anti-resurrection) until either a `lockbox resurrect` envelope supersedes the tombstone or the tombstone ages past retention.
7. Any future PULL of the tombstoned filename → 410 Gone. The puller records the tombstone locally on receiving this response — lazy catch-up for the rare code path that reaches pull.

**Catch-up after partition:** every announcement carries a 16-byte `tombstones_digest` (truncated SHA-256 over sorted `filename|deletion_time` entries) **and** a 16-byte `leaves_digest` (the analogous digest over `(age_pubkey, leave_time)` pairs from `LeavesTable`). A peer that observes a digest mismatch on either pulls the full table via `GET /lockbox/tombstones/<user>` or `GET /lockbox/leaves/<user>` and merges it. This is the common-case recovery path for "envoy was offline during a delete or LEAVE, comes back up with stale state." For tombstone catch-up the receiver also drops any local files newly covered by tombstones; for leaves catch-up it runs `apply_leave` on each newly-merged entry so the peer table prune + recipients regen + reencrypt sentinel fire just as they would have on the original LEAVE envelope. No peer-initiated work is required to converge.

### Signing keys

Each envoy generates an Ed25519 keypair *the first time the lockbox daemon observes enrollment for that user*. The raw private key lives at `/var/lib/vigo/swarm/lockbox/<user>/signing.key` (root-owned, 0600); the matching pubkey is at `signing.pub` (0644 text) and advertised on every announcement so peers can record it in their `peers.json` for signature verification.

The signing key is **daemon-owned, not password-wrapped**. Unlike the age identity (which gates content decryption — losing it is catastrophic and permanent), the signing key only authorizes destructive operations on behalf of this envoy. Recovery from a compromised signing key is fast: peers run `vigo swarm lockbox forget <stolen-host>` on every other envoy, which removes the compromised pubkey from their `peers.json`, and subsequent DELETE envelopes from the compromised host fail validation. This tradeoff is deliberate — wrapping the signing key would force the user to be unlocked for deletions to propagate, which breaks the pusher's reactive model.

### Resurrecting a deleted filename

While a tombstone is active, re-pushing that filename is rejected with 410 Gone on every receiver. The tombstone is a strict block, not an mtime-compared guard — lockbox's wire format doesn't currently carry a push-time mtime, so the receiver has no way to distinguish "legitimate re-publish" from "stale peer resurrecting old bytes."

The encrypt-time preflight (`cmd_encrypt`, `auto_encrypt`) refuses **before** doing any encryption work when the target name is effectively tombstoned. Without this, `lockbox encrypt` would silently produce ciphertext that every peer rejects with 410 — wasted CPU, destroyed plaintext, no operator-visible signal. The preflight fails with a message naming the override path:

```
filename `foo.pdf` is tombstoned (87d remaining in retention).
Run `vigo swarm lockbox resurrect <file> [--as <name>]` to override and re-encrypt.
```

The override paths, in order of preference:

- **`vigo swarm lockbox resurrect <file> [--as <name>]`** — operator override. Drops a `~/.lockbox/resurrect.pending` sentinel; the daemon picks it up on its dedicated 1s tick, signs a `ResurrectEnvelope` with the local Ed25519 signing key, persists a `ResurrectRecord` in the per-user resurrects store, and fans the envelope out to every live peer. The CLI awaits the daemon's sentinel removal (60s ceiling) before invoking the encrypt path, so by the time the watcher fires the PushJob, every reachable peer holds the resurrect record and the `effective_tombstone` gate returns None — the push lands instead of 410'ing. Offline peers catch up via `resurrects_digest` mismatch on their next announce-receive.
- **Rename** the new file (`vigo swarm lockbox encrypt foo-v2.pdf`) — different filename, no tombstone, no gate. Trivially works.
- **Wait out the tombstone.** Default retention is 90 days; after that, GC drops the tombstone and the resurrect record together (both age out at the same `tombstone_retention_days` window) and the original filename is freely usable again.

Mechanism: the gate is `tombstones::effective_tombstone(name, &tombstones, &resurrects)` — the tombstone for `name` is "effective" iff `tombstone.deletion_time > resurrect.resurrect_time` (or no resurrect exists). Tie-breaker: equal times go to the resurrect, so an operator-driven override always wins simultaneity. Subsequent re-DELETE on the same name simply lands a new tombstone with `deletion_time > resurrect_time` and the gate flips back. Per-filename last-write-wins.

Catch-up: announcements carry a third 16-byte digest, `resurrects_digest`, alongside `tombstones_digest` and `leaves_digest`. Receivers with a digest mismatch pull the full resurrects table via `GET /swarm/lockbox/resurrects/<user>` and merge — same shape as tombstone catch-up. The `RESURRECT` POST handler, the resurrects table type, and the GC tick all mirror their tombstone equivalents.

## Leaving lockbox

Two paths exist for removing a host from a user's lockbox circle — both land on the same local-apply function (`apply_leave` in `agent/src/lockbox/mod.rs`) but differ in how the agent learns about the leave.

### Path A — self-leave (user-triggered, P2P)

User runs `vigo swarm lockbox leave` on the departing envoy. The CLI drops a `~/.lockbox/leave.pending` sentinel; the daemon reactor picks it up on its next announce tick (~30 s), builds a **signed LEAVE envelope** (`agent/src/lockbox/leaves.rs`), fans it out to every live peer via `POST /lockbox/leaves/<user>`, persists the record to `/var/lib/vigo/swarm/lockbox/<user>/leaves.json`, and scrubs local state per the sentinel's flags.

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

Retention: 90 days (matches tombstones). A digest of the sorted `(age_pubkey, leave_time)` pairs piggybacks on future announcement payloads so offline-during-leave peers can catch up on reconnect via `GET /lockbox/leaves/<user>` — scheduled for Phase B.1.

### Path B — admin eviction (server-triggered, control plane)

Admin runs `vigocli swarm lockbox evict --host <h> {--user <u> | --all-users}`. The CLI queries `/api/v1/swarm/lockbox/fleet` to find affected users, then dispatches a shell command via `POST /api/v1/task`:

```
vigo swarm lockbox _apply-evict --user <u> --hostname <h>
```

Each enrolled agent receives the task, resolves the target's age pubkey from its own peer table, calls `apply_leave` directly (no envelope, no fan-out — the agent is authoritative for its own state). Offline agents pick up the task on next check-in via normal task-dispatch retry.

No signed envelope; authority is end-to-end mTLS + admin token at the control plane, not a P2P signature. This is the right shape for "gone host, can't self-leave" scenarios where no-one can produce the subject's signature.

**Self-evict scrub:** the dispatch goes to `target: "*"` so the evicted host receives `_apply-evict` for itself. Peer tables never include self, so the standard pubkey-lookup path would short-circuit as a no-op and leave the host's identity intact — blocking subsequent `vigo swarm lockbox enroll` (which bails when `~/.lockbox/identity.age` already exists). The agent detects the self case (local `/etc/hostname` matches the `--hostname` argument) and instead scrubs `~/.lockbox/identity.age`, `identity.salt`, `recipients.txt`, and `config.yaml` — same set as a `!keep_identity` self-leave. `~/lockbox/` ciphertext is preserved (operator can `rm -rf` for full wipe). Re-enroll then proceeds cleanly.

### Local scrub on self-leave

`vigo swarm lockbox leave` defaults to **delete identity, keep ciphertext**. `~/.lockbox/` is removed (wrapped identity, salt, recipients, config); `~/lockbox/` ciphertext is left in place but orphaned — the host no longer has an identity to decrypt it locally. Flags:

- `--purge` — also delete `~/lockbox/` ciphertext.
- `--keep-identity` — keep `~/.lockbox/` (rare; used when planning to re-enroll with the same keys).

### Reencrypt queueing on peers

When a LEAVE lands on a peer, the agent drops `~<user>/.lockbox/reencrypt.pending`. Existing ciphertext in `~/lockbox/` was wrapped for N recipients including the evicted one; it remains decryptable by the evicted host unless re-wrapped. The session helper drains the sentinel on its next tick (if the user is unlocked) or on the next `vigo swarm lockbox unlock` invocation — reencryption is eventually consistent and user-interactive by design.

### State persistence — per-user serialization

`TombstoneTable`, `LeavesTable`, and `PeerTable` all follow a load → mutate → save pattern. Two concurrent paths against the same user (e.g., the watcher fanning out N DeleteJobs from a single `vigo swarm lockbox leave --purge`, or simultaneous DELETE envelopes from different peers) would race their load-modify-save pairs — both load identical state, each upserts a different entry, the second save() overwrites the first, and the loser's update is silently lost. The atomic temp+rename guards readers against partial writes but doesn't serialize concurrent writers.

`agent/src/lockbox/locks.rs` exposes a `user_lock(table, user)` helper backed by three per-user lock maps (one per table type). Each table type has a `modify(user, |&mut Self| -> bool)` constructor that holds the per-user lock for the entire load+mutate+save+mirror sequence. Granularity is per-user so user A's tombstone activity doesn't block user B's, and per-table-type so a tombstone modify on user dan doesn't block a peer-table modify on user dan. Save still calls `write_user_mirror_json` inside the lock so canonical and user-side mirror stay in lockstep. Holding the lock across local file I/O is intentional — saves are rare and brief (milliseconds on local disk), and a copy-then-CAS shape would add code without a real win.

## Operability

Lockbox derives its envoy-level authorization from `swarm.puddle.enabled` (ADR-020 + 0.41.0). There is no `swarm.lockbox` section. A host allowed by the puddle pattern list can hold lockbox ciphertext for its locally-enrolled members; a host denied there cannot use lockbox at all.

```yaml
swarm:
  enabled: ["*"]
  puddle:
    enabled: ["annlap", "plex", "girlslaptop"]   # lockbox follows
```

Both `swarm.enabled` AND `swarm.puddle.enabled` must allow the host. The agent's `Substrate::Lockbox` gate (in `agent/src/swarm/gates.rs`) consults the resolved `swarm_puddle_enabled` bool from `PolicyBundle.vars_json` — single source of truth, no separate lockbox key.

When a host falls out of the pattern list, the announcer / listener / push handler all idle on the next check-in. Already-wrapped identity material under `~user/.lockbox/` and ciphertext under `~user/lockbox/` are preserved — gate flips are pause/resume, not evict.

The destructive lever moved to the **usercrate `puddle: bool` flag** (0.41.0). Removing `puddle: true` from a usercrate triggers a scrub on the next reconcile pass: queue a lockbox LEAVE sentinel for fan-out, then wipe `~/.vigo-puddle/`, `~/.lockbox/`, `~/lockbox/`, and `~/.vigo-gitback/` for that user. See [swarm Operability](swarm.md#operability--pattern-list-authorization) for the full picture.

Pattern-list changes record tamper-evident audit-chain entries (`swarm.puddle.enabled`) under `actor=config.reload` at publish time.

## Threat Model

**Lockbox protects against:**
- Root compromise on any envoy *without* the user's lockbox password. The attacker sees the wrapped `identity.age` + ciphertext only.
- Physical theft of an envoy with no active unlock session.
- Network eavesdropping (mTLS transport + age content encryption — defense in depth).
- Revoked envoys: files encrypted after `forget` cannot be decrypted by the revoked host.

**Lockbox does not protect against:**
- Root compromise on an envoy with an active unlocked session — root can read the session helper's memory.
- Keyloggers or compromised shells capturing the password.
- Weak passwords (Argon2id slows brute force but doesn't prevent it).
- Filename, size, or mtime leakage — these are not encrypted (deliberate, matches longdrawer's boundary).

Lose the password on every envoy and the data is unrecoverable. Phase 1 has no key escrow.

## Observability (`/lockbox` UI)

Every enrolled envoy ships a `lockbox` trait on each check-in carrying per-user state: file count, `recent_files: [last 20 by mtime]`, ciphertext SHA-256 digest, tombstone count + recent deletions, known peers (hostname + pubkey + sign_pubkey), and the current session-helper-unlocked signal. The server-side aggregator (`server/lockboxmesh/`) joins these across the fleet to power the `/lockbox` page.

The `/longdrawer`, `/lockbox`, `/gitback` pages and every mesh endpoint (`/api/v1/swarm/longdrawer/*`, `/api/v1/swarm/lockbox/*`, `/api/v1/swarm/gitback/fleet`, and the UI fragment drill-downs) are admin-only — fleet-wide per-user file lists aren't appropriate for non-admin roles. Viewer and compliance-role users get a 403 on direct access and don't see the nav links. The legacy `/mesh` URL 301-redirects to `/longdrawer` (or `/lockbox` with `?tab=crypt`) for one release while muscle memory migrates.

Operator-facing signals surfaced on that page:

- **Presence matrix** — per (user × envoy × filename) cells showing live / tombstoned / missing, with divergence flagged when shas disagree
- **Recipient drift** — two envoys in the same user's mesh with different peer sets. Common cause: one host missed a recent enrollment and files encrypted there won't decrypt on the unknown peer
- **Active sessions** — which envoys currently hold an unlocked identity in RAM
- **Three-state envoy reporting** — reporting (trait fresh) / stale (trait older than 5 min) / not_reporting (no trait — rolling upgrade or dead envoy), visually distinct so you don't confuse "not upgraded yet" with "offline"

The drill-down endpoint `GET /api/v1/swarm/lockbox/envoy/{id}/users/{user}/files` returns the full file list (not the bounded 20) for one envoy cell when an operator clicks through.

Ciphertext SHA-256 is cached per-(filename, mtime, size) at `/var/lib/vigo/swarm/lockbox/<user>/sha_cache.json` so multi-GiB files aren't re-hashed every trait cycle.

## Relationship to Other Subsystems

- **Longdrawer** is the plaintext sibling. Lockbox does not replace it; pick whichever matches the threat model.
- **Swarm** handles fleet-wide blob distribution (Vigo-managed content). Lockbox rides on the same mTLS peer server (port 1531) but uses its own routes, its own multicast group (224.0.0.44:1533), and its own peer table — no shared state.
- **Gitback** is the git-repo sibling for personal DR. Similar opt-in pattern; different subsystem entirely.
