---
title: puddle
---

# puddle

Reports each envoy's local view of every [puddle](../../../howto/set-up-puddle.md) — the per-user swarm identity (ADR-014) that gitback, curator, poolq, lockbox, and longdrawer all build on. This trait is the **sole reporter** of puddle identity and state: the current pubkey, the friendly-name claim (ADR-022), and the local health signals. Sibling traits only *reference* puddle pubkeys (gitback's `founder_puddle_pubkey` / `members`, curator/poolq founders, lockbox recipients) — they never re-report them.

The agent walks every user in `/etc/passwd` and, for each one whose `~/.vigo-puddle/identity.pub` is present, emits one entry. Trait collection runs as root, so per-user "is the session unlocked?" is read from the home-rooted `~/.vigo-puddle/session.unlocked` sentinel (written by the session helper, removed on lock) — a genuinely per-user answer without the uid-bound helper socket.

The server's `puddlemesh` aggregator groups these per-(envoy, user) entries by pubkey into the fleet puddle topology + health at `/swarm/puddle` (UI) and `GET /api/v1/swarm/puddle/fleet` (REST), and owns the fleet friendly-name map at `GET /api/v1/swarm/puddle/names`.

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `puddle_count` | int | Number of users on this envoy with an initialized puddle. |
| `puddles` | object[] | One entry per user's puddle. See below. |

### `puddles[]`

| Field | Type | Description |
|---|---|---|
| `user` | string | Unix username whose `~/.vigo-puddle/` holds this identity. |
| `puddle_pubkey` | string | 64-char lowercase hex of the puddle's Ed25519 pubkey (from `identity.pub`, normalized). The key `puddlemesh` groups by across envoys. |
| `initialized` | bool | `true` when the wrapped key + salt + pubkey are all present. |
| `session_unlocked` | bool | `true` when the `session.unlocked` sentinel is present — the session helper holds the unwrapped key in memory on this envoy. |
| `pair_pending` | bool | `true` when a `pair.pending` sentinel is present — a 5-minute pairing window is open (ADR-014). |
| `rekey_pending` | bool | `true` when a `rekey.pending` sentinel is present — a key rotation is in progress (ADR-018). |
| `retired_count` | int | Number of retired keypairs logged in `retired_puddles.json` (how many times this puddle has rotated). |
| `name_claim` | object | The full signed friendly-name claim (ADR-022) reported verbatim, or omitted when the user has claimed no name. A claim whose signature doesn't self-verify is dropped, not published. See below. |

### `puddles[].name_claim`

| Field | Type | Description |
|---|---|---|
| `body.puddle_pubkey` | string | 64-char lowercase hex of the puddle pubkey claiming the name. Resolving `gitback://<name>/<repo>` yields `project_id = sha256(puddle_pubkey ‖ <repo>)`. |
| `body.name` | string | The claimed friendly name. Same character rules as a project handle, plus: must not look like a 64-char-hex project_id. |
| `body.claimed_at` | int | Unix seconds the claim was signed. |
| `body.supersedes` | object[] | Rekey-delegation chain (ADR-019 `DelegationEnvelope` shape) proving this pubkey descends from a previous pubkey that held the same name. Empty/absent for a first claim; populated by `re-found`. |
| `signature` | string | 128-char hex Ed25519 signature over the canonical encoding of `body` (domain `gitback-name-v1\0`), made by `puddle_pubkey`. |

## Example

```jsonc
{
  "updated_at": 1777520400,
  "puddle_count": 1,
  "puddles": [
    {
      "user": "dan",
      "puddle_pubkey": "8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8",
      "initialized": true,
      "session_unlocked": true,
      "pair_pending": false,
      "rekey_pending": false,
      "retired_count": 0,
      "name_claim": {
        "body": {
          "puddle_pubkey": "8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8",
          "name": "dan",
          "claimed_at": 1777520400
        },
        "signature": "1f2e…(128 hex chars)…a90"
      }
    }
  ]
}
```

## Empty state

The trait is `null` (not an object with empty `puddles: []`) when `/etc/passwd` cannot be read or no user on this envoy has initialized a puddle. The aggregator treats a `null` trait as "no puddle reported".

## Consumers

- `server/swarm/puddlemesh` — groups per-(envoy, user) entries by pubkey into the fleet `Fleet` shape (owner name, member envoys, health band) and owns the `FleetNames` map (`name → []claim`, deduped per puddle pubkey, collisions kept — the server publishes, never arbitrates). `gitbackmesh` and the curator S3 facade consume the name map.
- `GET /api/v1/swarm/puddle/fleet` — serves the aggregated puddle topology + health to admins and the `vigocli swarm puddle list/show` CLI.
- `GET /api/v1/swarm/puddle/names` — serves the fleet name map (public-by-construction, no token; same access posture as `/api/v1/fleet/pubkeys`). Consumed on every agent by the `puddle name-map bootstrap` loop which caches it at `/var/lib/vigo/swarm/puddle/names.json` for the `git-remote-gitback` resolver and the `vigo swarm puddle name set` collision check.
- `/swarm/puddle` UI page — renders the fleet view + per-puddle drill-down.

## See also

- [ADR-014 — Puddle identity](../../../adr/014-puddle-primitive.md)
- [ADR-022 — Friendly gitback names](../../../adr/022-gitback-friendly-names.md) — the `name_claim` half of this trait.
- [`gitback`](gitback.md) — references puddle pubkeys (`founder_puddle_pubkey` / `members`) but no longer reports identity.
- [`vigo swarm puddle ...`](../../vigocli/agent-swarm-puddle.md) — envoy-side verb tree that creates the identity + name claim this trait reports.
- [`vigocli swarm puddle`](../../vigocli/swarm-puddle.md) — admin-side CLI that consumes the aggregated trait.
