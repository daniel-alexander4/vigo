---
title: gitback
---

# gitback

Reports each envoy's local view of the unified-substrate gitback subsystem (ADR-015): which projects this envoy holds local state for, the bare-repo / head-ref state per project, and counts of revoke / leave envelopes that have propagated.

The agent walks every user's `~/.vigo-gitback/<project_id>/` and joins per-user state with the per-envoy bare repo at `/var/lib/vigo/swarm/gitback/projects/<project_id>.git` and the per-envoy envelopes store at `/var/lib/vigo/swarm/gitback/envelopes/<project_id>/`. Multiple users on the same envoy holding the same project collapse to one entry. The collector also reads each user's `~/.vigo-puddle/name-claim.json` and reports the **friendly-name claim** (ADR-022) verbatim — see [`name_claims[]`](#name_claims) below.

The server's `gitbackmesh` aggregator joins these per-envoy reports across the fleet and renders the join at `/swarm/gitback` (UI) and `GET /api/v1/swarm/gitback/fleet` (REST); the name claims are aggregated into the fleet name map at `GET /api/v1/swarm/puddle/names` (consumed by `vigocli swarm puddle names list` and, on each agent, by the `git-remote-gitback` resolver for `gitback://<name>/<repo>` URLs).

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `project_count` | int | Number of distinct projects this envoy holds local state for. |
| `projects` | object[] | One entry per project. See below. |
| `name_claim_count` | int | Number of `~/.vigo-puddle/name-claim.json` files found across all users on this envoy. Omitted when 0. |
| `name_claims` | object[] | One entry per user's friendly-name claim. Omitted when empty. See [`name_claims[]`](#name_claims). |

> The trait is emitted whenever there's *either* gitback project state *or* a name claim — a user can have a `name_claims` entry with `project_count: 0`.

### `projects[]`

| Field | Type | Description |
|---|---|---|
| `project_id` | string | 64-char lowercase hex `sha256(founder_puddle_pubkey ‖ handle)`. Stable cryptographic identity baked into the URL `gitback://<project_id>/<handle>`. |
| `handle` | string | Human-readable handle from `project.json`. Display only — not authoritative; two projects can share a handle. |
| `founder_puddle_pubkey` | string | 64-char lowercase hex of the founder puddle's Ed25519 pubkey. The TOFU anchor: `sha256(founder_puddle_pubkey ‖ handle) == project_id`. |
| `members` | string[] | Hex Ed25519 puddle pubkeys of every member puddle, in `project.json` order (founder first). |
| `dr_scope` | string | `"swarm-wide"` (replicas land on every gitback-authorized envoy in the swarm) or `"members-only"` (replicas only on member-puddle envoys). |
| `users_with_state` | string[] | Local Unix usernames whose `~/.vigo-gitback/<project_id>/` directory holds a `project.json` for this project. Multiple users on the same envoy collapse into one entry; this list names them. |
| `has_bare_repo` | bool | `true` when `/var/lib/vigo/swarm/gitback/projects/<project_id>.git` exists on this envoy. |
| `bare_repo_size_bytes` | int | Total bytes occupied by the bare repo (sum of every regular file under it). Zero when `has_bare_repo` is false. |
| `head_refs` | object | Map of `ref` → `sha`. Output of `git for-each-ref` against the bare repo. Empty when the bare repo is missing or contains no refs (initialized but no pushes yet). |
| `envelopes` | object | Aggregate state of revoke / leave envelopes for this project on this envoy. See below. |
| `leaf_role` | string | `"Admin"` or `"Member"` — the role granted by the leaf token in this user's chain. Helpful for operator triage when multiple users hold the same project at different roles. Picked from the first user observed; absent when no chain is loaded. |
| `token_chain_depth` | int | Length of the user's `tokens.json` chain (1 for a founder, ≥2 for invitees). |
| `token_expires_at` | int / null | Unix seconds when the leaf token expires, or `null` when the leaf has no expiry. |

### `projects[].envelopes`

| Field | Type | Description |
|---|---|---|
| `revokes` | int | Count of revoke envelopes persisted under `<gitback_base>/envelopes/<project_id>/revokes/`. |
| `leaves` | int | Count of leave envelopes persisted under `<gitback_base>/envelopes/<project_id>/leaves/`. |
| `digest_hex` | string | Hex-encoded 16-byte digest of the project's full envelope set. Used by the server-side mesh aggregator to flag projects where the envelope state has diverged across hosts (gossip in flight). |

### `name_claims[]`

One entry per Unix user on this envoy who has run `vigo swarm puddle name set <name>` (ADR-022). The `claim` is the full signed `~/.vigo-puddle/name-claim.json` reported verbatim — the server publishes signed claims, never re-signs, and resolvers verify the signature + `supersedes` chain themselves. A claim whose signature doesn't self-verify is dropped here, not published.

| Field | Type | Description |
|---|---|---|
| `user` | string | Unix username whose `~/.vigo-puddle/` holds the claim. |
| `claim.body.puddle_pubkey` | string | 64-char lowercase hex of the puddle pubkey claiming the name. Resolving `gitback://<name>/<repo>` yields `project_id = sha256(puddle_pubkey ‖ <repo>)`. |
| `claim.body.name` | string | The claimed friendly name. Same character rules as a project handle, plus: must not look like a 64-char-hex project_id. |
| `claim.body.claimed_at` | int | Unix seconds the claim was signed. |
| `claim.body.supersedes` | object[] | Rekey-delegation chain (ADR-019 `DelegationEnvelope` shape) proving this pubkey descends from a previous pubkey that held the same name. Empty/absent for a first claim; populated by `re-found`. |
| `claim.signature` | string | 128-char hex Ed25519 signature over the canonical encoding of `body` (domain `gitback-name-v1\0`), made by `puddle_pubkey`. |

## Example

```jsonc
{
  "updated_at": 1777520400,
  "project_count": 1,
  "projects": [
    {
      "project_id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "handle": "vigo-web",
      "founder_puddle_pubkey": "8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8",
      "members": ["8711b12bac759fea30363f0ed4ab95b49b90b1b51c1cd4c095cb788cd0a9aea8"],
      "dr_scope": "swarm-wide",
      "users_with_state": ["dan"],
      "has_bare_repo": true,
      "bare_repo_size_bytes": 4096,
      "head_refs": {
        "refs/heads/main": "97debaead89e5f3a1c6b4e2e7cc7a9b3"
      },
      "envelopes": {
        "revokes": 0,
        "leaves": 0,
        "digest_hex": "f5b3a1c6b4e2e7cc7a9b3deafbeefcafe"
      },
      "leaf_role": "Admin",
      "token_chain_depth": 1,
      "token_expires_at": null
    }
  ],
  "name_claim_count": 1,
  "name_claims": [
    {
      "user": "dan",
      "claim": {
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

The trait is `null` (not an object with empty `projects: []`) when:

- `/etc/passwd` cannot be read, or
- no user has any `~/.vigo-gitback/<pid>/` state **and** no user has a `~/.vigo-puddle/name-claim.json`.

The server-side aggregator treats a `null` trait the same as "no participation reported" — the envoy contributes nothing to either the fleet gitback view or the fleet name map.

## Consumers

- `server/swarm/gitbackmesh` — aggregates per-envoy reports into the fleet-wide `Fleet` shape (head-ref consensus, divergent-ref detection, envelope-digest unanimity) and into the `FleetNames` map (`name → []claim`, deduped per puddle pubkey, collisions kept — the server publishes, never arbitrates).
- `GET /api/v1/swarm/gitback/fleet` — serves the aggregated gitback view to admins and the `vigocli swarm gitback` CLI.
- `GET /api/v1/swarm/puddle/names` — serves the fleet name map (public-by-construction, no token; same access posture as `/api/v1/fleet/pubkeys`). Consumed by `vigocli swarm puddle names list` and, on every agent, by the `puddle name-map bootstrap` loop which caches it at `/var/lib/vigo/swarm/puddle/names.json` for the `git-remote-gitback` resolver and the `vigo swarm puddle name set` collision check.
- `/swarm/gitback` UI page — renders the fleet view + per-project drill-down.

## See also

- [ADR-015 — Unified gitback substrate](../../../adr/015-unified-gitback-substrate.md)
- [ADR-022 — Friendly gitback names](../../../adr/022-gitback-friendly-names.md) — the `name_claims[]` half of this trait.
- [`vigo swarm gitback project ...`](../../vigocli/agent-swarm-gitback.md) — envoy-side verb tree that mutates the project state this trait reports.
- [`vigo swarm puddle name ...`](../../vigocli/agent-swarm-puddle.md) — envoy-side verb that writes the `name-claim.json` this trait reports.
- [`vigocli swarm gitback`](../../vigocli/swarm-gitback.md) — admin-side CLI that consumes the aggregated trait.
