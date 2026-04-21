
# vigocli repo

Admin-side, fleet-wide view of the [vigorepo](../../engineering/subsystems/vigorepo.md) personal-DR mesh. Every envoy's publisher-side source list and receiver-side mirror holdings are joined server-side by `server/vigorepomesh` and surfaced through `GET /api/v1/vigorepo/fleet`; this command is the CLI consumer of that endpoint.

`vigocli repo` is **read-only**. Recovery runs on the envoy holding the mirror, not from the admin host — use the envoy-local [`vigo repo`](vigorepo.md) CLI for that. Publish, revoke, and seed distribution live in [`vigocli swarm`](swarm.md).

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Fleet-wide vigorepo totals (publishers, labels, mirrors, orphans, stale) |
| `list` | One row per vigorepo label with publisher, mirror counts, bundle type, size |
| `show <label\|project>` | Detailed view of one label with per-host mirror grid |

Every subcommand accepts `--json` to emit machine-readable output instead of a table.

## Usage

```bash
vigocli repo <subcommand> [flags]
```

## `vigocli repo status`

Prints aggregate counts across the fleet.

```
$ vigocli repo status
Vigorepo fleet — generated 2026-04-19 15:48:02

Publishers (sources)   2
Distinct labels        2
Materialized mirrors   3
Orphan mirrors         0
Stale mirrors          0
Participants           3
```

**Orphan mirrors** are bare repos held by envoys whose publisher no longer selects them (the publisher rotated its LAN picks or was revoked). **Stale mirrors** haven't been refreshed in 30+ days — typically a dead source envoy. Both counts use yellow for any non-zero value.

## `vigocli repo list`

Prints one row per label. The `LAST BUNDLE` column distinguishes **full** (repo-sized) from **incremental** (delta since the prior bundle — expected to be small).

```
$ vigocli repo list
LABEL                          PUBLISHER  MIRRORS  LAST BUNDLE   SIZE      STATE
vigorepo:danlap:dan:notes      danlap     1/3      full          200.0 KB  publishing
vigorepo:danlap:dan:vigo       danlap     2/5      incremental   1.6 KB    publishing
```

An incremental bundle is **expected** to be small: every commit produces a new delta, not a full history pack. The bundle-kind pill is there so a 1.6 KB seed on the swarm page doesn't read as "something went wrong."

`STATE` is `publishing` normally, `paused` when the sidecar sets `paused: true`, or `orphan (no publisher)` when the label only appears in receiver-side mirror reports (the source envoy is gone).

## `vigocli repo show <label|project>`

Detailed view of one label with a per-host mirror grid.

```
$ vigocli repo show vigo
Label:          vigorepo:danlap:dan:vigo
Publisher:      danlap
User:           dan
Project:        vigo
Description:    Vigo distributed state enforcement engine
Tags:           vigo
Max mirrors:    5
Git HEAD:       97debaead89ec16cf46c0ad8e36252a97736c2cc
Blob sha:       d57ee0f57f3b4481344da21a74e867cfcf71189659dc45864d91fa5c83513d60
Last bundle:    incremental · 1.6 KB
Publisher seen: 2026-04-19 15:35:41

Mirrors (2 selected, 2 materialized, 0 orphan, 0 stale):
  HOST       STATUS   GIT SHA       LAST REFRESH
  envoy-b    active   97debaead89e  2026-04-19 15:35
  envoy-c    active   97debaead89e  2026-04-19 15:35
```

### Label resolution

The positional argument accepts, in order:

1. The full label: `vigorepo:<seeder>:<user>:<project>`
2. The project name alone, exact match
3. A unique prefix of the project name

If the same project is published by two different envoys, the bare project name is ambiguous — the command lists candidates and exits non-zero. Pass the full label to disambiguate.

### Mirror statuses

| Status | Meaning | Color |
|--------|---------|-------|
| `active` | Host is in the publisher's selection AND has the bare repo materialized | Green |
| `pending` | Host is selected but hasn't finished materializing yet | Yellow |
| `orphan` | Host holds the bare repo but the publisher no longer selects it (rotation or revocation) | Lavender |
| `(stale)` | Appended to any row where `now - last_refreshed > 30d` | Red |

## JSON output

Every subcommand accepts `--json`. `list` and `show` emit the raw `LabelSummary` / `[]LabelSummary` shape defined in `server/vigorepomesh`; `status` emits the full `Fleet` envelope so downstream scripts can pluck totals or the participant list directly.

```bash
vigocli repo list --json | jq '.[] | select(.count_stale > 0) | .label'
```

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Error (server unreachable, no matching label, ambiguous project name, etc.) |

## Related

- [`vigo repo`](vigorepo.md) — envoy-local inspection and recovery
- [`vigocli swarm`](swarm.md) — blob distribution and revocation
- [vigorepo concepts](../../engineering/subsystems/vigorepo.md) — sidecar format, LAN selection, full vs incremental bundles
