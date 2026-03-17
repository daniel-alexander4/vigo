# hobnobcli thralls

Manage enrolled thralls (managed nodes). Also available as `hobnobcli nodes`.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | List all enrolled thralls |
| `show` | Show details for a thrall |
| `revoke` | Revoke an enrolled thrall |
| `delete` | Permanently delete a thrall and all its history |
| `traits` | View collected traits for a thrall |
| `tag` | Set tags on a thrall |
| `untag` | Remove specific tags from a thrall |
| `maintenance` | Set or clear maintenance mode |
| `purge` | Delete stale duplicate thralls, keeping only the most recently seen per hostname |
| `rebootstrap` | Signal thralls to download the latest agent binary and restart |

## Usage

```bash
hobnobcli thralls [subcommand] [flags]
```

## list

List active (non-revoked) thralls with their status. Pass `--all` to include revoked thralls.

```bash
hobnobcli thralls list
hobnobcli thralls list --all   # include revoked
```

| Flag | Description |
|------|-------------|
| `--all` | Include revoked thralls in the output |

### Example output

```
ID          HOSTNAME              IP             ENROLLED           LAST SEEN          STATUS
a1b2c3d4    web-01.example.com    10.0.1.10      2026-03-01 08:00   2026-03-13 12:30   active
e5f6g7h8    db-01.internal.net    10.0.2.20      2026-03-02 09:15   2026-03-13 12:28   active
```

## show

Show detailed information for a specific thrall, including compliance status and pending secret rotations. Accepts a full UUID, short UUID prefix, or hostname.

```bash
hobnobcli thralls show <id-or-hostname>
```

### Example

```bash
hobnobcli thralls show web-01.example.com
hobnobcli thralls show a1b2c3d4
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

Revoke an enrolled thrall. The thrall will receive an error on its next check-in and cannot re-enroll with the same UUID.

```bash
hobnobcli thralls revoke <id>
```

### Example

```bash
hobnobcli thralls revoke a1b2c3d4
```

```
Revoked thrall a1b2c3d4
```

## delete

Permanently delete a thrall and all its associated history.

```bash
hobnobcli thralls delete <id>
```

### Example

```bash
hobnobcli thralls delete a1b2c3d4
```

```
Deleted thrall a1b2c3d4
```

## traits

View collected traits for a thrall by hostname. Optionally drill into a specific trait using a dot-separated path.

```bash
hobnobcli thralls traits <hostname> [dot.path]
```

### Examples

#### View all traits

```bash
hobnobcli thralls traits web-01.example.com
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
hobnobcli thralls traits web-01.example.com os.distro
```

```
ubuntu
```

## tag

Set tags on a thrall for targeting.

```bash
hobnobcli thralls tag <id> <tag1,tag2,...>
```

### Example

```bash
hobnobcli thralls tag a1b2c3d4 webserver,production
```

```
Tags set: webserver, production
```

Tags replace all existing tags. To add to existing tags, include the old ones.

## untag

Remove specific tags from a thrall.

```bash
hobnobcli thralls untag <id> <tag1,tag2,...>
```

### Example

```bash
hobnobcli thralls untag a1b2c3d4 staging
```

```
Tags updated: webserver, production
```

## maintenance

Set or clear maintenance mode for a thrall. While in maintenance, the thrall receives an empty policy on check-in (skips convergence) but still reports traits.

```bash
hobnobcli thralls maintenance <id> --until <RFC3339>
hobnobcli thralls maintenance <id> --clear
```

### Flags

| Flag | Description |
|------|-------------|
| `--until` | Maintenance window end time (RFC3339 format) |
| `--clear` | Clear maintenance mode |

### Example

```bash
# Set maintenance until 6 AM tomorrow
hobnobcli thralls maintenance a1b2c3d4 --until 2026-03-14T06:00:00Z

# Clear maintenance
hobnobcli thralls maintenance a1b2c3d4 --clear
```

## purge

Delete stale duplicate thralls. When a machine is rebuilt and re-enrolled with the same hostname, the old entry remains with a different UUID. `purge` finds all hostnames with multiple entries and deletes all but the most recently seen one.

Revoked entries always lose to active ones. Among entries with the same status, the most recent `last_seen` timestamp wins.

```bash
hobnobcli thralls purge
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Preview duplicates without deleting |

### Example

```bash
# Preview what would be deleted
hobnobcli thralls purge --dry-run

# Purge stale duplicates (prompts for confirmation)
hobnobcli thralls purge
```

```
ID          HOSTNAME              IP           LAST SEEN         STATUS
e6ff73f8    raspberry             192.168.1.78 2026-03-15 12:17  active
a1b2c3d4    raspberry             192.168.1.78 2026-03-14 09:30  revoked

2 duplicate thrall(s) will be deleted.
Purge these thralls? [y/N] y
  deleted e6ff73f8 (raspberry)
  deleted a1b2c3d4 (raspberry)
Purged 2 stale thrall(s).
```

## rebootstrap

Signal thralls to download the latest agent binary and restart on their next check-in. No SSH required — the server sets a `force_update` flag that the agent picks up during its normal polling cycle.

```bash
# Update specific thralls
hobnobcli thralls rebootstrap plex raspberry

# Update all active thralls
hobnobcli thralls rebootstrap --all
```

| Flag | Description |
|------|-------------|
| `--all` | Signal every active thrall to update |

The agent downloads the latest binary from the server's `/api/v1/agent/download` endpoint, verifies the SHA256 checksum, atomically replaces its own binary, and exec-restarts. On systemd, this is seamless — the service continues running with the new binary.

## Related

- [approved](approved.md) -- manage enrollment patterns
- [runs](runs.md) -- view run history for thralls
- [inventory](inventory.md) -- query thralls by traits
- [push](push.md) -- trigger immediate check-in
