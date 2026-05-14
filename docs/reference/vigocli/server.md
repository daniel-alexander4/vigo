
# vigocli server

Manage peer replication: promote a peer to primary, check replication status.

## Subcommands

### server.yaml

Annotated `server.yaml` reference. Has subcommands: `full`, `list`.

#### server.yaml full

Print the full annotated `server.yaml` with all options, defaults, and descriptions.

```bash
vigocli server server.yaml full
vigocli server server.yaml full > server.yaml
```

#### server.yaml list

List all configuration sections, or show a specific section.

```bash
vigocli server server.yaml list
vigocli server server.yaml list smtp
vigocli server server.yaml list ai
```

### promote

Promote a peer server to primary role.

```bash
# Promote the server at the configured --server address:
vigocli server promote

# Promote a specific server:
vigocli server promote --target peer1.example.com:8443
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
vigocli server status
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

- [Peer Replication](../../howto/troubleshoot.md) -- full operational guide
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/license.md).*
