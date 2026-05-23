---
title: "ADR-029: poolq — P2P ordered-log queue"
---

# ADR-029: poolq — P2P ordered-log queue

**Status:** Approved 2026-05-23, implementation in progress.
**Date:** 2026-05-23

## Context

The fleet has gossip-replicated content subsystems for files (filecast), git history (gitback), LAN plaintext sync (longdrawer), encrypted sync (lockbox), and named artifacts (curator). All five are *content* substrates — bytes addressed by some identifier, replicated across envoys.

What the fleet does not have is **ordered, append-only state** — the shape that lets one envoy post "the X.Y.Z release was tagged at T," "user U did action V," "a backlog item N exists waiting to be picked up," and have every other envoy see those events in order, replay them, and track their own consumption offset. Kafka's model: a per-topic log of immutable messages, consumer-owned offsets, retained for a window, no consensus required on the read side.

The pieces to build this exist. The swarm mTLS transport carries verifiable byte payloads between agents (curator's catalog-entry pattern). The anchor layer (`agent/src/swarm/anchor/`) gives puddle-rooted identity, capability tokens, and a `sha256(founder_pubkey ‖ name)` derivation pattern (gitback's `project_id_for`, curator's `artifact_id`). The trait → mesh → check-in propagation pattern carries small signed envelopes fleet-wide on the existing cadence. And the two-layer authorization gate (`server.yaml#swarm.*.enabled` × usercrate flag) already covers every prior content subsystem.

Constraints, settled in `/discuss` + `/scrutinize` 2026-05-21, MQ-semantics + brainstorm rename 2026-05-22, re-scrutinized 2026-05-23: founder-only posting in v1 (multi-writer delegation is a v2 lever, gated on demand); messages are immutable, retained by age, consumer offsets are local; confidentiality is **not** a goal (mTLS transport guard only — pair with curator for big payloads, lockbox for confidential payloads); the work-queue (competing consumers, ack/redeliver) RabbitMQ shape is explicitly deferred to v2 (see `deferred.md`); the multicast Tier-1 fast-path is deferred to post-v1 (check-in cadence is acceptable for an ordered-log substrate; spanner's precedent is additive when latency demands).

## Decision

### Name and placement

**`poolq`** — the sixth swarm content subsystem. Agent module `agent/src/swarm/poolq/` (signed `Message` envelope, founder-side monotonic `seq` allocator persisted under flock, per-user state, multicall verbs); server `server/swarm/poolqmesh/` (per-topic G-Set aggregator + age-prune + stale-on-ingest refusal) + `server/api/swarm_poolq.go` (REST: topics, head, range, log, block/unblock); CLI `cli/swarm_poolq.go` (`vigocli swarm poolq`); config `swarm.poolq.{enabled, retention, max_msg_bytes}` in `server/config/types.go` + `SwarmRuntimeLimits.poolq_{max_msg_bytes,retention_secs}`; usercrate `poolq: bool`.

**No root helper daemon.** Unlike curator (which uses `/var/run/vigo/curator.sock` to mediate root-only blob staging into `/var/lib/vigo/swarm/blobs/` + manifest CRDT recording), poolq has no root-only work: per-user state under `~/.vigo-poolq/` is user-owned, signing rides the existing per-user puddle session helper at `$XDG_RUNTIME_DIR/vigo/puddle.sock`, and allocator atomicity is one `flock(2)` on `allocator.json`. The trait collector (Slice 3) walks every user's home as root, same as it would have either way. Removing the daemon shrinks the agent's surface by a tokio listener + SO_PEERCRED dance + a socket-protocol contract that would have been doing zero unique work. (Settled at Slice 2 start, 2026-05-23.)

### Identity and the topic

A topic is keyed `topic_id = sha256(founder_puddle_pubkey ‖ topic_name)` — same shape as a gitback `project_id` and curator `artifact_id`, so Alice's `build-tasks` ≠ Bob's `build-tasks`. The human-facing reference is `poolq://<founder-puddle-name>/<topic_name>` (the puddle name resolves to the pubkey via the ADR-022 fleet name map, then to the topic_id); the hex `poolq://<topic_id_hex>/<topic_name>` form always works. Topics are created implicitly by the first signed `post` — there is no separate "create topic" verb.

### The message

A message is a signed envelope:

```
{ version, topic_id, seq (founder monotonic u64), msg_id = sha256(canonical_bytes),
  body (size-capped, default 16 KiB, hard ceiling 64 KiB),
  posted_at (unix ms), author_pubkey, signature }
```

`seq` is **inside** the signed canonical bytes. New anchor signing domain `poolq-msg-v1\0` (distinct from every other Vigo signed shape — gitback's `gitback-token-v2\0`, curator's `curator-catalog-v2\0`). The founder maintains a per-topic gap-free `seq` allocator persisted at `~/.vigo-poolq/<topic_id_hex>/allocator.json`; on boot or first post the allocator resumes from `max(local seq) + 1` (the founder's own messages are the source of truth for what it has issued).

### CRDT shape — per-topic G-Set with age prune

The set of known messages for a topic is a **grow-only set** (G-Set) keyed by `msg_id` (sha256 of canonical bytes — immutable identity). Merge = union. The readable log is the union sorted ascending by `seq`. The contiguous `seq` prefix is "what every consumer can replay"; a gap means "not yet gossiped in" (with founder-only posting in v1, gaps only appear during in-flight propagation, never permanently).

Retention is **age-based**: drop messages with `posted_at` older than `swarm.poolq.retention` (default 7d). To prevent prune flap under clock skew, ingest **refuses** messages already past the retention window at receive time — once a message ages out everywhere, it stays out, no tombstones required. This is simpler than the existing `ManifestStore` LWW-Map (which has revoke-wins + tombstones) because poolq messages never mutate. A dedicated `server/swarm/poolqmesh/poolqstore.go` rather than folding into `ManifestStore`: the semantic divergence is significant enough that one store-per-shape is cleaner than a knobbed merger.

### Replication path — trait → mesh → REST

Founder posts via `vigo swarm poolq post`, which writes the signed message to `~/.vigo-poolq/<topic_id_hex>/messages/<msg_id_hex>.json` (user-side, signs via the puddle session helper). The agent's `poolq` trait collector walks `~/<user>/.vigo-poolq/<topic_id>/messages/` across users and emits the signed messages verbatim. Server `poolqmesh` ingests the trait, merges into the per-topic G-Set, prunes aged-out entries, and serves the merged log via REST. Consumer reads route to the server REST API. **No agent-side fleet-log cache in v1** — every read is a REST roundtrip. If chatter becomes a problem, an additive cache is a follow-on (curator's `catalog.json` is the precedent), as is delivering a snapshot via the check-in response (curator's `curator_catalog` precedent).

The server publishes, never arbitrates — it does **not** verify signatures or delegation chains in Go. End-to-end verification happens on the consumer side (the `vigo`/`vigocli swarm poolq` read paths and any future agent-side replayer), matching curatormesh's stance and avoiding a Go-side twin of `agent/src/swarm/poolq/message.rs`. The Go-side does compute `topic_id` (a plain SHA-256) and apply age-prune + stale-on-ingest refusal — both are non-cryptographic mechanical work.

This rides the existing check-in cadence — log is minutes-fresh. Acceptable for an ordered-log substrate; a multicast Tier-1 fast-path (per-topic head-`seq` digest broadcast triggering early pull, the spanner-roster 2-i-b precedent) is the v2 lever if latency demands.

### Write side — founder + puddle members only

`vigo swarm poolq post/topics/subscribe/read/tail/seek/offset` (the multicall binary), runnable by a Unix user iff: (1) the host matches `swarm.poolq.enabled`; (2) the user has `poolq: true` in their usercrate (`/var/lib/vigo/usercrate-policy/<user>.json`); (3) the user has a puddle identity, unlocked (the helper daemon signs via the session helper — same heavier-grant posture as curator). v1 posting is **founder-only**: a non-founder posting to a topic produces a signed message whose `author_pubkey` does not match the topic's founder, which the mesh + every consumer rejects at signature verification. Multi-writer delegation (ADR-019 chain) is deferred to v2.

`vigocli swarm poolq` is the operator's read + moderation front-end (`topics`, `read`, `block <topic_id>`, `unblock <topic_id>` — admin-only). Per-publisher block is deferred to v2 alongside multi-writer.

### Subscription state

Consumer offsets are **local-only, never replicated**: `~/.vigo-poolq/subscriptions.json` keyed by `topic_id` carries the consumer's `seq` watermark + optional filter expression. `vigo swarm poolq read <topic>` consumes from the stored offset and advances it; `vigo swarm poolq tail <topic>` follows the head; `vigo swarm poolq seek <topic> <seq>` rewinds or fast-forwards. Rewinding replays — free, because messages are retained.

### Authorization

Two-layer gate, both must say yes, default-deny on both:

| Layer | Where | Question |
|---|---|---|
| Envoy | `server.yaml#swarm.poolq.enabled` pattern list vs agent hostname | Is poolq available on this envoy? |
| User | usercrate `poolq:` flag, cached at `/var/lib/vigo/usercrate-policy/<user>.json` | May this Unix user post / subscribe? |

Mirrors curator's posture exactly. Removing `poolq: true` from a user's usercrate triggers a scrub of `~/.vigo-poolq/` on the next reconcile (Slice 5).

## Consequences / tradeoffs

- **Confidentiality is not a goal.** Messages are fleet-readable at rest — every envoy in the topic's view carries the bytes. mTLS is the transport guard. Pair with lockbox for confidential payloads; pair with a curator artifact reference for large payloads.
- **Trait-mesh-checkin propagation is minutes-fresh.** For a work-queue substrate that is acceptable — Kafka itself is not real-time. The multicast Tier-1 fast-path is a v2 lever if a use case demands sub-cadence latency; it is purely additive over this design.
- **Per-topic log size is bounded by `retention × posting volume`.** The default 7d retention × 16 KiB message cap × any reasonable cadence keeps a topic's footprint in the low MiB range. Operators who need higher throughput tune both knobs; the hard ceiling on `max_msg_bytes` (64 KiB) keeps the per-check-in trait payload bounded.
- **The server owns moderation but never validates.** Signatures are verified end-to-end on the consumer side; the server relays the merged log verbatim. `block`/`unblock` is server-side resolution gating (the bytes still sit in agents' user dirs), mirroring curator.
- **Founder-only is a real v1 limitation.** A topic founded by a user whose puddle is later lost (no rekey delegation set up) cannot be recovered — re-publish as a new topic_id. v2's multi-writer delegation chain addresses this alongside per-publisher moderation.
- **Topic friendly-name resolution lag.** The ADR-022 `RESOLVE-NAME` map has a propagation window (see [[project_gitback_2026-05-10]] memory). Hex `topic_id` always resolves; friendly URLs may need a beat after `name set`.

## Implementation notes

### v1 slices

- **Slice 1** — this ADR + `_index.md` entry; `PoolqSection` in `server/config/types.go` + pattern-list compile + `server.yaml.example` block; `SwarmRuntimeLimits.poolq_{max_msg_bytes,retention_secs}` in `protobuf/vigo.proto` + `buildSwarmRuntimeLimits` wiring; `swarm_poolq_enabled` gate emission; `poolq: bool` on `UserPolicy` + `require_poolq` + `envoy_layer_allows` arm + `Substrate::Poolq` + `GateState.poolq` + `runtime_limits.rs` cache fields.
- **Slice 2** — `agent/src/swarm/poolq/{mod,message,allocator,state,post}.rs`: signed `Message` + `canonical_bytes` + `POOLQ_MSG_DOMAIN` + `topic_id_for`; per-topic gap-free `SeqAllocator` persisted at `~/.vigo-poolq/<topic_id_hex>/allocator.json` (flock-protected); `publish()` entry point the Slice-4 CLI verb calls (validates body + size, signs via closure for testability, allocates `seq`, writes message file atomically). No helper daemon — see the "No root helper daemon" line in the Decision section.
- **Slice 3** — `server/swarm/poolqmesh/poolqmesh.go`: aggregator that joins envoy `poolq` traits into a per-topic G-Set keyed by `msg_id`, with age-prune + stale-on-ingest refusal (both non-cryptographic, no Go-side signature verification — matches curatormesh's "server publishes, never arbitrates" stance); resolver helpers (`TopicIDFor`, `LookupByID`, `RangeMessages`) that Slice 4's REST handlers call. Agent `poolq` trait collector at `agent/src/traits/poolq.rs` (walks every user's `~/.vigo-poolq/<topic_id>/messages/*.json` and emits the signed `Message` JSONs verbatim, dropping a locally-corrupt, misfiled, or non-self-verifying entry rather than publishing it).
- **Slice 4** — `server/api/swarm_poolq.go` (`topics` / `topic/{id}/head` / `topic/{id}/range` / `topic/{id}/log`) + agent multicall verbs (`post` / `topics` / `subscribe` / `read` / `tail` / `seek` / `offset`) + `vigocli swarm poolq` fleet-read.
- **Slice 5** — `POST .../block/{topic_id}` + `.../unblock/{topic_id}` admin moderation; `~/.vigo-poolq/` scrub on `poolq:` flag removal (reconcile-path extension); `docs/howto/set-up-poolq.md`, `docs/reference/server-yaml.md`, `docs/reference/vigocli/swarm-poolq.md`, `docs/concepts/poolq.md`, CLAUDE.md Architecture entry; version bump.

### Out of v1 scope (→ v2 / post-v1)

- **Competing consumers + ack/redeliver + exactly-once-delivery** (the RabbitMQ-style work queue — see `deferred.md`).
- Partitions + consumer-group partition assignment (pointless without multi-writer).
- Delegated non-founder publishers (the ADR-019 delegation chain).
- Multicast Tier-1 fast-path (per-topic head-`seq` digest broadcast → early pull).
- Active push-delivery hooks (wall / MOTD / exec-on-message).
- Encrypted / private topics (lockbox-style confidential variant — pair-with-lockbox is the v1 escape hatch).
- Agent-side fleet-log cache (curator's `catalog.json` is the precedent; additive over the v1 REST-only read).
- Per-publisher block at the moderation layer (per-topic is sufficient until multi-writer exists).
- Retention floor "always keep last N messages regardless of age" (v1 is age-only).
