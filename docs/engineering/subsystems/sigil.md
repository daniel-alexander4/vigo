
# Sigil — Per-User Encrypted P2P Directory

Sigil keeps `~/sigil/` in sync across every envoy where a user is enrolled, **ciphertext at rest** everywhere. Drop a plaintext file on the primary, sigil encrypts it to every peer's public key and fans out the ciphertext. Unlock with the user's sigil password on any envoy to decrypt.

Sigil is the encryption-first sibling of [Echo](echo.md):

| | Echo | Sigil |
|---|---|---|
| At-rest state | Plaintext | **Ciphertext** everywhere |
| Key material | N/A | Per-envoy age identity, password-wrapped |
| Read access | Any user login | Requires `vigo sigil unlock` (password) |
| Fan-out trigger | Directory scan every 30s | Filesystem watch + explicit CLI |
| Membership | Any user on the LAN with `~/echo/` | `sigil: true` on the user's usercrate |

Pick echo for ambient work files you want across machines. Pick sigil for anything you'd want to stay unreadable on a stolen envoy.

Sigil is one of four content subsystems that ride the [swarm](swarm.md) substrate. The server plays no role in content or keys — sigil reuses swarm's mTLS peer channel (port 1531) for ciphertext transfers and runs its own multicast group (`224.0.0.44:1533`) for announcements.

## How It Works

Sigil is a **pure peer-to-peer** feature. The Vigo server plays no role in content or keys. Envoys discover each other via UDP multicast and exchange ciphertext directly over mTLS on swarm's peer port.

1. **Enroll.** On each envoy that should hold the user's encrypted files, the user's usercrate applies `sigil: true`. The user executor drops `~/.sigil/enroll.pending`. The next time the user runs any `vigo sigil *` verb, the CLI prompts for the sigil password and generates a fresh age identity on that envoy. The identity is wrapped with the password (Argon2id + XChaCha20-Poly1305) and stored at `~/.sigil/identity.age` with mode 0600. The public key is written to `~/.sigil/recipients.txt` and begins appearing in UDP announcements.
2. **Discover.** Every 30 seconds, each enrolled envoy multicasts a compact announcement on `224.0.0.44:1533` — `{machine, user, pubkey, emitted_at, port}`. Other envoys running sigil for the same user receive the announcement and add the peer to `/var/lib/vigo/sigil/<user>/peers.json`. `~/.sigil/recipients.txt` is regenerated from the peer table so future encrypts reach the new peer automatically.
3. **Encrypt.** The user runs `vigo sigil encrypt ~/Downloads/foo.pdf`. The CLI reads `recipients.txt`, age-encrypts the plaintext to every listed public key, and writes the ciphertext atomically to `~/sigil/foo.pdf`. No password prompt — encryption uses public keys only.
4. **Fan out.** The agent daemon watches `~/sigil/` via inotify (Linux) / kqueue (macOS). On a new or modified file, the pusher POSTs the ciphertext to every live peer on `https://<peer>:1531/sigil/push/<user>/<filename>`, plus every host listed in the user's `cross_subnet_peers:` config. Pushes run in parallel with bounded concurrency (4 at a time).
5. **Receive.** On the peer side, the `/sigil/push/...` HTTPS handler writes the ciphertext atomically into the target user's `~/sigil/<filename>`. Every envoy in the mesh ends up with identical bytes on disk.
6. **Decrypt.** On any enrolled envoy, `vigo sigil unlock` prompts for the password, unwraps the age identity into a per-user session helper daemon, and holds the key in RAM until the session ends or the idle timer fires. `vigo sigil decrypt <name> -o <path>` streams the ciphertext through the helper's DECRYPT socket and writes plaintext to a user-chosen path.
7. **Revoke.** `vigo sigil forget <hostname>` removes the peer's pubkey from `recipients.txt` so future encrypts exclude it. `vigo sigil reencrypt` re-encrypts every existing file in `~/sigil/` to the reduced recipient set, cutting off access to any ciphertext the revoked envoy will cache afterward.

## Commands

| Command | Behavior |
|---|---|
| `vigo sigil init` | First-time setup on the primary envoy. Prompts password (confirm), generates wrapped identity, creates `~/sigil/`. |
| `vigo sigil enroll` | Per-envoy setup. Normally triggered by the `enroll.pending` sentinel on first `vigo sigil *` invocation; can be run manually. Idempotent. |
| `vigo sigil unlock` | Prompt password, start the session helper daemon. |
| `vigo sigil lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `vigo sigil encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/sigil/`. Does not require `unlock`. |
| `vigo sigil decrypt <file> -o <path>` | Decrypt from `~/sigil/` to a user-chosen path. Requires `unlock`. `-o` is mandatory — no default destination. |
| `vigo sigil forget <hostname>` | Revoke an envoy from the recipient set. |
| `vigo sigil reencrypt` | Force re-encrypt of every file in `~/sigil/` to the current recipient set. Requires `unlock`. |
| `vigo sigil status` | Show local state: enrolled, recipients, file count, session helper status. |

## Usercrate Integration

```yaml
- type: user
  name: dan
  home: /home/dan
  shell: /bin/bash
  sigil: true        # enroll this user in sigil on this envoy
```

`sigil: true` tells the user executor to drop `~/.sigil/enroll.pending` after user creation. On first login the user runs any `vigo sigil *` verb and completes enrollment interactively (one password prompt). The field is idempotent — flipping it true on an already-enrolled user is a no-op.

The sigil password is **not** the login password. Phase 1 has no PAM integration: the user sets a separate sigil password at enrollment and types it again at each `unlock`.

## Key Wrapping

- **Identity**: fresh age X25519 keypair generated locally on each envoy, never transmitted or regenerated.
- **KDF**: Argon2id (memory=64 MiB, iterations=3, parallelism=1, salt=16 random bytes).
- **AEAD**: XChaCha20-Poly1305 with a 24-byte random nonce per wrap.
- **Blob format** at `~/.sigil/identity.age`: `[1 byte version = 0x01][24 byte nonce][ciphertext + 16 byte auth tag]`. Mode 0600, user-owned.

The agent daemon (running as root) never holds the unwrapped identity. Only the per-user session helper does, only while unlocked, only in RAM.

## Session Helper

`vigo sigil unlock` spawns a detached per-user daemon process (`setsid` away from the login shell) that holds the unwrapped age identity in memory and serves DECRYPT/STATUS/LOCK requests over a Unix socket:

- **Socket**: `$XDG_RUNTIME_DIR/vigo/sigil.sock` (Linux) or `$TMPDIR/sigil-<UID>.sock` (macOS), mode 0600.
- **Authentication**: `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS). Only connections from the same UID are accepted.
- **Parent-PID watchdog**: polls the login shell's PID every 10s and exits when it's gone.
- **Idle timeout**: default 60 minutes, configurable via `idle_timeout_minutes` in `~/.sigil/config.yaml`. Resets on every DECRYPT/STATUS.
- **Log**: `$XDG_RUNTIME_DIR/vigo/sigil.log`.

Explicit `vigo sigil lock` sends a LOCK command. The helper zeroizes the key in memory and exits. Socket and PID file are unlinked.

## Cross-Subnet Peers

Multicast doesn't cross subnets. For envoys on different networks, list them explicitly in `~/.sigil/config.yaml`:

```yaml
cross_subnet_peers:
  - envoy-london.internal
  - envoy-tokyo.internal
idle_timeout_minutes: 60
```

The agent announces to these hosts via `POST /sigil/announce` on port 1531 and fans out ciphertext via `POST /sigil/push/...` the same way as LAN peers. Ports 1531 and 1533 must be reachable; the same fleet CA must be trusted on both sides.

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable) |
| Content supported | Regular files in `~/sigil/` only (no subdirectories, no symlinks) |
| Filenames | Plaintext on the wire and on disk — only content is encrypted |
| Platforms | Linux + macOS (Phase 1) |
| Password recovery | None — lose the password on every envoy and the data is unrecoverable |
| Keys per envoy | One age identity per user per envoy |

**Disk-pressure guardrail.** Before every `POST /sigil/push/{user}/{filename}`, the sender probes the recipient at `GET /health/storage?for=sigil&user=<user>` (60 s cache per peer+user) and skips the push if the recipient reports `free_pct <= 10`. The receiver's push handler re-checks `~<user>/sigil` on entry and returns **507 Insufficient Storage** before reading a byte of the body, closing the race between probe and arrival. Skipped pushes are no-ops — no retry backoff, no failure counter; the next watcher event re-probes and sends when the peer recovers. See [Disk Space in swarm operations](../../operations/swarm.md#disk-space) for the fleet-wide policy.

## Deletion propagation (tombstones)

Deletion is a first-class mesh operation, not a local-only `rm`. When the operator removes a file from `~/sigil/` on any envoy — via `vigo sigil delete <name>` or plain `rm` — the watcher fires a Delete event, the pusher signs a DELETE envelope and fans it out to every peer, and each peer records a **tombstone** and removes its local copy. Tombstones outlive the delete by `tombstone_retention_days` (default **90 days**) so that an envoy that was offline at delete time can't resurrect the file when it reconnects with a stale copy.

**Flow:**

1. `vigo sigil delete foo.pdf` (or `rm ~/sigil/foo.pdf`) on envoy A.
2. Watcher catches the Remove event, emits a `DeleteJob { user, filename, deletion_time }`.
3. Pusher loads A's Ed25519 signing key (`/var/lib/vigo/sigil/<user>/signing.key`, root-owned), builds a signed `DeleteEnvelope`, and POSTs it to every live LAN peer at `https://<peer>:1531/sigil/tombstones/<user>/<filename>` plus every `cross_subnet_peers:` host.
4. Receiver validates the envelope against the sender's known signing pubkey from its local `peers.json`. Unknown sender or mismatched signature → 403 Forbidden.
5. On valid envelope: receiver records a tombstone in `/var/lib/vigo/sigil/<user>/tombstones.json` (root-owned canonical) and mirrors the table to `~<user>/.sigil/tombstones.json` (user-owned 0600) so `vigo sigil list` / `status` running as the unprivileged user can see it. Removes the local ciphertext file if present, returns 200 OK. The same daemon-side-canonical + user-side-mirror pattern applies to `leaves.json`.
6. Any future PUSH of the same filename from any peer → 410 Gone (anti-resurrection) unless the tombstone is cleared by a newer-mtime republish.
7. Any future PULL of the tombstoned filename → 410 Gone. The puller records the tombstone locally on receiving this response — lazy catch-up for the rare code path that reaches pull.

**Catch-up after partition:** every announcement carries a 16-byte `tombstones_digest` (truncated SHA-256 over sorted `filename|deletion_time` entries). A peer that observes a digest mismatch pulls the full tombstone list via `GET /sigil/tombstones/<user>` and merges it. This is the common-case recovery path for "envoy was offline during a delete, comes back up with the old file." No peer-initiated work is required to converge.

### Signing keys

Each envoy generates an Ed25519 keypair *the first time the sigil daemon observes enrollment for that user*. The raw private key lives at `/var/lib/vigo/sigil/<user>/signing.key` (root-owned, 0600); the matching pubkey is at `signing.pub` (0644 text) and advertised on every announcement so peers can record it in their `peers.json` for signature verification.

The signing key is **daemon-owned, not password-wrapped**. Unlike the age identity (which gates content decryption — losing it is catastrophic and permanent), the signing key only authorizes destructive operations on behalf of this envoy. Recovery from a compromised signing key is fast: peers run `vigo sigil forget <stolen-host>` on every other envoy, which removes the compromised pubkey from their `peers.json`, and subsequent DELETE envelopes from the compromised host fail validation. This tradeoff is deliberate — wrapping the signing key would force the user to be unlocked for deletions to propagate, which breaks the pusher's reactive model.

### What happens when you re-create a deleted filename

While a tombstone is active, re-pushing that filename is rejected with 410 Gone on every receiver. The tombstone is a strict block, not an mtime-compared guard — sigil's wire format doesn't currently carry a push-time mtime, so the receiver has no way to distinguish "legitimate re-publish" from "stale peer resurrecting old bytes." To re-create a deleted filename while its tombstone is still active, pick one of:

- **Rename** the new file (`vigo sigil encrypt foo-v2.pdf`) — different filename, no tombstone, no rejection.
- **Wait out the tombstone.** Default retention is 90 days; after that the tombstone is GC'd and the original filename is usable again.
- **Manually clear the tombstone** on each envoy: `sudo vigocli swarm cleanup sigil --user <user> --filename <name>` (planned — not shipped in the initial tombstone feature).

Filename reuse with a fresh name is the recommended pattern.

## Leaving sigil

Two paths exist for removing a host from a user's sigil circle — both land on the same local-apply function (`apply_leave` in `agent/src/sigil/mod.rs`) but differ in how the agent learns about the leave.

### Path A — self-leave (user-triggered, P2P)

User runs `vigo sigil leave` on the departing envoy. The CLI drops a `~/.sigil/leave.pending` sentinel; the daemon reactor picks it up on its next announce tick (~30 s), builds a **signed LEAVE envelope** (`agent/src/sigil/leaves.rs`), fans it out to every live peer via `POST /sigil/leaves/<user>`, persists the record to `/var/lib/vigo/sigil/<user>/leaves.json`, and scrubs local state per the sentinel's flags.

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

Retention: 90 days (matches tombstones). A digest of the sorted `(age_pubkey, leave_time)` pairs piggybacks on future announcement payloads so offline-during-leave peers can catch up on reconnect via `GET /sigil/leaves/<user>` — scheduled for Phase B.1.

### Path B — admin eviction (server-triggered, control plane)

Admin runs `vigocli sigil evict --host <h> {--user <u> | --all-users}`. The CLI queries `/api/v1/sigil/fleet` to find affected users, then dispatches a shell command via `POST /api/v1/task`:

```
vigo sigil _apply-evict --user <u> --hostname <h>
```

Each enrolled agent receives the task, resolves the target's age pubkey from its own peer table, calls `apply_leave` directly (no envelope, no fan-out — the agent is authoritative for its own state). Offline agents pick up the task on next check-in via normal task-dispatch retry.

No signed envelope; authority is end-to-end mTLS + admin token at the control plane, not a P2P signature. This is the right shape for "gone host, can't self-leave" scenarios where no-one can produce the subject's signature.

### Local scrub on self-leave

`vigo sigil leave` defaults to **delete identity, keep ciphertext**. `~/.sigil/` is removed (wrapped identity, salt, recipients, config); `~/sigil/` ciphertext is left in place but orphaned — the host no longer has an identity to decrypt it locally. Flags:

- `--purge` — also delete `~/sigil/` ciphertext.
- `--keep-identity` — keep `~/.sigil/` (rare; used when planning to re-enroll with the same keys).

### Reencrypt queueing on peers

When a LEAVE lands on a peer, the agent drops `~<user>/.sigil/reencrypt.pending`. Existing ciphertext in `~/sigil/` was wrapped for N recipients including the evicted one; it remains decryptable by the evicted host unless re-wrapped. The session helper drains the sentinel on its next tick (if the user is unlocked) or on the next `vigo sigil unlock` invocation — reencryption is eventually consistent and user-interactive by design.

## Threat Model

**Sigil protects against:**
- Root compromise on any envoy *without* the user's sigil password. The attacker sees the wrapped `identity.age` + ciphertext only.
- Physical theft of an envoy with no active unlock session.
- Network eavesdropping (mTLS transport + age content encryption — defense in depth).
- Revoked envoys: files encrypted after `forget` cannot be decrypted by the revoked host.

**Sigil does not protect against:**
- Root compromise on an envoy with an active unlocked session — root can read the session helper's memory.
- Keyloggers or compromised shells capturing the password.
- Weak passwords (Argon2id slows brute force but doesn't prevent it).
- Filename, size, or mtime leakage — these are not encrypted (deliberate, matches echo's boundary).

Lose the password on every envoy and the data is unrecoverable. Phase 1 has no key escrow.

## Observability (`/sigil` UI)

Every enrolled envoy ships a `sigil` trait on each check-in carrying per-user state: file count, `recent_files: [last 20 by mtime]`, ciphertext SHA-256 digest, tombstone count + recent deletions, known peers (hostname + pubkey + sign_pubkey), and the current session-helper-unlocked signal. The server-side aggregator (`server/sigilmesh/`) joins these across the fleet to power the `/sigil` page.

The `/echo`, `/sigil`, `/cairn` pages and every mesh endpoint (`/api/v1/echo/*`, `/api/v1/sigil/*`, `/api/v1/cairn/fleet`, and the UI fragment drill-downs) are admin-only — fleet-wide per-user file lists aren't viewer-appropriate. Viewers get a 403 on direct access and don't see the nav links. The legacy `/mesh` URL 301-redirects to `/echo` (or `/sigil` with `?tab=crypt`) for one release while muscle memory migrates.

Operator-facing signals surfaced on that page:

- **Presence matrix** — per (user × envoy × filename) cells showing live / tombstoned / missing, with divergence flagged when shas disagree
- **Recipient drift** — two envoys in the same user's mesh with different peer sets. Common cause: one host missed a recent enrollment and files encrypted there won't decrypt on the unknown peer
- **Active sessions** — which envoys currently hold an unlocked identity in RAM
- **Three-state envoy reporting** — reporting (trait fresh) / stale (trait older than 5 min) / not_reporting (no trait — rolling upgrade or dead envoy), visually distinct so you don't confuse "not upgraded yet" with "offline"

The drill-down endpoint `GET /api/v1/sigil/envoy/{id}/users/{user}/files` returns the full file list (not the bounded 20) for one envoy cell when an operator clicks through.

Ciphertext SHA-256 is cached per-(filename, mtime, size) at `/var/lib/vigo/sigil/<user>/sha_cache.json` so multi-GiB files aren't re-hashed every trait cycle.

## Relationship to Other Subsystems

- **Echo** is the plaintext sibling. Sigil does not replace it; pick whichever matches the threat model.
- **Swarm** handles fleet-wide blob distribution (Vigo-managed content). Sigil rides on the same mTLS peer server (port 1531) but uses its own routes, its own multicast group (224.0.0.44:1533), and its own peer table — no shared state.
- **Cairn** is the git-repo sibling for personal DR. Similar opt-in pattern; different subsystem entirely.
