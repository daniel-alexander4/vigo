# vigogo spanner

Manage and inspect hub-spoke spanner status, including spoke health, compliance, envoy reassignment, and spoke draining.

> **Note:** Spanner requires a paid license. The server will not start in hub or spoke mode on the built-in free license.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Show spanner role and spoke health |
| `spokes` | List all spokes |
| `spoke` | Show spoke detail and compliance |
| `spoke-envoys` | List envoys on a spoke |
| `spoke-runs` | List runs on a spoke |
| `spoke-push` | Force push on a spoke |
| `reassign` | Reassign a envoy from one spoke to another |
| `drain` | Drain all envoys from a spoke |

## Usage

```bash
vigogo spanner [subcommand] [flags]
```

## status

Show the spanner role and an overview of all spokes with health, compliance, and latency.

```bash
vigogo spanner status
```

### Example

```bash
vigogo spanner status
```

```
  Role: hub
  Spokes: 3 (3 healthy)
  Total Envoys: 45

  ID      ADDRESS               ENVOYS  COMPLIANT  COMPLIANCE  LATENCY  PATTERNS         STATUS
  east    spoke-east:1530       20       18/20      90.0%       12ms     *.east.*         healthy
  west    spoke-west:1530       15       15/15      100.0%      8ms      *.west.*         healthy
  eu      spoke-eu:1530         10       8/10       80.0%       45ms     *.eu.*           healthy
```

## spokes

List all spokes in a compact table format.

```bash
vigogo spanner spokes
```

### Example output

```
  ID      ADDRESS               ENVOYS  COMPLIANCE  LATENCY  STATUS
  east    spoke-east:1530       20       90.0%       12ms     healthy
  west    spoke-west:1530       15       100.0%      8ms      healthy
  eu      spoke-eu:1530         10       80.0%       45ms     healthy
```

## spoke

Show detailed information for a specific spoke, including compliance breakdown.

```bash
vigogo spanner spoke <spoke-id>
```

### Example

```bash
vigogo spanner spoke east
```

```
  Spoke: east
  Address: spoke-east:1530
  Status: healthy
  Latency: 12ms
  Patterns: *.east.*

  Envoys: 20
  Compliance: 90.0%
    Compliant: 18
    Drifted:   1
    Failing:   1
    Stale:     0
    Unknown:   0
```

## spoke-envoys

List all envoys enrolled on a specific spoke.

```bash
vigogo spanner spoke-envoys <spoke-id>
```

### Example

```bash
vigogo spanner spoke-envoys east
```

```
  ID          HOSTNAME                  IP ADDRESS     REVOKED
  a1b2c3d4    web-01.east.example.com   10.1.1.10
  e5f6g7h8    web-02.east.example.com   10.1.1.11
  i9j0k1l2    db-01.east.example.com    10.1.2.20
```

## spoke-runs

List recent runs on a specific spoke.

```bash
vigogo spanner spoke-runs <spoke-id>
```

### Example

```bash
vigogo spanner spoke-runs east
```

```
  ID          ENVOY      STATUS     MODULES  CHANGED  FAILED
  r1a2b3c4    a1b2c3d4    success    5        2        0
  r5e6f7g8    e5f6g7h8    success    3        0        0
```

## spoke-push

Force push an immediate check-in on a spoke, optionally targeting a specific envoy.

```bash
vigogo spanner spoke-push <spoke-id> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--envoy-id` | | | Target a specific envoy by ID |

### Examples

```bash
vigogo spanner spoke-push east
```

```
  Force push sent to spoke east: 20 envoys notified.
```

```bash
vigogo spanner spoke-push east --envoy-id a1b2c3d4
```

```
  Force push sent to spoke east: 1 envoys notified.
```

## reassign

Reassign a envoy from one spoke to another. The agent reconnects on its next check-in.

```bash
vigogo spanner reassign <envoy-id> --from <spoke-id> --to <spoke-id>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--from` | | | Source spoke ID (required) |
| `--to` | | | Target spoke ID (required) |

### Example

```bash
vigogo spanner reassign a1b2c3d4 --from east --to west
```

```
  Envoy a1b2c3d4 reassigned.
  New ID:     x9y8z7w6
  Target:     spoke-west:1530
  The agent will reconnect on its next check-in.
```

## drain

Drain all envoys from a spoke, redistributing them to another spoke or auto-routing by pattern.

```bash
vigogo spanner drain <spoke-id> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--to` | | | Target spoke ID (optional, auto-route by pattern if empty) |

### Examples

#### Drain to a specific spoke

```bash
vigogo spanner drain east --to west
```

```
  Spoke east drained: 20 moved, 0 failed.
```

#### Drain with auto-routing

```bash
vigogo spanner drain east
```

```
  Spoke east drained: 18 moved, 2 failed.
  Error: envoy a1b2c3d4 has no matching spoke pattern
  Error: envoy e5f6g7h8 has no matching spoke pattern
```

## Related

- [envoys](envoys.md) -- manage enrolled envoys
- [push](push.md) -- push to local envoys
