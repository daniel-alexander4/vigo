
# vigocli filesync

Read-only admin view of the [filesync](../../engineering/subsystems/filesync.md) mesh across the fleet — per-user plaintext directory sync on the [swarm](../../engineering/subsystems/swarm.md) substrate. Every envoy reports its per-user filesync state (files, tombstones, peer digest) through the `filesync` trait; this command renders the fleet-wide join.

Filesync membership is per-user: users opt in by dropping files into `~<user>/filesync/`. There is no admin write path — `vigocli swarm filesync` never touches files, tombstones, or decides what syncs where.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Fleet-wide totals (users, envoys, files, tombstones, divergences) |
| `list` | One row per user across the fleet |
| `show` | Detailed per-envoy presence for one user |

All subcommands support `--json` for machine-readable output.

## status

```bash
vigocli filesync status
```

```
Filesync fleet — generated 2026-04-23 18:12:04

Users              3
Envoys reporting   7
Files              42
Tombstones         5
Divergences        0
Participants       7
```

`Divergences` is shown in yellow when non-zero — a user whose file set differs across envoys reporting for them.

## list

```bash
vigocli filesync list
```

```
USER   ENVOYS  FILES  SIZE    STATE
alice  3       18     3.2 MB  in sync
bob    2       9      540 KB  in sync
dan    4       15     1.1 MB  2 divergence(s)
```

| Column | Description |
|--------|-------------|
| USER | Unix username reported by the envoys |
| ENVOYS | Number of envoys where this user has an filesync directory |
| FILES | Total file count across all envoys (may exceed per-envoy count if divergent) |
| SIZE | Sum of live file sizes |
| STATE | `in sync` (green) or `N divergence(s)` (red) |

## show

```bash
vigocli filesync show <user>
```

```bash
vigocli filesync show dan
```

```
User:        dan
Envoys:      4
Files:       15
Total size:  1.1 MB
Divergences: notes.md

  HOST                   STATUS      FILES  TOMBSTONES  LAST UPDATE
  danlap                 reporting   15     5           2026-04-23 18:10
  girlslaptop            reporting   14     5           2026-04-23 18:09
  homeserver             stale       15     5           2026-04-22 21:17
  plex                   reporting   15     5           2026-04-23 18:11
```

Per-envoy states:

| Status | Meaning |
|--------|---------|
| `reporting` | Envoy's most recent check-in carried a fresh `filesync` trait payload for this user |
| `stale` | Last check-in is older than the staleness threshold — presence counts may be behind |
| `not_reporting` | Envoy is enrolled and reachable but emitted no filesync trait (user may have been removed from the envoy) |

## Related

- [Filesync Subsystem](../../engineering/subsystems/filesync.md) — How the plaintext per-user sync works
- [Swarm Substrate](../../engineering/subsystems/swarm.md) — The envoy P2P transport filesync rides on
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
