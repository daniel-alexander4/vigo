
# Set up Poolq

You'll finish this page with a Kafka-style ordered log on the fleet — one envoy posts signed, sequence-numbered messages to a named topic, every other envoy can pull them in order, replay from a chosen offset, and trust the content because the founder's puddle key signed each one end-to-end. Posts are puddle-members-only; reads are universal.

**When you'd use this:** small, ordered, append-only events you want propagated to the whole fleet — build-task work items, deploy markers, "X-released-at-Y" notices, security advisories, anywhere `journalctl` would be too local and an email would be too heavy. Poolq earns its keep when the same handful of bytes has to land on more than one envoy *in order*.

**When you'd skip this:** large payloads (>16 KiB per message; pair with a [curator](set-up-curator.md) artifact + a poolq message that references it by id); confidential content (use [lockbox](set-up-lockbox.md) — poolq is fleet-readable at rest); ad-hoc unstructured files (use filecast); state that should live as a git repo (use [gitback](set-up-gitback.md)).

## Enable it

Per-envoy: opt in via `server.yaml`:

```yaml
swarm:
  poolq:
    enabled:
      - "*"                   # pattern list, first-match-wins; "-" prefix denies
    retention: "7d"           # age window; older messages prune, stale-on-ingest refused
    max_msg_bytes: 16384      # per-message body cap; default 16 KiB, hard ceiling 64 KiB
```

Per-user: grant `poolq: true` on the usercrate (heavier than `gitback:` — write access to a fleet-readable queue):

```yaml
resources:
  - type: user
    username: dan
    puddle: true              # required
    poolq: true               # opt-in for poolq publish
```

Publish (`sudo vigocli config publish`).

## Use it

From an envoy where the operator has puddle unlocked:

```bash
# Post a message (the topic is implicitly created by the first post):
vigo swarm poolq post build-tasks --body '{"task":"deploy","host":"plex"}'

# Post from stdin:
echo '{"task":"rollback"}' | vigo swarm poolq post build-tasks --stdin

# List every topic the fleet knows about:
vigo swarm poolq topics

# Subscribe + read (offset tracked locally per user):
vigo swarm poolq subscribe build-tasks
vigo swarm poolq read build-tasks                # advances the offset
vigo swarm poolq read build-tasks --limit 5

# Inspect without touching the offset:
vigo swarm poolq tail build-tasks --limit 20     # latest N messages

# Replay:
vigo swarm poolq seek build-tasks 0              # next read returns from seq 1
vigo swarm poolq offset build-tasks              # show current offset
```

From the admin server (`vigocli`) — operator views plus moderation:

```bash
vigocli swarm poolq list                          # fleet topic table
vigocli swarm poolq show <topic_id>               # full signed log
vigocli swarm poolq read <topic_id> --from 1 --to 10
vigocli swarm poolq block <topic_id>              # server refuses to serve the log
vigocli swarm poolq unblock <topic_id>
```

## How it works

Poolq is the sixth swarm content subsystem. It reuses three pieces of existing fleet machinery and adds one new thing — a founder-stamped monotonic sequence number — that turns a CRDT-replicated set into an ordered log.

### Identity

`topic_id = sha256(founder_puddle_pubkey ‖ topic_name)` — the gitback `project_id` / curator `artifact_id` shape. Two operators can both publish `build-tasks` without collision; the operator's own puddle is the implicit default namespace, so `vigo swarm poolq read build-tasks` resolves to the local user's topic. Cross-puddle reads use the hex `topic_id` (always works, self-certifying).

There is no separate "create topic" verb. A topic exists when its first signed message lands.

### The message

Each message is a small signed JSON envelope:

```jsonc
{
  "body": {
    "schema_version": 1,
    "topic_name": "build-tasks",
    "seq": 17,                            // founder-monotonic, gap-free, inside the signed bytes
    "body": "{\"task\":\"deploy\"}",      // UTF-8 text, capped at swarm.poolq.max_msg_bytes
    "posted_at": 1714521600123,           // unix ms
    "author_puddle_pubkey": "<hex 32 B>"  // == founder in v1
  },
  "signature": "<hex 64 B ed25519 over canonical_bytes(body)>"
}
```

`seq` is **inside** the signed canonical bytes — a reorder attempt invalidates the signature. The signing domain is `poolq-msg-v1\0` (distinct from every other Vigo signed shape), so a signature from one Vigo subsystem can't be replayed under another.

### Publishing — write path

`vigo swarm poolq post <topic> --body <text>` runs as a real user with `poolq: true` and an unlocked puddle. The CLI (no helper daemon — see [ADR-029](../../adr/029-poolq.md)):

1. Validates topic name + body size (operator cap and the 64 KiB hard ceiling).
2. Acquires the per-topic seq allocator at `~/.vigo-poolq/<topic_id>/allocator.json` under `flock(LOCK_EX)`, reads `next_seq`, issues it, bumps the file, releases the lock.
3. Builds the canonical bytes (POOLQ_MSG_DOMAIN prefix + schema_version + author_pubkey + topic_name + seq + posted_at + body) and sends them to the puddle session helper's `SIGN` verb (`$XDG_RUNTIME_DIR/vigo/puddle.sock`).
4. Assembles the Message + signature, writes it atomically to `~/.vigo-poolq/<topic_id>/messages/<msg_id>.json` (msg_id = sha256 of canonical_bytes — content-addressed, idempotent).

Concurrent posts from the same user on the same envoy serialize on the flock; two terminals can't mint the same seq.

### Replication — trait → mesh → REST

On every check-in cycle, each envoy's `poolq` trait collector walks `~/<user>/.vigo-poolq/<topic_id>/messages/*.json` for every Unix user, drops anything whose filename doesn't equal its own `msg_id` or whose signature doesn't verify, and emits the rest verbatim to the server. The server's `poolqmesh` aggregator:

- Re-derives `topic_id` from each message's `(author_pubkey, topic_name)` and buckets by it. Two founders posting under the same `topic_name` end up in different buckets — that's the v1 founder-only enforcement, implicit in the topic_id derivation.
- Merges the per-topic G-Set on `msg_id` (immutable messages → identical bytes → identical id → one row).
- Applies one predicate as both age-prune and stale-on-ingest refusal: `posted_at < (now - swarm.poolq.retention)` ⇒ drop. Flap-stable without tombstones — once a message ages out everywhere, it stays out.

The server does **not** verify Ed25519 signatures — `poolqmesh` is a relay, end-to-end verification happens on the consumer side (`vigo swarm poolq read`, `vigocli swarm poolq show`). Same posture curator and gitback name-claims take; avoids a Go-side twin of `agent/src/swarm/poolq/message.rs`.

### Reading — universal, server-mediated

The REST surface lives at `/api/v1/swarm/poolq/`:

```
GET /topics                              → every known topic + summary
GET /topic/{topic_id}/head                → one topic's summary (cheap poll)
GET /topic/{topic_id}/range?from&to       → message slice for a consumer's catch-up
GET /topic/{topic_id}/log                 → full topic for inspection
```

All four are public-by-construction — mTLS is the transport guard — so a swarm-disabled envoy can consume topics the same way it pulls curator artifact bytes through `/blob/<sha>`.

Consumer offsets live per-user at `~/.vigo-poolq/subscriptions.json` and are **never replicated**. That's the property that makes the Kafka-style model fit a CRDT substrate without consensus: each consumer owns its cursor, and replaying a topic costs nothing (`vigo swarm poolq seek build-tasks 0` rewinds; the next `read` returns from seq 1 again).

### Authorization

Two layers gate posting. Both must say yes:

| Layer | Where | Question |
|---|---|---|
| Envoy | `server.yaml#swarm.poolq.enabled` pattern list | Is poolq available on this envoy at all? |
| User | usercrate `poolq: bool` flag | May this Unix user post? |

Plus the operator-puddle layer at signing time: the user's puddle session helper must be running (`vigo swarm puddle unlock`).

`poolq: true` is a **heavier** grant than `gitback: true` — "may post to a fleet-readable queue" vs "may found personal git repos." Removing it from a usercrate triggers a scrub on the next reconcile (`~/.vigo-poolq/` wiped for that user). Already-posted messages stay in the fleet view until they age out — you can't recall them by losing a flag, same stance ADR-024 takes for curator.

Reading is **ungated** — messages are fleet-readable.

The operator-moderation backstop is `vigocli swarm poolq block <topic_id>` (admin-only). The server stops serving the topic's `/range` and `/log` endpoints fleet-wide regardless of who founded it (the topic's summary still appears in `/topics` with `"blocked": true`, so consumers and operators see it was blocked). The signed messages still exist in posting users' `~/.vigo-poolq/` directories — only a `rm -rf` on the founder's state dir truly purges.

### Leaving poolq / decommissioning a user

`userdel -r <user>` removes `~/.vigo-poolq/` along with the rest of the home dir; the trait collector stops seeing the user's messages on the next cycle, and they age out of the fleet view by `swarm.poolq.retention`. Removing `poolq: true` from the usercrate (without removing the user) triggers the per-user `~/.vigo-poolq/` scrub on the next reconcile — same shape curator and gitback flag-removal take.

### Out of v1 scope

See [ADR-029](../../adr/029-poolq.md)'s "Out of v1 scope" section for the deferred features: competing consumers / ack-redeliver (the RabbitMQ-style work-queue shape), partitions, delegated non-founder publishers, multicast Tier-1 fast-path for sub-cadence latency, active push-delivery hooks (wall / MOTD / exec-on-message), encrypted/private topics, agent-side fleet-log cache, per-publisher block.
