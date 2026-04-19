
# Vigosync — Per-User LAN File Sync

Vigosync keeps `~/vigosync/` in sync across every machine where a user has an account, on the same LAN. Drop a file on any machine and it appears on all the others. Delete a file and it disappears from all of them. No config, no commands, no server involvement.

## How It Works

Vigosync is a **pure peer-to-peer LAN feature**. The Vigo server plays no role at all. Envoys discover each other via UDP multicast and exchange files directly over mTLS.

1. **Scan.** Every 30 seconds each agent scans `~/vigosync/` for every local user. Files are hashed (SHA-256), and the in-memory state (file list + deletion tombstones) is compared against the previous snapshot. Added files show up in the new state; deleted files get a tombstone stamped with the current time.
2. **Announce.** Each agent multicasts its full per-user state on `224.0.0.43:1532`. One announcement per user per cycle. Everything on the wire is LAN-local (TTL=1) and mTLS-authenticated.
3. **Reconcile.** Peers receive the announcement and compare it against their own state for the same user. For each entry:
    - **Peer has a file we don't** → fetch it.
    - **Peer has a newer version** (higher mtime) → fetch it.
    - **Peer has a tombstone we don't, and our file's mtime is older than the deletion time** → delete the file.
    - **Peer has a tombstone but our file is newer than the deletion time** → keep the file (it was re-added after the delete) and our next announce will propagate it back.
4. **Transfer.** Files are fetched over HTTPS from the sender's swarm peer port (1531) via a dedicated `/vigosync/{user}/{filename}` route. Content is verified against the announced SHA-256 before being written to the destination.
5. **Apply.** The receiving agent writes the file to `~/{user}/vigosync/{filename}` with correct ownership (`chown user:user`) and preserved modification time.

## Behavior

| Action | Behavior |
|--------|----------|
| Add a file | Synced to other machines within ~60s |
| Update a file | Newer mtime wins, overwrites on other machines |
| Delete a file | Tombstone propagates, file disappears on other machines within ~60s |
| Re-add a deleted file | Newer mtime beats the tombstone, file reappears everywhere |
| Conflicting edits on two machines | Last-write-wins by mtime (trust the OS clock — run NTP) |

## Activation

No configuration. Vigosync activates automatically when:

1. The agent is running and has enrollment certificates (mTLS)
2. A user has created `~/vigosync/` in their home directory

The directory is **not** created automatically — users opt in by creating it.

## State Storage

The agent keeps per-user state (file snapshots + tombstones) under `/var/lib/vigo/vigosync/<user>.json`. This survives agent restarts. **Tombstones are garbage-collected after 30 days** — after that, a long-offline machine that still has the file will resurrect it on rejoin. If this matters to you, don't let machines stay offline for more than a month.

## Limits

| Limit | Value |
|-------|-------|
| Max file size | 2 GB per file |
| Supported content | Regular files in the top-level directory only (no subdirectories, no symlinks) |
| Dotfiles | Skipped (files starting with `.`) |
| Scope | LAN only (TTL=1 multicast, no cross-subnet routing) |
| Machine identity | Hostname — if two machines share a hostname, vigosync will self-confuse |

## Platform Support

| Platform | Home Directory | Ownership |
|----------|---------------|-----------|
| Linux | `/home/{user}/vigosync/` | `chown user:user` |
| macOS | `/Users/{user}/vigosync/` | `chown user:user` |
| Windows | `C:\Users\{user}\vigosync\` | (untested) |

## Security

- All transport is mTLS, same bootstrap certificates as the swarm peer server
- Files are stored plaintext on disk by default — see [Encryption](#encryption) for opt-in at-rest encryption
- The vigosync HTTP route canonicalizes paths and rejects anything that resolves outside `~/{user}/vigosync/` to prevent symlink escape
- Filenames are validated: no `/`, `\`, `\0`, `.`, `..`, or leading `.`
- Users can only sync files into their own home directory on other machines

## Encryption

Opt-in per-user via `~/.vigosync/config.yaml`:

```yaml
encrypt: true
extra_peers:
  - offsite-box.example.com   # optional, see Cross-subnet receivers below
```

When `encrypt: true`, the agent encrypts every file in `~/vigosync/` in place with [age](https://age-encryption.org) before hashing and announcing. Every machine — including the sender — then holds ciphertext. Peers that don't hold the user's age identity store opaque ciphertext and function as dark backup nodes; root on those machines cannot read content.

### One-time setup

On the user's main machine:

```
$ vigo vigosync init
wrote ~/.vigosync/identity.txt (0600)
wrote ~/.vigosync/recipients.txt

public key: age1qz3...x7y

Keep identity.txt private. To add another device that should also be able
to decrypt, run `vigo vigosync init` on that device and use `vigo vigosync
recipients add <pubkey>` on the first machine to grant access, then run
`vigo vigosync reencrypt`.
```

### Reading encrypted files

```
$ vigo vigosync decrypt ~/vigosync/trip.jpg -o /tmp/trip.jpg
# or pipe to stdout
$ vigo vigosync decrypt ~/vigosync/notes.md
```

### Key rotation

Edit `~/.vigosync/recipients.txt` (or use `vigo vigosync recipients add|remove <pubkey>`), then run `vigo vigosync reencrypt`. The command walks the user's `vigosync/` dir, decrypts with the current identity, and re-encrypts to the new recipient set. Old ciphertext blobs retire through vigosync's normal LWW path on the next scan cycle.

### Threat model

- **Defends against**: root on receivers that don't hold `identity.txt`; physical theft of a dark-receiver machine.
- **Does NOT defend against**: root on any machine that holds `identity.txt` (root can read 0600 files); compromise of key material after generation; metadata leakage (filenames, sizes, mtimes remain visible to dark receivers).
- Filename encryption is intentionally not in v1 — it would complicate delta sync and match behavior is familiar from `git-crypt` / `rclone crypt` (without name encryption). File contents are encrypted; file names are not.

### Fail-closed posture

If `encrypt: true` is set but `~/.vigosync/recipients.txt` is missing, unreadable, or contains no valid recipients, the agent skips the user's entire scan cycle for that tick. No files are touched, no announcement is sent. The agent log records the reason. This rule is absolute: the daemon never falls through to plaintext sync when encryption is configured.

## Cross-subnet receivers

By default vigosync is LAN-only (multicast TTL=1). To reach a peer on a different subnet, list it in `extra_peers:`:

```yaml
# ~/.vigosync/config.yaml
encrypt: true
extra_peers:
  - offsite-box.example.com
```

The sender POSTs each scan cycle's announcement directly to `https://offsite-box.example.com:1531/vigosync/announce` over mTLS. The receiver feeds the announcement into the same reconciler path multicast uses — no transport changes, no server involvement. Combine with `encrypt: true` if the remote site should be a dark backup.

Unreachable extra_peers log a debug-level error and retry next cycle; transient outages don't stall the scan loop.

## What Vigosync Does NOT Do

- No subdirectory sync (only flat files at the top level)
- No cross-LAN sync via multicast (TTL=1 by design). Use `extra_peers:` for explicit cross-subnet targets.
- No server-side state, no API, no web UI
- No conflict resolution beyond last-write-wins
- No versioning or history
- No clock-skew correction — bad clocks mean bad sync
- No filename encryption — enable `encrypt: true` and file contents become ciphertext, but filenames remain visible to receivers

## Troubleshooting

If files aren't syncing between two machines:

1. **Same LAN?** Vigosync won't traverse routers. Check that both envoys can multicast to each other — `tcpdump -i <iface> host 224.0.0.43` should show announcements from the peer.
2. **Same user account?** Both machines need a user with the exact same username. Syncing happens per-user.
3. **`~/vigosync/` exists on both?** A user without the directory doesn't participate.
4. **Clocks synced?** Last-write-wins trusts mtimes — badly skewed clocks will cause files to ping-pong or losers to appear.
5. **mTLS healthy?** Vigosync uses the same enrollment certs as the swarm peer server. If `curl https://<peer>:1531/health` fails from one machine to another, so does vigosync.
6. **Check `/var/lib/vigo/vigosync/<user>.json`** for the current snapshot and tombstone log.

## Related

- [Swarm](swarm.md) — Infrastructure-level P2P blob distribution (operator-controlled, server-orchestrated)
- [Swarm Operations](../operations/swarm.md) — Swarm configuration and troubleshooting
- [Vigorepo](vigorepo.md) — Personal-DR git mirroring (sister subsystem; server-coordinated mirror selection, hybrid bare-repo + working-tree layout on receivers)
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
