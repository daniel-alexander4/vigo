# vigocli swarm puddle

Admin-side cross-envoy operations for the [puddle](../../engineering/subsystems/puddle.md) primitive (ADR-014, ADR-020, ADR-021, ADR-022). Most puddle lifecycle (`init`, `pair`, `join`, `unlock`, `lock`, `leave`, `rotate-passphrase`, `rekey-pair`, `rekey`, `name`) is per-user-per-envoy and lives on the agent CLI — see [`vigo swarm puddle`](agent-swarm-puddle.md). The verbs here are the admin-authority / fleet-wide ones.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `evict <host>` | Evict a host from a puddle fleet-wide (cross-envoy). Triggers lockbox peer-table cleanup + `recipients.txt` regeneration + a queued reencrypt on every remaining member so the evicted host can't decrypt future ciphertext. Authenticated end-to-end via mTLS + admin token through the server's TaskDispatch path; the evicted host doesn't need to cooperate. Per ADR-021 this is the canonical fleet-wide eviction verb; `vigocli swarm lockbox evict` delegates here. Flags: `--user <u>` (the puddle owner) **or** `--all-users`; `--yes` to skip confirmation. |
| `names list` | (ADR-022) Every claimed friendly puddle name across the fleet, one row per name. Collisions (a name claimed by more than one puddle) are flagged loudly; a `(re-found)` marker shows when a claim carries a `supersedes` chain. `--json` emits the raw fleet name map. Read-only — names are set/cleared on the envoy with [`vigo swarm puddle name set\|clear`](agent-swarm-puddle.md#friendly-names-adr-022). |

## Friendly names

`names list` consumes `GET /api/v1/swarm/puddle/names` — the fleet name map the server builds by aggregating every envoy's `swarmgit` trait (each user's `~/.vigo-puddle/name-claim.json`, reported verbatim). The server **publishes, never arbitrates**: a name with one claimant is unambiguous; a name with two or more is shown with all of them and flagged a collision (resolvers refuse it and require the hex `swarmgit://<project_id>/<repo>` form).

```bash
vigocli swarm puddle names list
```

```
Puddle names — fleet map generated 2026-04-23 18:24:11

NAME  PUDDLE PUBKEY                 CLAIMED     STATUS
acme  ed25519:8711b12bac75…0a9aea8  2026-04-12  ok
dan   ed25519:9f3acafe1234…c1d8abc1  2026-04-09  ok
ops   ed25519:11aa22bb33cc…ddeeff00  2026-04-15  COLLISION — 2 competing claims
ops   ed25519:99887766aabb…ccddeeff  2026-04-16

1 name(s) have competing claims; resolvers refuse those and require swarmgit://<project_id>/<repo>.
```

A friendly name is a hint, not a trust anchor — it's spoofable by squatting (first claim wins, including a malicious first-claimer). The unforgeable form is always `swarmgit://<project_id>/<repo>`. See [ADR-022](../../adr/022-swarmgit-friendly-names.md) for the full design + tradeoffs, and the envoy-side [Friendly names](agent-swarm-puddle.md#friendly-names-adr-022) section for setting/clearing one.

## See also

- [`vigo swarm puddle`](agent-swarm-puddle.md) — the per-user envoy-side verb tree (including `name set/clear/show`).
- [Puddle subsystem](../../engineering/subsystems/puddle.md) — design + code map.
- [ADR-021](../../adr/021-lockbox-lifecycle-on-puddle.md) — why `puddle evict` is the canonical eviction verb.
- [ADR-022](../../adr/022-swarmgit-friendly-names.md) — friendly names.
- [`vigocli swarm swarmgit`](swarm-swarmgit.md) — fleet-wide view of swarmgit projects (the things friendly names point at).
