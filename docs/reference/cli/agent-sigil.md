
# vigo sigil CLI Reference

`vigo sigil` is the user-facing interface to [sigil](../../engineering/subsystems/sigil.md), the per-user encrypted P2P directory. It runs inside the `vigo` agent binary, operates on files under the invoking user's home, and does not require root. The agent daemon is never contacted directly by the CLI — `unlock` spawns its own session helper daemon, and `encrypt`/`decrypt` talk to that helper over a local Unix socket.

## Invocation

```
vigo sigil <verb> [args]
```

Every verb runs under the invoking user's uid. No root. The agent daemon does not need to be running for `init`, `enroll`, `status`, or `encrypt` — only for fan-out of the written ciphertext to other envoys.

## Permission model

- `init` and `enroll` write `identity.age` + `identity.salt` + `recipients.txt` with mode 0600 under `~/.sigil/`, owned by the invoking user. The sigil password wraps the age identity with Argon2id — the agent running as root cannot decrypt these files without the password.
- `unlock` spawns the session helper daemon. The helper is `setsid`-detached so it outlives the shell that ran `unlock`. It holds the unwrapped key in its own process memory and serves DECRYPT/STATUS/LOCK over a Unix socket in `$XDG_RUNTIME_DIR/vigo/` (Linux) or `$TMPDIR/sigil-<UID>.sock` (macOS), mode 0600, `SO_PEERCRED`-gated so only the same uid can connect.
- `encrypt` reads `recipients.txt` (public keys only, no secret material) and writes ciphertext into `~/sigil/`. Does not require `unlock` — encryption is a pure pubkey operation.
- `decrypt` sends ciphertext bytes to the running session helper and receives plaintext back. Requires a live helper (i.e. `unlock` has been run).
- `lock` shuts down the helper and scrubs the key. Auto-fires on logout, idle timeout, or explicit invocation.

## Commands

| Verb | Purpose |
|---|---|
| `init` | First-time setup on the primary envoy. Prompts for password (with confirm), generates wrapped identity, creates `~/sigil/`. |
| `enroll` | Per-envoy setup. Normally triggered by the `enroll.pending` sentinel on first `vigo sigil *` invocation; can be run manually for recovery. Idempotent. |
| `unlock` | Prompt password, spawn session helper that holds the unwrapped identity. |
| `lock` | Scrub the held key. Auto-fires on logout and idle timeout. |
| `encrypt <file> [--as <name>]` | Encrypt a plaintext file into `~/sigil/` to every current recipient. |
| `decrypt <file> -o <path>` | Decrypt from `~/sigil/` to a user-chosen path. Requires `unlock`. `-o` is mandatory. |
| `forget <hostname>` | Revoke an envoy from the recipient set; triggers a local `recipients.txt` regeneration. Follow with `reencrypt` to re-key existing files. |
| `reencrypt` | Force re-encrypt of every file in `~/sigil/` to the current recipient set. Requires `unlock`. |
| `status` | Show local state: enrolled, recipient count, file count, session helper status. |

## File layout

```
~/.sigil/
  identity.age     # wrapped age private key (binary: [version][nonce][ciphertext+tag])
  identity.salt    # 16-byte random Argon2id salt
  recipients.txt   # every enrolled envoy's pubkey for this user, one per line
  config.yaml      # user-managed: cross_subnet_peers, idle_timeout_minutes
  enroll.pending   # sentinel dropped by the user executor; removed on first enroll

~/sigil/
  foo.pdf          # age ciphertext at rest
  notes.md         # age ciphertext at rest
```

Plus agent-owned state at `/var/lib/vigo/sigil/<user>/peers.json` (root-owned, 0600). The user never reads this; it's the agent's peer bookkeeping.

## Typical flow

**Primary envoy, one-time:**
```
$ vigo sigil init
sigil password: ****
confirm password:   ****
wrote ~/.sigil/identity.age (0600, wrapped)
wrote ~/.sigil/identity.salt (0600)
wrote ~/.sigil/recipients.txt
created ~/sigil (0700)

public key: age1abc...
```

**Secondary envoy (after usercrate applied with `sigil: true`):**
```
$ ssh dan@laptop.home
$ vigo sigil status       # detects enroll.pending, runs enrollment interactively
sigil password: ****
...
```

**Daily use:**
```
$ vigo sigil encrypt ~/Downloads/tax.pdf
✓ Encrypted to 3 recipient(s); wrote ~/sigil/tax.pdf

$ vigo sigil unlock
sigil password: ****
✓ Unlocked. Session helper running until logout, lock, or 60 min idle.

$ vigo sigil decrypt tax.pdf -o /tmp/tax.pdf
✓ Wrote 847293 bytes to /tmp/tax.pdf

$ vigo sigil lock
✓ Locked. Key scrubbed.
```

**Listing files:**
```
$ vigo sigil list
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
$ vigo sigil delete tax.pdf
✓ Removed /home/dan/sigil/tax.pdf; tombstone will propagate on next watcher cycle.
```

`vigo sigil delete` is equivalent to `rm ~/sigil/<name>` — both trigger the watcher's Remove-event path, which signs a DELETE envelope and fans it out to every peer. Receivers record a tombstone (retention 90 days by default) and remove their local copy. Any future push of the same filename while the tombstone is active is rejected with 410 Gone. To reuse a deleted filename before the tombstone ages out, encrypt under a new name.

**Revoking an envoy:**
```
$ vigo sigil forget laptop.home
✓ Forgot laptop.home. Recipients regenerated.
  Run `vigo sigil reencrypt` to re-encrypt existing files
  so the revoked host cannot decrypt them from its cached ciphertext.

$ vigo sigil unlock
sigil password: ****
$ vigo sigil reencrypt
✓ Re-encrypted 47 file(s) to 2 recipient(s); skipped 0
```

## Configuration

`~/.sigil/config.yaml` is optional:

```yaml
cross_subnet_peers:
  - envoy-london.internal   # cross-subnet peers unreachable via LAN multicast
idle_timeout_minutes: 60    # session helper auto-lock (default 60)
```

## Limits

| Limit | Value |
|---|---|
| Max file size | 1 GB per push (configurable server-side) |
| Content scope | Regular files in top-level `~/sigil/` only |
| Filename encryption | No — filenames are plaintext on disk and on the wire |
| Platforms | Linux + macOS (Phase 1) |

## See also

- [sigil concept](../../engineering/subsystems/sigil.md) — architecture, threat model, relationship to echo
- [Echo](../../engineering/subsystems/echo.md) — the plaintext sibling
