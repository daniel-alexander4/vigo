
# Echo — Per-User LAN File Sync

Echo keeps `~/echo/` in sync across every machine where a user has an account, on the same LAN. Drop a file on any machine and it appears on all the others. Delete a file and it disappears from all of them. No config, no commands, no server involvement.

## How It Works

Echo is a **pure peer-to-peer LAN feature**. The Vigo server plays no role at all. Envoys discover each other via UDP multicast and exchange files directly over mTLS.

1. **Scan.** Every 30 seconds each agent scans `~/echo/` for every local user. Files are hashed (SHA-256), and the in-memory state (file list + deletion tombstones) is compared against the previous snapshot. Added files show up in the new state; deleted files get a tombstone stamped with the current time.
2. **Announce.** Each agent multicasts its full per-user state on `224.0.0.43:1532`. One announcement per user per cycle. Everything on the wire is LAN-local (TTL=1) and mTLS-authenticated.
3. **Reconcile.** Peers receive the announcement and compare it against their own state for the same user. For each entry:
    - **Peer has a file we don't** → fetch it.
    - **Peer has a newer version** (higher mtime) → fetch it.
    - **Peer has a tombstone we don't, and our file's mtime is older than the deletion time** → delete the file.
    - **Peer has a tombstone but our file is newer than the deletion time** → keep the file (it was re-added after the delete) and our next announce will propagate it back.
4. **Transfer.** Files are fetched over HTTPS from the sender's swarm peer port (1531) via a dedicated `/echo/{user}/{filename}` route. Content is verified against the announced SHA-256 before being written to the destination.
5. **Apply.** The receiving agent writes the file to `~/{user}/echo/{filename}` with correct ownership (`chown user:user`) and preserved modification time.

## Behavior

| Action | Behavior |
|--------|----------|
| Add a file | Synced to other machines within ~60s |
| Update a file | Newer mtime wins, overwrites on other machines |
| Delete a file | Tombstone propagates, file disappears on other machines within ~60s |
| Re-add a deleted file | Newer mtime beats the tombstone, file reappears everywhere |
| Conflicting edits on two machines | Last-write-wins by mtime (trust the OS clock — run NTP) |

## Activation

No configuration. Echo activates automatically when:

1. The agent is running and has enrollment certificates (mTLS)
2. A user has created `~/echo/` in their home directory

The directory is **not** created automatically — users opt in by creating it.

## State Storage

The agent keeps per-user state (file snapshots + tombstones) under `/var/lib/vigo/echo/<user>.json`. This survives agent restarts. **Tombstones are garbage-collected after 30 days** — after that, a long-offline machine that still has the file will resurrect it on rejoin. If this matters to you, don't let machines stay offline for more than a month.

## Limits

| Limit | Value |
|-------|-------|
| Max file size | 2 GB per file |
| Supported content | Regular files in the top-level directory only (no subdirectories, no symlinks) |
| Dotfiles | Skipped (files starting with `.`) |
| Scope | LAN only (TTL=1 multicast, no cross-subnet routing) |
| Machine identity | Hostname — if two machines share a hostname, echo will self-confuse |

## Platform Support

| Platform | Home Directory | Ownership |
|----------|---------------|-----------|
| Linux | `/home/{user}/echo/` | `chown user:user` |
| macOS | `/Users/{user}/echo/` | `chown user:user` |
| Windows | `C:\Users\{user}\echo\` | (untested) |

## Security

- All transport is mTLS, same bootstrap certificates as the swarm peer server
- Files are stored **plaintext on disk**. Echo is LAN-trusted sync; anyone with shell on a participating machine sees the contents.
- The echo HTTP route canonicalizes paths and rejects anything that resolves outside `~/{user}/echo/` to prevent symlink escape
- Filenames are validated: no `/`, `\`, `\0`, `.`, `..`, or leading `.`
- Users can only sync files into their own home directory on other machines

**For encrypted sync**, use [sigil](sigil.md) — the sibling subsystem that keeps ciphertext at rest on every envoy and gates decryption behind a per-user password-wrapped age identity. Echo is deliberately unencrypted now; the 0.25.0 `encrypt: true` opt-in was removed in 0.26.1 because maintaining two overlapping encryption models confused the threat-model story.

### Migrating from echo 0.25.0 encryption

If you adopted `encrypt: true` in 0.25.0 and want encrypted sync going forward:

1. Decrypt existing ciphertext files with the age CLI directly: `age -d -i ~/.echo/identity.txt ~/echo/foo > /tmp/foo && mv /tmp/foo ~/echo/foo`. Vigo no longer ships `vigo echo decrypt` — use upstream age.
2. Enroll the user in sigil via `sigil: true` on the user resource, or run `vigo crypt init` on the primary + `vigo crypt enroll` on every secondary.
3. Move the plaintext files from `~/echo/` to `~/sigil/` via `vigo crypt encrypt`.
4. Delete the leftover `~/.echo/identity.txt` and `~/.echo/recipients.txt` — they're no longer read.

## Cross-subnet receivers

By default echo is LAN-only (multicast TTL=1). To reach a peer on a different subnet, list it in `~/.echo/config.yaml`:

```yaml
cross_subnet_peers:
  - offsite-box.example.com
```

The sender POSTs each scan cycle's announcement directly to `https://offsite-box.example.com:1531/echo/announce` over mTLS. The receiver feeds the announcement into the same reconciler path multicast uses — no transport changes, no server involvement.

Unreachable cross_subnet_peers log a debug-level error and retry next cycle; transient outages don't stall the scan loop.

## What Echo Does NOT Do

- No subdirectory sync (only flat files at the top level)
- No cross-LAN sync via multicast (TTL=1 by design). Use `cross_subnet_peers:` for explicit cross-subnet targets.
- No server-side state, no API, no web UI
- No conflict resolution beyond last-write-wins
- No versioning or history
- No clock-skew correction — bad clocks mean bad sync
- No encryption. Use [sigil](sigil.md) if you need encrypted sync.

## Observability (`/echo` UI)

Every envoy with per-user echo state on disk ships a `echo` trait containing the file set (sha + size + mtime), tombstones, and a stable 16-byte digest over the full-file-set. The server-side aggregator (`server/echomesh/`) joins these across the fleet for the `/echo` page.

The `/echo`, `/sigil`, `/cairn` pages and every mesh endpoint (`/api/v1/echo/*`, `/api/v1/sigil/*`, `/api/v1/cairn/fleet`, and the UI fragment drill-downs) are admin-only — fleet-wide per-user file lists aren't viewer-appropriate. Viewers get a 403 on direct access and don't see the nav links. The legacy `/mesh` URL 301-redirects to `/echo` (or `/sigil` with `?tab=crypt`) for one release while muscle memory migrates.

Surfaces:

- **Presence matrix** — per (user × envoy × filename) live / tombstoned / missing / divergent (mismatched shas)
- **Three-state envoy reporting** — reporting / stale (>5 min) / not_reporting
- **Drill-down endpoint** — `GET /api/v1/echo/envoy/{id}/users/{user}/files` for the unbounded file list when an operator clicks through.

Echo is LAN-only plaintext, so there's no signing-key or encryption-state signal to surface (unlike sigil).

## Troubleshooting

If files aren't syncing between two machines:

1. **Same LAN?** Echo won't traverse routers. Check that both envoys can multicast to each other — `tcpdump -i <iface> host 224.0.0.43` should show announcements from the peer.
2. **Same user account?** Both machines need a user with the exact same username. Syncing happens per-user.
3. **`~/echo/` exists on both?** A user without the directory doesn't participate.
4. **Clocks synced?** Last-write-wins trusts mtimes — badly skewed clocks will cause files to ping-pong or losers to appear.
5. **mTLS healthy?** Echo uses the same enrollment certs as the swarm peer server. If `curl https://<peer>:1531/health` fails from one machine to another, so does echo.
6. **Check `/var/lib/vigo/echo/<user>.json`** for the current snapshot and tombstone log.

## Related

- [Swarm](swarm.md) — Infrastructure-level P2P blob distribution (operator-controlled, server-orchestrated)
- [Swarm Operations](../../operations/swarm.md) — Swarm configuration and troubleshooting
- [Cairn](cairn.md) — Personal-DR git mirroring (sister subsystem; server-coordinated mirror selection, hybrid bare-repo + working-tree layout on receivers)
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
