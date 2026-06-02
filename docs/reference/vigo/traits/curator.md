---
title: curator
---

# curator

Reports each envoy's local view of the [curator](../../../howto/swarm/set-up-curator.md) P2P artifact registry (ADR-024): every signed catalog entry a local user has published, verbatim. The server's `curatormesh` aggregator joins these into the fleet catalog (keyed by `artifact_id`, last-write-wins on `issued_at`); the agent resolver and `vigocli swarm curator inspect` verify the signature + ADR-019 delegation chain end-to-end without trusting the server.

The agent walks every user's `~/.vigo-curator/<artifact_id>/entry.json` and drops anything whose directory name doesn't equal its own derived `artifact_id`, or whose signature/delegation chain doesn't verify. Same "server publishes, never arbitrates" stance the other content subsystems take. Returns `null` when no curator state exists.

## Fields

| Field | Type | Description |
|---|---|---|
| `updated_at` | int | Unix seconds when the trait was collected. |
| `artifact_count` | int | Number of valid signed catalog entries this envoy is reporting (post-dedup). |
| `artifacts` | object[] | One full signed `CatalogEntry` per artifact. See below. Sorted by `artifact_id` for deterministic output. |

### `artifacts[]`

Each entry is published verbatim — `body` plus its Ed25519 `signature`. Key `body` fields:

| Field | Type | Description |
|---|---|---|
| `body.name` | string | Artifact name. `artifact_id = sha256(founder_puddle_pubkey ‖ name)`. |
| `body.kind` | string | Artifact kind (e.g. `oci-archive`, `generic`). |
| `body.founder_puddle_pubkey` | string | 64-hex pubkey that founded the artifact (the TOFU anchor). |
| `body.signing_puddle_pubkey` | string | 64-hex pubkey that signed this entry (== founder unless re-signed via an ADR-019 delegation). |
| `body.versions` | object | `version → { platforms: [{ os, arch, blob_sha, size }] }`. |
| `body.tags` | object | `tag → version` (e.g. `latest → 1.2.0`). |
| `body.recipients` | string[] | Authorized pullers (`*` = fleet-wide). |
| `body.issued_at` | int | Unix milliseconds — the last-write-wins key for fleet merge. |
| `body.delegations` | object[] | ADR-019 re-sign delegation chain; empty for a founder-signed entry. |
| `signature` | string | 128-hex Ed25519 signature over the canonical body, verified against `signing_puddle_pubkey`. |

## When the trait is absent

`null` when no user on this envoy has published a curator artifact, `/etc/passwd` can't be read, or every candidate entry was dropped as misfiled/corrupt/non-verifying.

## See also

- [`vigocli swarm curator`](../../vigocli/swarm-curator.md) — the fleet catalog this trait feeds.
- [Set up curator](../../../howto/swarm/set-up-curator.md) · [ADR-024](../../../adr/024-curator.md).
- [`puddle`](puddle.md) — the identity curator artifacts are anchored to.
