
# vigo crypt CLI Reference

`vigo crypt` is the user-facing interface to [vigocrypt](../../concepts/vigocrypt.md), the per-user encrypted P2P directory. It runs inside the `vigo` agent binary, operates on files under the invoking user's home, and does not require root. The agent daemon is never contacted directly by the CLI — `unlock` spawns its own session helper daemon, and `encrypt`/`decrypt` talk to that helper over a local Unix socket.

## Invocation

```
vigo crypt <verb> [args]
```

Every verb runs under the invoking user's uid. No root. The agent daemon does not need to be running for `init`, `enroll`, `status`, or `encrypt` — only for fan-out of the written ciphertext to other envoys.

## Permission model

- `init` and `enroll` write `identity.age` + `identity.salt` + `recipients.txt` with mode 0600 under `~/.vigocrypt/`, owned by the invoking user. The vigocrypt password wraps the age identity with Argon2id — the agent running as root cannot decrypt these files without the password.
- `unlock` spawns the session helper daemon. The helper is `setsid`-detached so it outlives the shell that ran `unlock`. It holds the unwrapped key in its own process memory and serves DECRYPT/STATUS/LOCK over a Unix socket in `$XDG_RUNTIME_DIR/vigo/` (Linux) or `$TMPDIR/vigocrypt-<UID>.sock` (macOS), mode 0600, `SO_PEERCRED`-gated so only the same uid can connect.
- `encrypt` reads `recipients.txt` (public keys only, no secret material) and writes ciphertext into `~/vigocrypt/`. Does not require `unlock` — encryption is a pure pubkey operation.
- `decrypt` sends ciphertext bytes to the running session helper and receives plaintext back. Requires a live helper (i.e. `unlock` has been run).
- `lock` shuts down the helper and scrubs the key. Auto-fires on logout, idle timeout, or explicit invocation.

## Commands

| Verb | Purpose |
|---|---|
| `init` | First-time setup on the primary envoy. Prompts for password (with confirm), generates wrapped identity, creates `~/vigocrypt/`. |
| `enroll` | Per-envoy setup. Normally triggered by the `enroll.pending` sentinel on first `vigo crypt *` invocation; can be run manually for recovery. Idempotent. |
| `unlock` | Prompt password, spawn session helper that holds the unwrapped identity. |
| `lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/vigocrypt/` to every current recipient. |
| `decrypt <file> -o <path>` | Decrypt from `~/vigocrypt/` to a user-chosen path. Requires `unlock`. `-o` is mandatory. |
| `forget <hostname>` | Revoke an envoy from the recipient set; triggers a local `recipients.txt` regeneration. Follow with `reencrypt` to re-key existing files. |
| `reencrypt` | Force re-encrypt of every file in `~/vigocrypt/` to the current recipient set. Requires `unlock`. |
| `status` | Show local state: enrolled, recipient count, file count, session helper status. |

## File layout

```
~/.vigocrypt/
  identity.age     # wrapped age private key (binary: [version][nonce][ciphertext+tag])
  identity.salt    # 16-byte random Argon2id salt
  recipients.txt   # every enrolled envoy's pubkey for this user, one per line
  config.yaml      # user-managed: extra_peers, idle_timeout_minutes
  enroll.pending   # sentinel dropped by the user executor; removed on first enroll

~/vigocrypt/
  foo.pdf          # age ciphertext at rest
  notes.md         # age ciphertext at rest
```

Plus agent-owned state at `/var/lib/vigo/vigocrypt/<user>/peers.json` (root-owned, 0600). The user never reads this; it's the agent's peer bookkeeping.

## Typical flow

**Primary envoy, one-time:**
```
$ vigo crypt init
vigocrypt password: ****
confirm password:   ****
wrote ~/.vigocrypt/identity.age (0600, wrapped)
wrote ~/.vigocrypt/identity.salt (0600)
wrote ~/.vigocrypt/recipients.txt
created ~/vigocrypt (0700)

public key: age1abc...
```

**Secondary envoy (after usercrate applied with `vigocrypt: true`):**
```
$ ssh dan@laptop.home
$ vigo crypt status       # detects enroll.pending, runs enrollment interactively
vigocrypt password: ****
...
```

**Daily use:**
```
$ vigo crypt encrypt ~/Downloads/tax.pdf
✓ Encrypted to 3 recipient(s); wrote ~/vigocrypt/tax.pdf

$ vigo crypt unlock
vigocrypt password: ****
✓ Unlocked. Session helper running until logout, lock, or 60 min idle.

$ vigo crypt decrypt tax.pdf -o /tmp/tax.pdf
✓ Wrote 847293 bytes to /tmp/tax.pdf

$ vigo crypt lock
✓ Locked. Key scrubbed.
```

**Revoking an envoy:**
```
$ vigo crypt forget laptop.home
✓ Forgot laptop.home. Recipients regenerated.
  Run `vigo crypt reencrypt` to re-encrypt existing files
  so the revoked host cannot decrypt them from its cached ciphertext.

$ vigo crypt unlock
vigocrypt password: ****
$ vigo crypt reencrypt
✓ Re-encrypted 47 file(s) to 2 recipient(s); skipped 0
```

## Configuration

`~/.vigocrypt/config.yaml` is optional:

```yaml
extra_peers:
  - envoy-london.internal   # cross-subnet peers unreachable via LAN multicast
idle_timeout_minutes: 60    # session helper auto-lock (default 60)
```

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable server-side) |
| Content scope | Regular files in top-level `~/vigocrypt/` only |
| Filename encryption | No — filenames are plaintext on disk and on the wire |
| Platforms | Linux + macOS (Phase 1) |

## See also

- [vigocrypt concept](../../concepts/vigocrypt.md) — architecture, threat model, relationship to vigosync
- [vigosync CLI reference](vigosync.md) — the plaintext sibling
