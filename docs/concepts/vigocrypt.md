
# Vigocrypt — Per-User Encrypted P2P Directory

Vigocrypt keeps `~/vigocrypt/` in sync across every envoy where a user is enrolled, **ciphertext at rest** everywhere. Drop a plaintext file on the primary, vigocrypt encrypts it to every peer's public key and fans out the ciphertext. Unlock with the user's vigocrypt password on any envoy to decrypt.

Vigocrypt is the encryption-first sibling of [Vigosync](vigosync.md):

| | Vigosync | Vigocrypt |
|---|---|---|
| At-rest state | Plaintext | **Ciphertext** everywhere |
| Key material | N/A | Per-envoy age identity, password-wrapped |
| Read access | Any user login | Requires `vigo crypt unlock` (password) |
| Fan-out trigger | Directory scan every 30s | Filesystem watch + explicit CLI |
| Membership | Any user on the LAN with `~/vigosync/` | `vigocrypt: true` on the user module |

Pick vigosync for ambient work files you want across machines. Pick vigocrypt for anything you'd want to stay unreadable on a stolen envoy.

## How It Works

Vigocrypt is a **pure peer-to-peer** feature. The Vigo server plays no role in content or keys. Envoys discover each other via UDP multicast and exchange ciphertext directly over mTLS.

1. **Enroll.** On each envoy that should hold the user's encrypted files, the user module applies `vigocrypt: true`. The user executor drops `~/.vigocrypt/enroll.pending`. The next time the user runs any `vigo crypt *` verb, the CLI prompts for the vigocrypt password and generates a fresh age identity on that envoy. The identity is wrapped with the password (Argon2id + XChaCha20-Poly1305) and stored at `~/.vigocrypt/identity.age` with mode 0600. The public key is written to `~/.vigocrypt/recipients.txt` and begins appearing in UDP announcements.
2. **Discover.** Every 30 seconds, each enrolled envoy multicasts a compact announcement on `224.0.0.44:1533` — `{machine, user, pubkey, emitted_at, port}`. Other envoys running vigocrypt for the same user receive the announcement and add the peer to `/var/lib/vigo/vigocrypt/<user>/peers.json`. `~/.vigocrypt/recipients.txt` is regenerated from the peer table so future encrypts reach the new peer automatically.
3. **Encrypt.** The user runs `vigo crypt encrypt ~/Downloads/foo.pdf`. The CLI reads `recipients.txt`, age-encrypts the plaintext to every listed public key, and writes the ciphertext atomically to `~/vigocrypt/foo.pdf`. No password prompt — encryption uses public keys only.
4. **Fan out.** The agent daemon watches `~/vigocrypt/` via inotify (Linux) / kqueue (macOS). On a new or modified file, the pusher POSTs the ciphertext to every live peer on `https://<peer>:1531/vigocrypt/push/<user>/<filename>`, plus every host listed in the user's `extra_peers:` config. Pushes run in parallel with bounded concurrency (4 at a time).
5. **Receive.** On the peer side, the `/vigocrypt/push/...` HTTPS handler writes the ciphertext atomically into the target user's `~/vigocrypt/<filename>`. Every envoy in the mesh ends up with identical bytes on disk.
6. **Decrypt.** On any enrolled envoy, `vigo crypt unlock` prompts for the password, unwraps the age identity into a per-user session helper daemon, and holds the key in RAM until the session ends or the idle timer fires. `vigo crypt decrypt <name> -o <path>` streams the ciphertext through the helper's DECRYPT socket and writes plaintext to a user-chosen path.
7. **Revoke.** `vigo crypt forget <hostname>` removes the peer's pubkey from `recipients.txt` so future encrypts exclude it. `vigo crypt reencrypt` re-encrypts every existing file in `~/vigocrypt/` to the reduced recipient set, cutting off access to any ciphertext the revoked envoy will cache afterward.

## Commands

| Command | Behavior |
|---|---|
| `vigo crypt init` | First-time setup on the primary envoy. Prompts password (confirm), generates wrapped identity, creates `~/vigocrypt/`. |
| `vigo crypt enroll` | Per-envoy setup. Normally triggered by the `enroll.pending` sentinel on first `vigo crypt *` invocation; can be run manually. Idempotent. |
| `vigo crypt unlock` | Prompt password, start the session helper daemon. |
| `vigo crypt lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `vigo crypt encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/vigocrypt/`. Does not require `unlock`. |
| `vigo crypt decrypt <file> -o <path>` | Decrypt from `~/vigocrypt/` to a user-chosen path. Requires `unlock`. `-o` is mandatory — no default destination. |
| `vigo crypt forget <hostname>` | Revoke an envoy from the recipient set. |
| `vigo crypt reencrypt` | Force re-encrypt of every file in `~/vigocrypt/` to the current recipient set. Requires `unlock`. |
| `vigo crypt status` | Show local state: enrolled, recipients, file count, session helper status. |

## User Module Integration

```yaml
- type: user
  name: dan
  home: /home/dan
  shell: /bin/bash
  vigocrypt: true        # enroll this user in vigocrypt on this envoy
```

`vigocrypt: true` tells the user executor to drop `~/.vigocrypt/enroll.pending` after user creation. On first login the user runs any `vigo crypt *` verb and completes enrollment interactively (one password prompt). The field is idempotent — flipping it true on an already-enrolled user is a no-op.

The vigocrypt password is **not** the login password. Phase 1 has no PAM integration: the user sets a separate vigocrypt password at enrollment and types it again at each `unlock`.

## Key Wrapping

- **Identity**: fresh age X25519 keypair generated locally on each envoy, never transmitted or regenerated.
- **KDF**: Argon2id (memory=64 MiB, iterations=3, parallelism=1, salt=16 random bytes).
- **AEAD**: XChaCha20-Poly1305 with a 24-byte random nonce per wrap.
- **Blob format** at `~/.vigocrypt/identity.age`: `[1 byte version = 0x01][24 byte nonce][ciphertext + 16 byte auth tag]`. Mode 0600, user-owned.

The agent daemon (running as root) never holds the unwrapped identity. Only the per-user session helper does, only while unlocked, only in RAM.

## Session Helper

`vigo crypt unlock` spawns a detached per-user daemon process (`setsid` away from the login shell) that holds the unwrapped age identity in memory and serves DECRYPT/STATUS/LOCK requests over a Unix socket:

- **Socket**: `$XDG_RUNTIME_DIR/vigo/vigocrypt.sock` (Linux) or `$TMPDIR/vigocrypt-<UID>.sock` (macOS), mode 0600.
- **Authentication**: `SO_PEERCRED` (Linux) / `LOCAL_PEEREID` (macOS). Only connections from the same UID are accepted.
- **Parent-PID watchdog**: polls the login shell's PID every 10s and exits when it's gone.
- **Idle timeout**: default 60 minutes, configurable via `idle_timeout_minutes` in `~/.vigocrypt/config.yaml`. Resets on every DECRYPT/STATUS.
- **Log**: `$XDG_RUNTIME_DIR/vigo/vigocrypt.log`.

Explicit `vigo crypt lock` sends a LOCK command. The helper zeroizes the key in memory and exits. Socket and PID file are unlinked.

## Cross-Subnet Peers

Multicast doesn't cross subnets. For envoys on different networks, list them explicitly in `~/.vigocrypt/config.yaml`:

```yaml
extra_peers:
  - envoy-london.internal
  - envoy-tokyo.internal
idle_timeout_minutes: 60
```

The agent announces to these hosts via `POST /vigocrypt/announce` on port 1531 and fans out ciphertext via `POST /vigocrypt/push/...` the same way as LAN peers. Ports 1531 and 1533 must be reachable; the same fleet CA must be trusted on both sides.

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable) |
| Content supported | Regular files in `~/vigocrypt/` only (no subdirectories, no symlinks) |
| Filenames | Plaintext on the wire and on disk — only content is encrypted |
| Platforms | Linux + macOS (Phase 1) |
| Password recovery | None — lose the password on every envoy and the data is unrecoverable |
| Keys per envoy | One age identity per user per envoy |

## Threat Model

**Vigocrypt protects against:**
- Root compromise on any envoy *without* the user's vigocrypt password. The attacker sees the wrapped `identity.age` + ciphertext only.
- Physical theft of an envoy with no active unlock session.
- Network eavesdropping (mTLS transport + age content encryption — defense in depth).
- Revoked envoys: files encrypted after `forget` cannot be decrypted by the revoked host.

**Vigocrypt does not protect against:**
- Root compromise on an envoy with an active unlocked session — root can read the session helper's memory.
- Keyloggers or compromised shells capturing the password.
- Weak passwords (Argon2id slows brute force but doesn't prevent it).
- Filename, size, or mtime leakage — these are not encrypted (deliberate, matches vigosync's boundary).

Lose the password on every envoy and the data is unrecoverable. Phase 1 has no key escrow.

## Relationship to Other Subsystems

- **Vigosync** is the plaintext sibling. Vigocrypt does not replace it; pick whichever matches the threat model.
- **Swarm** handles fleet-wide blob distribution (Vigo-managed content). Vigocrypt rides on the same mTLS peer server (port 1531) but uses its own routes, its own multicast group (224.0.0.44:1533), and its own peer table — no shared state.
- **Vigorepo** is the git-repo sibling for personal DR. Similar opt-in pattern; different subsystem entirely.
