
# vigo swarm poolq CLI Reference

`vigo swarm poolq` is the envoy-side surface for [poolq](../../howto/swarm/set-up-poolq.md) — the P2P ordered-log queue (ADR-029). Founders post messages; consumers read them with a per-user offset that survives across restarts and replays freely.

`vigo swarm poolq` is the **founder + consumer** side. The fleet-aggregator view and admin moderation (`block` / `unblock`) live on [`vigocli swarm poolq`](swarm-poolq.md). The mutating verb (`post`) deliberately is NOT on the vigocli side — it needs an unlocked per-user puddle to sign messages, which doesn't fit vigocli's root-only / peer-auth posture (same split that curator takes).

## Invocation

```
vigo swarm poolq <verb> [args]
```

The command lives inside the `vigo` agent binary. CLI verbs run as the calling user — root is **not** required and would discard the user's puddle identity (every signing operation needs `~/.vigo-puddle/` and the puddle session helper on `$XDG_RUNTIME_DIR/vigo/puddle.sock`).

**Usercrate authorization.** `post` refuses unless the invoking user has `poolq: true` on the user resource in their usercrate. The agent's user executor writes the resolved flags to `/var/lib/vigo/usercrate-policy/<user>.json` on each reconcile pass; the CLI reads it as a precondition. Both layers — envoy-level (`swarm.poolq.enabled` pattern list in `server.yaml`) and user-level (this flag) — must say yes.

`poolq: true` is a **heavier grant than `gitback: true`** — "may post to a fleet-readable ordered log" vs "may found personal git repos." Removing the flag scrubs `~/.vigo-poolq/` for that user on the next reconcile pass via `ScrubScope::Poolq`. Already-posted messages stay in the fleet view until they age out — you can't recall them by losing a flag.

Read verbs (`topics` / `subscribe` / `read` / `tail` / `seek` / `offset` / `status`) are ungated — messages are fleet-readable.

## Topic reference

Verbs that take a `<topic>` argument accept two forms:

1. **64-char lowercase hex `topic_id`** — always works; self-certifying like a curator `artifact_id` or a gitback `project_id`.
2. **Bare `<topic_name>`** — treated as the topic founded by THIS user. The CLI computes `topic_id = sha256(local_puddle_pubkey ‖ topic_name)`. Right shape for "I'm posting to my own topic" and "I'm reading the topic I founded." Cross-puddle reads use the hex form.

Friendly `poolq://<puddle-name>/<topic_name>` URLs land when the ADR-022 fleet name-map integration ships — same pre-0.49.0 papercut gitback had.

## Publish verb

### `vigo swarm poolq post <topic-name> --body <text>`

Post a message. The CLI:

1. Validates the topic name (1-64 chars, ASCII alphanumeric + `-` `_` `.`).
2. Validates the body against `swarm.poolq.max_msg_bytes` (16 KiB default) and the 64 KiB hard ceiling.
3. Computes `topic_id = sha256(local_puddle_pubkey ‖ topic_name)`.
4. Acquires the per-topic seq allocator at `~/.vigo-poolq/<topic_id>/allocator.json` under `flock(LOCK_EX)`, issues the next gap-free seq.
5. Builds canonical bytes (POOLQ_MSG_DOMAIN prefix), asks the puddle session helper to sign them.
6. Writes the signed message atomically to `~/.vigo-poolq/<topic_id>/messages/<msg_id>.json`.

Concurrent posts from the same user on the same envoy serialize cleanly on the flock.

```
$ vigo swarm poolq post build-tasks --body '{"task":"deploy"}'
Posted to a1b2c3d4e5f6/build-tasks as seq 17 (msg_id 7890abcdef12)
```

Variants:

```
vigo swarm poolq post build-tasks --body 'hello'
echo 'hello' | vigo swarm poolq post build-tasks --stdin
```

Errors:

- `puddle is not initialized for this user` — run `vigo swarm puddle init` (or `join --from <host>`).
- `could not sign with puddle identity` — run `vigo swarm puddle unlock`.
- `user X is not authorized to use poolq on this envoy` — add `poolq: true` to the user's usercrate.
- `message body is N bytes, over the operator-configured swarm.poolq.max_msg_bytes cap` — body is too big; pair with a curator artifact for larger payloads.

## Read verbs

### `vigo swarm poolq topics`

Every topic the fleet's `poolqmesh` aggregator knows about — name, head seq, count, short founder pubkey, short topic_id. `--json` for the raw response.

### `vigo swarm poolq subscribe <topic>`

Record a subscription with `offset_seq = 0` so `read` has a cursor to advance. Idempotent — re-subscribing keeps the existing offset; the `topic_name` field is updated from the bare-name resolution if it was empty.

State: `~/.vigo-poolq/subscriptions.json` (per-user, never replicated).

### `vigo swarm poolq read <topic> [--limit N]`

Fetch messages from `offset_seq + 1` to head, print them, advance the local offset to the last-read seq. If no new messages are available, prints "No new messages" and leaves the offset alone.

`--limit N` caps the response after the fetch (the offset advances by the truncated length, so a partial read leaves the rest queued for the next call).

`--json` prints the raw signed Message envelopes; the default rendering is a single-line `seq=N posted_at=… msg_id=… body=…` per message.

### `vigo swarm poolq tail <topic> [--limit N]`

Print the latest N messages (head − N + 1 to head) without touching the offset. Default `--limit 10`. v1 is one-shot — no continuous follow loop. Useful for inspection without consuming.

### `vigo swarm poolq seek <topic> <seq>`

Set the local offset so the next `read` returns from `<seq>`. `seek 0` rewinds the offset to 0 (next read returns from seq 1 — full replay). `seek 5` sets offset=4, next read returns from seq 5.

### `vigo swarm poolq offset <topic>`

Print the current offset for a subscription. Reports "no subscription" if the user hasn't subscribed.

### `vigo swarm poolq status`

One-line envoy posture: user, state dir, subscription count, server-configured runtime limits (`max_msg_bytes`, `retention_secs`).

## State layout

```
~/.vigo-poolq/
├── subscriptions.json                    # per-user consumer state (never replicated)
└── <topic_id_hex>/
    ├── allocator.json                    # founder-only, flock-protected seq counter
    └── messages/
        └── <msg_id_hex>.json             # one signed message per file
```

Trait collector walks this layout across every user on every check-in cycle.

## REST endpoints consumed

- `GET /api/v1/swarm/poolq/topics` — `topics`
- `GET /api/v1/swarm/poolq/topic/{topic_id}/head` — `tail` (head probe), `status`
- `GET /api/v1/swarm/poolq/topic/{topic_id}/range?from=N` — `read`, `tail`

All three are public-by-construction (mTLS is the transport guard); the agent's bootstrap client cert authenticates the call.
