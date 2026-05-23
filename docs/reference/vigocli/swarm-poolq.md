
# vigocli swarm poolq

Operator-side view of [poolq](../../howto/set-up-poolq.md) — the P2P ordered-log queue (ADR-029). Every envoy's `poolq` trait is aggregated server-side by `server/swarm/poolqmesh` (30-second TTL cache; invalidated on block/unblock so moderation takes effect immediately) and surfaced through `/api/v1/swarm/poolq/*`. This command is the CLI consumer.

`vigocli swarm poolq` is **read-only plus the admin moderation backstop**. The write verb (`post`) lives on the agent side as [`vigo swarm poolq`](agent-swarm-poolq.md) — it needs an unlocked per-user puddle to sign messages, which doesn't fit vigocli's root-only / peer-auth posture. Same split that [`vigocli swarm curator`](swarm-curator.md) takes.

## Subcommands

| Subcommand | Description |
|---|---|
| `status` | Fleet-wide totals (topics, participants, message count) |
| `list` | One row per topic across the fleet |
| `show <topic_id>` | Detailed view of one topic — full signed message log |
| `read <topic_id> [--from N] [--to N]` | Print messages from a topic in a seq range (stateless — does not advance any offset) |
| `block <topic_id> [--reason <text>]` | Operator-block a topic — server refuses to serve its message log fleet-wide |
| `unblock <topic_id>` | Reverse a prior block |

Every read subcommand accepts `--json` to emit machine-readable output.

## Topic reference (`<topic_id>`)

The positional argument is always the **64-char lowercase hex `topic_id`** — `sha256(founder_puddle_pubkey ‖ topic_name)`. The friendly `poolq://<puddle-name>/<topic_name>` form is a posting-side convenience (`vigo swarm poolq post my-topic` resolves "my-topic" against the local user's puddle); operator views work in hex because the operator is reading across founders.

Get a topic_id from `vigocli swarm poolq list` (right-most column).

## Usage

```bash
vigocli swarm poolq <subcommand> [flags]
```

## `vigocli swarm poolq status`

Fleet rollup:

```
Poolq — generated 2026-05-23 14:02:11

Topics          3
Participants    4
Messages        47

Participating hosts:
  annlap
  girlslaptop
  plex
  webhost
```

## `vigocli swarm poolq list`

One row per topic. Columns:

| Column | Meaning |
|---|---|
| `NAME` | Operator-readable topic name (truncated to 28 chars) |
| `HEAD_SEQ` | The highest seq the server has merged for this topic |
| `COUNT` | Total messages in the merged G-Set (after age-prune) |
| `FIRST` | `posted_at` of the earliest retained message |
| `LAST` | `posted_at` of the most recent message |
| `TOPIC_ID` | 12-char short id; full id via `--json` or copy from `show` |

## `vigocli swarm poolq show <topic_id>`

Full signed log — header (founder pubkey, head seq, count, first/last timestamps, blocked-status) plus a seq-ordered table of every message body. The `BODY` column is truncated to 80 chars on a single line; use `--json` for the raw envelope per message including the signature.

Refuses (HTTP 404) if the topic is operator-blocked.

## `vigocli swarm poolq read <topic_id> [--from N] [--to N]`

Like `show` but bounded to a `[from, to]` seq range. Defaults: `from=1`, `to=0` (head). The operator view is stateless — it does not maintain a server-side offset (consumer offsets live per-user on each posting/reading envoy). For stateful consumption, use [`vigo swarm poolq read`](agent-swarm-poolq.md) on the agent side.

Refuses (HTTP 404) if the topic is operator-blocked.

## `vigocli swarm poolq block <topic_id> [--reason <text>]`

Mark a topic as operator-blocked. The server stops serving `/topic/<id>/range` and `/topic/<id>/log` fleet-wide regardless of who founded the topic. The summary still appears in `/topics` with `"blocked": true`, so consumers learn the topic was blocked rather than silently 404-ing on a topic they once read.

What block does NOT do:

- It does not delete messages. The signed messages still exist in posting users' `~/.vigo-poolq/<topic_id>/messages/` directories and the trait collector still emits them. Only a `rm -rf` on the founder's state dir (`userdel -r`, withdrawing the `poolq:` usercrate flag, or a manual scrub) truly purges.
- It does not gossip — the block is a server-side relay refusal, not a wire-level signal to peer agents.

`--reason` records a free-form note on the `poolq_blocked_topics` audit row. Recommended for legal / compliance traces.

## `vigocli swarm poolq unblock <topic_id>`

Reverse a prior block. Topic resumes serving immediately (the 30s `poolqmesh` cache is invalidated by both block and unblock).

## REST endpoints sourced from

- `GET /api/v1/swarm/poolq/topics` — `list` / `status`
- `GET /api/v1/swarm/poolq/topic/{topic_id}/head` — head-only summary
- `GET /api/v1/swarm/poolq/topic/{topic_id}/range?from&to` — `read`
- `GET /api/v1/swarm/poolq/topic/{topic_id}/log` — `show`
- `POST /api/v1/swarm/poolq/block/{topic_id}` — `block` (admin)
- `POST /api/v1/swarm/poolq/unblock/{topic_id}` — `unblock` (admin)

See [`/api/v1/swarm/poolq/...`](../api.md) for full request/response shapes.
