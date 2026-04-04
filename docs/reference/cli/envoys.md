
# vigocli envoys

Manage enrolled envoys (managed nodes). Also available as `vigocli nodes`.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all enrolled envoys |
| `show` | Show details for a envoy |
| `revoke` | Revoke an enrolled envoy |
| `delete` | Permanently delete a envoy and all its history |
| `traits` | View collected traits for a envoy |
| `tag` | Set tags on a envoy |
| `untag` | Remove specific tags from a envoy |
| `maintenance` | Set or clear maintenance mode |
| `purge` | Delete stale duplicate envoys, keeping only the most recently seen per hostname |
| `push` | Force immediate check-in and convergence |
| `rebootstrap` | Signal envoys to download the latest agent binary and restart |

## Usage

```bash
vigocli envoys [subcommand] [flags]
```

## list

List active (non-revoked) envoys with their status. Pass `--all` to include revoked envoys.

```bash
vigocli envoys list
vigocli envoys list --all   # include revoked
```

| Flag | Description |
|------|-------------|
| `--all` | Include revoked envoys in the output |

### Example output

```
ID          HOSTNAME              IP             ENROLLED           LAST SEEN          STATUS
a1b2c3d4    web-01.example.com    10.0.1.10      2026-03-01 08:00   2026-03-13 12:30   active
e5f6g7h8    db-01.internal.net    10.0.2.20      2026-03-02 09:15   2026-03-13 12:28   active
```

## show

Show detailed information for a specific envoy, including compliance status and pending secret rotations. Accepts a full UUID, short UUID prefix, or hostname.

```bash
vigocli envoys show <id-or-hostname>
```

### Example

```bash
vigocli envoys show web-01.example.com
vigocli envoys show a1b2c3d4
```

```
ID:         a1b2c3d4
Hostname:   web-01.example.com
IP:         10.0.1.10
Enrolled:   2026-03-01 08:00
Last Seen:  2026-03-13 12:30
Status:     active
Compliance: compliant
```

## revoke

Revoke an enrolled envoy. The envoy will receive an error on its next check-in and cannot re-enroll with the same UUID.

```bash
vigocli envoys revoke <id>
```

### Example

```bash
vigocli envoys revoke a1b2c3d4
```

```
Revoked envoy a1b2c3d4
```

## delete

Permanently delete a envoy and all its associated history.

```bash
vigocli envoys delete <id>
```

### Example

```bash
vigocli envoys delete a1b2c3d4
```

```
Deleted envoy a1b2c3d4
```

## traits

View collected traits for a envoy by hostname. Optionally drill into a specific trait using a dot-separated path.

```bash
vigocli envoys traits <hostname> [dot.path]
```

### Examples

#### View all traits


```bash
vigocli envoys traits web-01.example.com
```

```json
{
  "hostname": "web-01.example.com",
  "collected_at": "2026-03-13T12:30:00Z",
  "traits": {
    "os": {
      "distro": "ubuntu",
      "version": "24.04",
      "family": "debian"
    },
    "memory": {
      "total": 8589934592,
      "percent": 62.5
    }
  }
}
```

#### View a specific trait

```bash
vigocli envoys traits web-01.example.com os.distro
```

```
ubuntu
```

## tag

Set tags on a envoy for targeting.

```bash
vigocli envoys tag <id> <tag1,tag2,...>
```

### Example

```bash
vigocli envoys tag a1b2c3d4 webserver,production
```

```
Tags set: webserver, production
```

Tags replace all existing tags. To add to existing tags, include the old ones.

## untag

Remove specific tags from a envoy.

```bash
vigocli envoys untag <id> <tag1,tag2,...>
```


### Example

```bash
vigocli envoys untag a1b2c3d4 staging
```

```
Tags updated: webserver, production
```

## maintenance

Set or clear maintenance mode for a envoy. While in maintenance, the envoy receives an empty policy on check-in (skips convergence) but still reports traits.

```bash
vigocli envoys maintenance <id> --until <RFC3339>
vigocli envoys maintenance <id> --clear
```

### Flags

| Flag | Description |
|------|-------------|
| `--until` | Maintenance window end time (RFC3339 format) |
| `--clear` | Clear maintenance mode |

### Example

```bash
# Set maintenance until 6 AM tomorrow
vigocli envoys maintenance a1b2c3d4 --until 2026-03-14T06:00:00Z

# Clear maintenance
vigocli envoys maintenance a1b2c3d4 --clear
```

## purge

Delete stale duplicate envoys. When a machine is rebuilt and re-enrolled with the same hostname, the old entry remains with a different UUID. `purge` finds all hostnames with multiple entries and deletes all but the most recently seen one.

Revoked entries always lose to active ones. Among entries with the same status, the most recent `last_seen` timestamp wins.

```bash
vigocli envoys purge
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Preview duplicates without deleting |

### Example

```bash
# Preview what would be deleted
vigocli envoys purge --dry-run

# Purge stale duplicates (prompts for confirmation)
vigocli envoys purge
```

```
ID          HOSTNAME              IP           LAST SEEN         STATUS
e6ff73f8    raspberry             192.168.1.78 2026-03-15 12:17  active
a1b2c3d4    raspberry             192.168.1.78 2026-03-14 09:30  revoked

2 duplicate envoy(s) will be deleted.
Purge these envoys? [y/N] y
  deleted e6ff73f8 (raspberry)
  deleted a1b2c3d4 (raspberry)
Purged 2 stale envoy(s).
```

## push

Force a envoy to check in and converge immediately, without waiting for its next polling interval. Sets a force-update flag on the server that the agent picks up on its next poll.

```bash
# Push a single envoy
vigocli envoys push <hostname-or-uuid>

# Push all active envoys
vigocli envoys push --all
```

### Flags

| Flag | Description |
|------|-------------|
| `--all` | Force check-in for all active envoys |

### Example

```bash
vigocli envoys push web-01.example.com
```

```
OK Force update queued for web-01.example.com
```

## rebootstrap

Signal envoys to download the latest agent binary and restart on their next check-in. No SSH required — the server sets a `force_update` flag that the agent picks up during its normal polling cycle.

```bash
# Update specific envoys
vigocli envoys rebootstrap plex raspberry

# Update all active envoys
vigocli envoys rebootstrap --all
```

| Flag | Description |
|------|-------------|
| `--all` | Signal every active envoy to update |

The agent downloads the latest binary from the server's `/api/v1/agent/download` endpoint, verifies the SHA256 checksum, atomically replaces its own binary, and exec-restarts. On systemd, this is seamless — the service continues running with the new binary.

## Related

- [approved](approved.md) -- manage enrollment patterns
- [runs](runs.md) -- view run history for envoys
- [inventory](inventory.md) -- query envoys by traits
- [task](task.md) -- dispatch ad-hoc commands to envoys
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
