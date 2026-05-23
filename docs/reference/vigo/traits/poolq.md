---
title: poolq
---

# poolq

Reports each envoy's local view of the [poolq](../../../howto/set-up-poolq.md) ordered-log queue (ADR-029): every signed message posted by a local founder, verbatim, ready for the server's `poolqmesh` aggregator to merge into the fleet-wide G-Set.

The agent walks every user's `~/.vigo-poolq/<topic_id>/messages/*.json` and drops anything whose directory name doesn't equal its own derived `topic_id`, whose filename doesn't equal its own derived `msg_id`, or whose signature doesn't verify. Surviving messages flow through verbatim; the server's aggregator buckets them by `topic_id`, applies age-based retention with stale-on-ingest refusal, and serves the merged log via `/api/v1/swarm/poolq/*`. The server does **not** verify Ed25519 signatures — end-to-end verification stays on the consumer side (see [ADR-029](../../../adr/029-poolq.md)).

The trait is emitted only when at least one valid message exists. An envoy with no poolq state on any user's home produces a `null` value for the namespace.

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `message_count` | int | Number of valid signed messages this envoy is reporting (post-dedup). |
| `messages` | object[] | One entry per signed message. See below. Sorted by `(topic_id_hex, msg_id_hex)` for deterministic trait output. |

### `messages[]`

| Field | Type | Description |
|---|---|---|
| `body.schema_version` | int | Layout discriminator. `1` for the v1 envelope. Inside the signed canonical bytes; a pre-v2 envelope fails verification under a v2 reader rather than being silently reinterpreted. |
| `body.topic_name` | string | Human-readable topic name. Validated 1-64 chars, ASCII alphanumeric + `-` `_` `.`, no leading dot, not 64 hex. The topic_id is derived `sha256(author_puddle_pubkey ‖ topic_name)` at receive time — the agent uses this to bucket. |
| `body.seq` | int | Founder-monotonic sequence number. Gap-free per topic on a single envoy (allocated under flock); inside the signed canonical bytes, so reordering is detectable. |
| `body.body` | string | UTF-8 text body. Operators encode binary payloads at the application layer. Size-capped at `swarm.poolq.max_msg_bytes` (16 KiB default, 64 KiB hard ceiling). |
| `body.posted_at` | int | Unix milliseconds when the message was posted. Used by the mesh aggregator as both age-prune cutoff and stale-on-ingest refusal predicate. |
| `body.author_puddle_pubkey` | string | 64-char lowercase hex of the signing puddle's Ed25519 pubkey. In v1 founder-only, this equals the topic's founder pubkey. |
| `signature` | string | 128-char lowercase hex (64 raw bytes) of the Ed25519 signature over `canonical_bytes(body)` under the `poolq-msg-v1\0` domain. Consumers verify this against `author_puddle_pubkey`. |

## Drop criteria (collector-side)

The collector silently drops a candidate message if any of these hold:

| Condition | Why |
|---|---|
| File isn't valid JSON | Local corruption — never publish unreadable bytes. |
| Filename ≠ `<msg_id>.json` where `msg_id = sha256(canonical_bytes(body))` | Tampering at rest: a different message bytes than the filename claims. |
| Directory name ≠ `<topic_id_hex>` where `topic_id = sha256(author_puddle_pubkey ‖ topic_name)` | Misfiled message — refuses to attribute it to the wrong topic. |
| Signature doesn't verify under `author_puddle_pubkey` | Forgery or post-write mutation. |

Dropped messages produce no log entry — silent drops are by design (the agent isn't a moderator; it just refuses to publish malformed local state).

## Cross-package wiring

- **Posting:** `vigo swarm poolq post` → atomic write under `~/.vigo-poolq/<topic_id>/messages/<msg_id>.json` (`agent/src/swarm/poolq/post.rs`).
- **Trait emit:** this collector (`agent/src/traits/poolq.rs`).
- **Server merge:** `server/swarm/poolqmesh/poolqmesh.go` — per-topic G-Set by `msg_id`, age-prune + stale-on-ingest refusal at `posted_at < (now - retention)`.
- **REST surface:** `server/api/swarm_poolq.go` — `/api/v1/swarm/poolq/{topics,topic/{id}/{head,range,log}}`. See [`/api/v1/swarm/poolq/...`](../../api.md).
- **Operator views:** [`vigocli swarm poolq`](../../vigocli/swarm-poolq.md).
- **Agent CLI:** [`vigo swarm poolq`](../../vigocli/agent-swarm-poolq.md).

## When the trait is absent

The namespace is omitted (`null` at the JSON level) under any of:

- No user on this envoy has ever posted to a poolq topic (no `~/.vigo-poolq/<topic_id>/messages/` directories on disk).
- `/etc/passwd` cannot be read (collector returns null rather than failing the whole report).
- Every candidate message was dropped by the criteria above.

An empty envoy is information-free; the absence is the report.
