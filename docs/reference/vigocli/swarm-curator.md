
# vigocli swarm curator

Operator-side view of [curator](../../howto/set-up-curator.md) — the P2P artifact registry (ADR-024). Every envoy's `swarm_curator` trait is aggregated server-side by `server/swarm/curatormesh` (30-second TTL cache; invalidated on block/unblock so moderation takes effect immediately) and surfaced through `/api/v1/swarm/curator/*`. This command is the CLI consumer.

`vigocli swarm curator` is **read-only plus the admin moderation backstop**. Mutating verbs (`push` / `tag` / `untag` / `set-recipients` / `set-target` / `rm` / `prune` / `gc` / `stale` / `purge`) live on the agent side as [`vigo swarm curator`](agent-swarm-curator.md) — they need an unlocked per-user puddle to sign catalog entries, which doesn't fit vigocli's root-only / peer-auth posture. Per [ADR-024 open question #1, resolved 2026-05-13](../../adr/024-curator.md#implementation-notes), this split is deliberate.

## Subcommands

| Subcommand | Description |
|---|---|
| `status` | Fleet-wide totals (artifacts, participants, blocked count) |
| `list` | One row per artifact across the fleet; flags blocked entries; surfaces orphan blocks in a footer |
| `show <ref>` | Detailed view of one artifact — full signed catalog entry (versions, tags, recipients, target, rekey hint, dangling tags) |
| `versions <ref>` | Version + tag table for one artifact |
| `resolve <ref> <tag\|version> <os> <arch>` | Show the blob coordinates a pull would fetch |
| `block <ref> [--reason <text>]` | Operator-block an artifact — server refuses to resolve/relay regardless of who published |
| `unblock <ref>` | Reverse a prior block |

Every read subcommand accepts `--json` to emit machine-readable output.

## Artifact reference (`<ref>`)

The positional argument accepts, in order:

1. The full 64-char lowercase hex `artifact_id` (canonical, always wins)
2. An exact artifact name match across the fleet catalog (errors if two founders publish the same name — pass the artifact_id instead)
3. A unique name prefix

Two different founders publishing the same name is the structural ambiguity case: the same name resolves to two different `artifact_id`s. The CLI prints both short artifact_ids and refuses; pass the full hex to disambiguate.

## Usage

```bash
vigocli swarm curator <subcommand> [flags]
```

## `vigocli swarm curator status`

Aggregate counts across the fleet, plus the participating-hosts list.

```
$ vigocli swarm curator status
Curator — generated 2026-05-14 09:12:33

Artifacts     7
Participants  4
Blocked       0

Participating hosts:
  annlap
  danlap
  girlslaptop
  plex
```

`Blocked` is yellow when non-zero so an operator can see at a glance whether any moderation actions are in effect. The participating-hosts list inline is the envoys reporting a `swarm_curator` trait (any envoy holding local publish state for any user) — useful for verifying that the right hosts are actually reporting.

## `vigocli swarm curator list`

One row per artifact known to the fleet catalog.

```
$ vigocli swarm curator list
NAME             VERSIONS  TAGS  PLATFORMS               TARGET   STATUS    ARTIFACT_ID
nginx-config     2         1     linux/amd64             *        ok        1d8a5e23fffa
redis-bin        3         2     linux/amd64,linux/arm64 us-west* ok        8e23bc5491cf
vigo-agent       12        3     darwin/arm64,linux/…    *        ok        e3b0c44298fc
old-thing        1         0     linux/amd64             *        blocked   af31c2e84d12
```

| Column | Meaning |
|---|---|
| `NAME` | Artifact name from the catalog entry. |
| `VERSIONS` | Number of distinct version strings published. |
| `TAGS` | Number of distinct tag bindings. |
| `PLATFORMS` | Sorted, deduped union of `<os>/<arch>` across all versions. Truncated for display; use `show` for the full breakdown. |
| `TARGET` | The `target` glob from the entry — which envoys hold the P2P DR replica. Truncated for display. |
| `STATUS` | `ok` (green) for a normal entry; `blocked` (red) when the server is refusing to resolve/relay. |
| `ARTIFACT_ID` | First 12 chars of the artifact_id hex. Pass the full id to `show` / `block` / `unblock` to disambiguate name collisions. |

### Orphan blocks

If a block was issued against an artifact whose publisher has since `rm`'d the entry, the live catalog won't include it. The CLI surfaces these in a footer so an operator can see all in-effect blocks at a glance:

```
$ vigocli swarm curator list
NAME       VERSIONS  TAGS  PLATFORMS    TARGET  STATUS  ARTIFACT_ID
nginx-cfg  1         1     linux/amd64  *       ok      1d8a5e23fffa

Note: 1 block(s) without a current catalog entry:
  af31c2e84d12  blocked 2026-05-12 by dan — supply-chain incident #ABC
```

## `vigocli swarm curator show <ref>`

Detailed view of one artifact — full signed catalog entry.

```
$ vigocli swarm curator show vigo-agent
Name:           vigo-agent
Artifact ID:    e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
Founder puddle: ed25519:8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8
Target:         *
Recipients:     *
Issued at:      2026-05-14 09:11:08
Status:         ok

Versions:
  VERSION  TAGS           OS/ARCH        SIZE      SHA
  0.50.2   stable         linux/amd64    8.4 MiB   3f2a8b9c1d4e
                          linux/arm64    8.1 MiB   1a5e7c2d9f43
                          darwin/arm64   8.6 MiB   7b3c2f1d4a9e
  0.51.0   latest         linux/amd64    8.5 MiB   c1d4e3f2a8b9
                          linux/arm64    8.2 MiB   9f43a5e7c2d1
                          darwin/arm64   8.7 MiB   4a9e7b3c2f1d
```

When the signing puddle differs from the founder puddle (the founder has rekeyed via [ADR-019](../../adr/019-gitback-founder-rekey.md)), `show` notes the rekey hint:

```
Signing puddle: ed25519:<new pubkey>  (rekeyed — delegation chain bridges)
```

Tags pointing at non-existent versions are flagged in a footer:

```
Note: 1 dangling tag(s): canary → 0.99.0
```

## `vigocli swarm curator versions <ref>`

Compact version + tag table.

```
$ vigocli swarm curator versions vigo-agent
Artifact: vigo-agent  (id e3b0c44298fc)

VERSION  TAGS           PLATFORMS
0.50.2   stable         darwin/arm64, linux/amd64, linux/arm64
0.51.0   latest         darwin/arm64, linux/amd64, linux/arm64
```

`<ref>` must resolve to a hex artifact_id — the server's `/versions/{aid}` endpoint takes hex only. Name resolution is client-side via `/list`.

## `vigocli swarm curator resolve <ref> <tag|version> <os> <arch>`

Show what `vigo swarm curator pull` would fetch. Does not download.

```
$ vigocli swarm curator resolve vigo-agent latest linux amd64
Resolved e3b0c44298fc/vigo-agent @ 0.51.0 on linux/amd64:

Blob sha  c1d4e3f2a8b9...
Size      8.5 MiB
Version   0.51.0
```

The CLI tries `tag=` first; on miss, falls back to `version=` (the server treats them as alternative parameters, not fallbacks).

## `vigocli swarm curator block <ref> [--reason <text>]`

Operator-block an artifact. The server stops resolving it on `/resolve` and stops relaying its bytes on `/blob/<sha>` regardless of who published it. The signed catalog entry still exists and gossips, and the DR bytes still sit on every swarm-enabled envoy that holds the `target` replica — only a publisher `vigo swarm curator rm` truly purges.

```
$ vigocli swarm curator block old-thing --reason "supply-chain incident #ABC"
Blocked af31c2e84d12.
Reason: supply-chain incident #ABC
```

Block events go in the tamper-evident audit chain. The aggregator's TTL cache is invalidated immediately so the moderation effect is synchronously visible on `list`/`show`/`resolve` — no waiting for the 30-second window.

## `vigocli swarm curator unblock <ref>`

Reverse a prior block.

```
$ vigocli swarm curator unblock af31c2e84d12
Unblocked af31c2e84d12.
```

If the publisher has since `rm`'d the entry, unblock removes the row from the block table but doesn't restore an entry that no longer exists.

## JSON output

Every read subcommand accepts `--json`. Shapes mirror the REST responses from `server/api/swarm_curator.go`:

```bash
# All artifacts published by a specific founder puddle.
vigocli swarm curator list --json | jq '.catalog.artifacts[] | select(.body.founder_puddle_pubkey == "<hex>") | .body.name'

# Every artifact currently blocked, with reasons.
vigocli swarm curator list --json | jq '.blocked[] | {id: .artifact_id, when: .blocked_at, who: .blocked_by, why: .reason}'

# Just the size of the largest version of a single artifact.
vigocli swarm curator show e3b0c44298fc --json | jq '[.entry.body.versions[].platforms[].size] | max'
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Error (server unreachable, no matching artifact, ambiguous name, etc.) |

## Related

- [`vigo swarm curator`](agent-swarm-curator.md) — envoy-side surface: founder verbs (push/tag/untag/set-recipients/set-target/rm), read verbs, local cleanup
- [Curator engineering doc](../../howto/set-up-curator.md) — identity, catalog shape, two-axis distribution, wire surface
- [ADR-024 — curator P2P artifact registry](../../adr/024-curator.md) — the design
- [`vigocli swarm gitback`](swarm-gitback.md) — the analogous fleet view for personal-DR git mirrors
- [`vigocli swarm filecast`](swarm-filecast.md) — admin-pushed *one-off* file distribution (curator is for named, versioned, mutable-tag artifacts)

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
