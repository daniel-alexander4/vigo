
# vigogo server

Manage peer replication: promote a peer to primary, check replication status.

## Subcommands

### promote

Promote a peer server to primary role.

```bash
# Promote the server at the configured --server address:
vigogo server promote

# Promote a specific server:
vigogo server promote --target peer1.example.com:8443
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--target` | (uses `--server`) | REST API address of the server to promote |

**Output:**

```
  Promoted to primary.
  promoted from peer to primary
```

If promotion is denied due to split-brain resolution:

```
  Promotion failed: split-brain: peer hub.example.com:1530 is primary with config version 12 (local: 10); promotion denied
```

### status

Show the current peer replication status.

```bash
vigogo server status
```

**Output (primary):**

```
  Role:           primary
  Last sync:      2026-03-16T10:30:00Z
  Files synced:   47
  Config version: 12
```

**Output (peer):**

```
  Role:           peer
  Primary:        primary.example.com:1530
  Last sync:      2026-03-16T10:30:15Z
  Files synced:   47
  Config version: 12
```

## See Also

- [Peer Replication](../../operations/peer-replication.md) -- full operational guide
