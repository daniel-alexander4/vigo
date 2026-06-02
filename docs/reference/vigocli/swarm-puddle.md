# vigocli swarm puddle

Fleet view and admin-side cross-envoy operations for the [puddle](../../howto/set-up-puddle.md) primitive (ADR-014, ADR-020, ADR-021, ADR-022) — the per-user identity that gitback, curator, poolq, lockbox, and longdrawer all build on. Most puddle lifecycle (`init`, `pair`, `join`, `unlock`, `lock`, `leave`, `rotate-passphrase`, `rekey-pair`, `rekey`, `name`) is per-user-per-envoy and lives on the agent CLI — see [`vigo swarm puddle`](agent-swarm-puddle.md). The verbs here are fleet observability + admin authority.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `list` | One row per puddle across the fleet: owner (its uncontested friendly name, ADR-022, or `(unnamed)`/`(contested)`), short pubkey, envoy count, users, unlocked state, and health (`healthy`/`warning`/`degraded`). `--json` emits the full list. Read-only. |
| `show <name\|pubkey>` | Detailed view of one puddle — resolved by uncontested owner name or a pubkey prefix. Prints owner, full pubkey, users, health band + reasons, retired-key count, and a per-envoy table (host, user, status, initialized, unlocked, pair/rekey flags). `--json` emits the detail. |
| `evict <host>` | Evict a host from a puddle fleet-wide (cross-envoy). Triggers lockbox peer-table cleanup + `recipients.txt` regeneration + a queued reencrypt on every remaining member so the evicted host can't decrypt future ciphertext. Authenticated end-to-end via mTLS + admin token through the server's TaskDispatch path; the evicted host doesn't need to cooperate. Per ADR-021 this is the canonical fleet-wide eviction verb; `vigocli swarm lockbox evict` delegates here. Flags: `--user <u>` (the puddle owner) **or** `--all-users`; `--yes` to skip confirmation. |

## Fleet view

`list` / `show` consume `GET /api/v1/swarm/puddle/fleet` — the puddle topology the server builds by aggregating every envoy's `puddle` trait (each user's `~/.vigo-puddle/` identity, name claim, and health signals). A puddle's **owner** is its uncontested friendly name; a name claimed by more than one puddle is **contested** (the resolver refuses it and requires the hex `gitback://<project_id>/<repo>` form, and the puddle's health drops to a warning).

```bash
vigocli swarm puddle list
```

```
OWNER         PUBKEY                  ENVOYS  USERS  UNLOCKED  HEALTH
dan           ed25519:9f3acafe1234…   3       dan    yes       healthy
acme          ed25519:8711b12bac75…   2       ops    —         warning
(contested)   ed25519:11aa22bb33cc…   1       ops    —         warning

3 puddle(s) · 5 envoy(s) · 2 named · 1 unlocked · 2 need attention
```

Health bands: **healthy** (≥1 reporting, initialized holder; name uncontested; nothing mid-rotation), **warning** (contested name, a rekey in progress, or some holders stale), **degraded** (no holder currently reporting, or not initialized anywhere reporting). Reachability uses the same trait-report freshness rule as the other swarm mesh views.

A friendly name is a hint, not a trust anchor — it's spoofable by squatting (first claim wins, including a malicious first-claimer). The unforgeable form is always `gitback://<project_id>/<repo>`. The raw signed name claims are still served at `GET /api/v1/swarm/puddle/names` (the agent name-cache bootstrap consumes it). See [ADR-022](../../adr/022-gitback-friendly-names.md) for the full design + tradeoffs, and the envoy-side [Friendly names](agent-swarm-puddle.md#friendly-names-adr-022) section for setting/clearing one.

## See also

- [`vigo swarm puddle`](agent-swarm-puddle.md) — the per-user envoy-side verb tree (including `name set/clear/show`).
- [Puddle subsystem](../../howto/set-up-puddle.md) — design + code map.
- [ADR-021](../../adr/021-lockbox-lifecycle-on-puddle.md) — why `puddle evict` is the canonical eviction verb.
- [ADR-022](../../adr/022-gitback-friendly-names.md) — friendly names.
- [`vigocli swarm gitback`](swarm-gitback.md) — fleet-wide view of gitback projects (the things friendly names point at).
