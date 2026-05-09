
# vigocli swarm gitmirror

Admin-side, fleet-wide view of the [gitmirror](../../engineering/subsystems/gitmirror.md) unified-substrate mesh (ADR-015). Every envoy's `gitmirror` trait is aggregated server-side by `server/swarm/gitmirrormesh` and surfaced through `GET /api/v1/swarm/gitmirror/fleet`; this command is the CLI consumer of that endpoint.

`vigocli swarm gitmirror` is **read-only**. Membership changes (init / invite / join / leave / revoke / reanchor) run on the envoy holding the project — see the envoy-side [`vigo swarm gitmirror`](agent-swarm-gitmirror.md) verb tree.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `status` | Fleet-wide totals (projects, hosts, divergent refs, envelope counts) |
| `list` | One row per project with handle, fan-out, member fleets, host count, divergence, envelopes |
| `show <project_id\|handle>` | Detailed view of one project with refs and per-host breakdown |
| `members [project_id\|handle]` | Per-project membership (founder + member puddle pubkeys) as a table; mirrors `vigocli swarm cryptsync members` |

Every subcommand accepts `--json` to emit machine-readable output.

## Project identity

Every project is identified by `project_id = sha256(founder_puddle_pubkey)` rendered as 64-character lowercase hex. URLs:

```
vigo://<project_id>/<handle>
```

The handle is human-readable; cryptographic identity is the project_id. Two projects can share a handle — there's no central registry.

## Usage

```bash
vigocli swarm gitmirror <subcommand> [flags]
```

## `vigocli swarm gitmirror status`

Prints aggregate counts across the fleet.

```
$ vigocli swarm gitmirror status
Gitmirror fleet — generated 2026-04-30 15:48:02

Projects          3
Hosts             5
Divergent refs    0
Revoke envelopes  0
Leave envelopes   0
Participants      5
```

`Divergent refs` flags ref names where hosts holding the project disagree on the head sha. Resolves on the next successful fan-out push. Yellow for any non-zero value.

## `vigocli swarm gitmirror list`

Prints one row per project.

```
$ vigocli swarm gitmirror list
HANDLE       FAN-OUT        MEMBERS  HOSTS  DIVERGENT  ENVELOPES   PROJECT_ID
notes        members-only   1        1      —          0 R / 0 L   8e23bc5491cf
team-repo    fleet-wide     2        4      1 ref(s)   1 R / 0 L   1d8a5e23fffa
vigo-web     fleet-wide     1        2      —          0 R / 0 L   e3b0c44298fc
```

| Column | Meaning |
|--------|---------|
| `HANDLE` | Human-readable handle from `project.json`. Display only. |
| `FAN-OUT` | `fleet-wide` (replicas land on every fleet envoy) or `members-only`. |
| `MEMBERS` | Number of distinct member fleets. |
| `HOSTS` | Number of envoys reporting that they hold the project. |
| `DIVERGENT` | Count of refs where hosts disagree on the head sha. Yellow when non-zero. |
| `ENVELOPES` | `<R revokes> / <L leaves>`. Adds yellow `(skew)` when host envelope digests don't match — gossip is in flight. |
| `PROJECT_ID` | First 12 chars of the project_id hex. Pass the full id to `show` to disambiguate handle collisions. |

## `vigocli swarm gitmirror show <project_id|handle>`

Detailed view of one project.

```
$ vigocli swarm gitmirror show vigo-web
Project:        vigo-web
URL:            vigo://e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855/vigo-web
Project ID:     e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Founder puddle:  ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8
Fan-out:        fleet-wide
Members (1):
  ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8
Hosts:          2
Last updated:   2026-04-30 15:48:02
Envelope state: unanimous · 0 revoke(s), 0 leave(s)

Refs:
  REF                CONSENSUS     LAGGING
  refs/heads/main    97debaead89e  —

Hosts:
  HOST     BARE REPO  SIZE     USERS  LEAF ROLE  LAST SEEN
  danlap   yes        4.0 KB   dan    Admin      2026-04-30 15:48
  plex     yes        4.0 KB   dan    Admin      2026-04-30 15:47
```

### Argument resolution

The positional argument accepts, in order:

1. The full 64-char-lowercase-hex `project_id` (canonical, always wins)
2. An exact handle match (errors if two projects share the handle — pass the project_id instead)
3. A unique handle prefix

## `vigocli swarm gitmirror members [project_id|handle]`

Per-project membership table mirroring [`vigocli swarm cryptsync members`](swarm-cryptsync.md). Without an argument, prints every project; with one, scopes to a single project (same resolution rules as `show`).

```
$ vigocli swarm gitmirror members
PROJECT: notes  (project_id 8e23bc5491cf)
ROLE      FLEET-PUBKEY
founder   ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8

PROJECT: vigo-web  (project_id e3b0c44298fc)
ROLE      FLEET-PUBKEY
founder   ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8
member    ed25519:f43f6ef3eea29e3780d73aa2cee4a78193419a69f43f6ef3eea29e3780d73aa2
```

| Column | Meaning |
|--------|---------|
| `ROLE` | `founder` for the project's anchor fleet (always the first member); `member` for every additional fleet. |
| `FLEET-PUBKEY` | The fleet's Ed25519 pubkey, prefixed with `ed25519:` for symmetry with `vigo swarm puddle pubkey` output. |

For host-presence detail (which envoys hold the bare repo, sizes, last-seen times) use `vigocli swarm gitmirror show <project>` instead — `members` is intentionally cryptographic-membership only, mirroring how `cryptsync members` is per-user-circle only.

## JSON output

Every subcommand accepts `--json`. `list` and `show` emit the raw `ProjectSummary` shape defined in `server/swarm/gitmirrormesh`; `status` emits the full `Fleet` envelope so downstream scripts can pluck totals or the participants list directly.

```bash
vigocli swarm gitmirror list --json | jq '.[] | select(.divergent_refs | length > 0) | .handle'
```

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Error (server unreachable, no matching project, ambiguous handle, etc.) |

## Related

- [`vigo swarm gitmirror`](agent-swarm-gitmirror.md) — envoy-local verb tree (`project init / invite / join / leave / revoke / reanchor`)
- [gitmirror trait](../traits/gitmirror.md) — the per-envoy report this command aggregates
- [`vigocli swarm swarmcast`](swarm-swarmcast.md) — admin-pushed swarmcast distribution on the swarm substrate
- [gitmirror concepts](../../engineering/subsystems/gitmirror.md)
- [ADR-015 — Unified gitmirror substrate](../../adr/015-unified-gitmirror-substrate.md)
