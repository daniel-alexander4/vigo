
# Vigosync — Per-User File Sync

Vigosync syncs files across machines for individual users. Drop a file in `~/vigosync/` on any machine — it appears in `~/vigosync/` on every other machine where that user has an account. No config, no commands, no manifests.

## How It Works

1. **Detection:** The agent scans `~/vigosync/` for every user on each check-in cycle. Files are hashed (SHA256) and reported to the server via the `vigosync` trait.

2. **Targeting:** The server checks which other envoys have the same username (via the `users.human` trait). Only machines where the user has an account receive the file.

3. **Delivery:** Files are delivered via the same swarm P2P transport used for blob distribution — mTLS HTTPS between agents, chunked, integrity-verified.

4. **Placement:** The receiving agent writes the file to `~/vigosync/{filename}` with the correct user ownership and preserved modification time.

## Behavior

| Action | Behavior |
|--------|----------|
| Add a file | Synced to other machines within one check-in cycle (~5-15s) |
| Update a file | Updated copy overwrites on other machines (last-write-wins by mtime) |
| Delete a file | Local only — other machines keep their copy |

## Activation

No configuration needed. Vigosync activates automatically when:

1. The agent is enrolled and running
2. A user creates `~/vigosync/` in their home directory

The directory is NOT created automatically — users opt in by creating it.

## Limits

| Limit | Value |
|-------|-------|
| Max file size | 100 MB per file |
| Supported content | Files only (no subdirectories, no symlinks) |
| Dotfiles | Skipped (files starting with `.`) |

## Platform Support

| Platform | Home Directory | Ownership |
|----------|---------------|-----------|
| Linux | `/home/{user}/vigosync/` | `chown user:user` |
| macOS | `/Users/{user}/vigosync/` | `chown user:user` |
| Windows | `C:\Users\{user}\vigosync\` | User profile ACLs |

## Security

- All transport is mTLS (same as swarm P2P)
- Files are stored plaintext on disk (no at-rest encryption)
- Any user who can write to `~/vigosync/` can share files to their own accounts on other machines
- Users cannot sync files to other users' directories

## What Vigosync Does NOT Do

- No directory sync (files only, not subdirectories)
- No delete propagation (local delete stays local)
- No versioning or history (overwritten files are gone)
- No access control beyond Unix permissions
- No web UI (filesystem interface only)

## Related

- [Swarm](swarm.md) — Infrastructure-level P2P blob distribution (operator-controlled)
- [Swarm Operations](../operations/swarm.md) — Swarm configuration and troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
