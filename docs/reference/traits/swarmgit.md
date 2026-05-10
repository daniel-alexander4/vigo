---
title: swarmgit
---

# swarmgit

Reports each envoy's local view of the unified-substrate swarmgit subsystem (ADR-015): which projects this envoy holds local state for, the bare-repo / head-ref state per project, and counts of revoke / leave envelopes that have propagated.

The agent walks every user's `~/.vigo-swarmgit/<project_id>/` and joins per-user state with the per-envoy bare repo at `/var/lib/vigo/swarm/swarmgit/projects/<project_id>.git` and the per-envoy envelopes store at `/var/lib/vigo/swarm/swarmgit/envelopes/<project_id>/`. Multiple users on the same envoy holding the same project collapse to one entry.

The server's `swarmgitmesh` aggregator joins these per-envoy reports across the fleet and renders the join at `/swarm/swarmgit` (UI) and `GET /api/v1/swarm/swarmgit/fleet` (REST).

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `project_count` | int | Number of distinct projects this envoy holds local state for. |
| `projects` | object[] | One entry per project. See below. |

### `projects[]`

| Field | Type | Description |
|---|---|---|
| `project_id` | string | 64-char lowercase hex `sha256(founder_puddle_pubkey ‖ handle)`. Stable cryptographic identity baked into the URL `swarmgit://<project_id>/<handle>`. |
| `handle` | string | Human-readable handle from `project.json`. Display only — not authoritative; two projects can share a handle. |
| `founder_puddle_pubkey` | string | 64-char lowercase hex of the founder puddle's Ed25519 pubkey. The TOFU anchor: `sha256(founder_puddle_pubkey ‖ handle) == project_id`. |
| `members` | string[] | Hex Ed25519 puddle pubkeys of every member puddle, in `project.json` order (founder first). |
| `dr_scope` | string | `"swarm-wide"` (replicas land on every swarmgit-authorized envoy in the swarm) or `"members-only"` (replicas only on member-puddle envoys). |
| `users_with_state` | string[] | Local Unix usernames whose `~/.vigo-swarmgit/<project_id>/` directory holds a `project.json` for this project. Multiple users on the same envoy collapse into one entry; this list names them. |
| `has_bare_repo` | bool | `true` when `/var/lib/vigo/swarm/swarmgit/projects/<project_id>.git` exists on this envoy. |
| `bare_repo_size_bytes` | int | Total bytes occupied by the bare repo (sum of every regular file under it). Zero when `has_bare_repo` is false. |
| `head_refs` | object | Map of `ref` → `sha`. Output of `git for-each-ref` against the bare repo. Empty when the bare repo is missing or contains no refs (initialized but no pushes yet). |
| `envelopes` | object | Aggregate state of revoke / leave envelopes for this project on this envoy. See below. |
| `leaf_role` | string | `"Admin"` or `"Member"` — the role granted by the leaf token in this user's chain. Helpful for operator triage when multiple users hold the same project at different roles. Picked from the first user observed; absent when no chain is loaded. |
| `token_chain_depth` | int | Length of the user's `tokens.json` chain (1 for a founder, ≥2 for invitees). |
| `token_expires_at` | int / null | Unix seconds when the leaf token expires, or `null` when the leaf has no expiry. |

### `projects[].envelopes`

| Field | Type | Description |
|---|---|---|
| `revokes` | int | Count of revoke envelopes persisted under `<swarmgit_base>/envelopes/<project_id>/revokes/`. |
| `leaves` | int | Count of leave envelopes persisted under `<swarmgit_base>/envelopes/<project_id>/leaves/`. |
| `digest_hex` | string | Hex-encoded 16-byte digest of the project's full envelope set. Used by the server-side mesh aggregator to flag projects where the envelope state has diverged across hosts (gossip in flight). |

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
  ]
}
```

## Empty state

The trait is `null` (not an object with empty `projects: []`) when:

- `/etc/passwd` cannot be read.
- No user has any `~/.vigo-swarmgit/<pid>/` state on this envoy.

The server-side aggregator treats a `null` trait the same as "no participation reported" — the envoy contributes nothing to the fleet view.

## Consumers

- `server/swarm/swarmgitmesh` — aggregates per-envoy reports into the fleet-wide `Fleet` shape (head-ref consensus, divergent-ref detection, envelope-digest unanimity).
- `GET /api/v1/swarm/swarmgit/fleet` — serves the aggregated view to admins and the `vigocli swarm swarmgit` CLI.
- `/swarm/swarmgit` UI page — renders the fleet view + per-project drill-down.

## See also

- [ADR-015 — Unified swarmgit substrate](../../adr/015-unified-swarmgit-substrate.md)
- [`vigo swarm swarmgit project ...`](../cli/agent-swarm-swarmgit.md) — envoy-side verb tree that mutates the state this trait reports.
- [`vigocli swarm swarmgit`](../cli/swarm-swarmgit.md) — admin-side CLI that consumes the aggregated trait.
