---
title: "ADR-024: curator — P2P artifact registry"
---

# ADR-024: curator — P2P artifact registry

**Status:** Implemented — slices 0–5 (catalog + helper + server REST + agent CLI + UI/docs) shipped 0.51.0 (2026-05-14); slice 6 (`artifact:` executor param) shipped 0.51.5 (2026-05-13); slice 7 (Docker Registry v2 shim) shipped 0.51.6 (2026-05-13); the `anchor` layer (curator's puddle-rooted-ownership cross-subsystem primitive, shared with gitback) extracted into `agent/src/swarm/anchor/` at 0.51.7 (2026-05-14); slice 8 (agent self-update accelerator — `vigo-agent` artifact convention, REST stays authoritative) shipped 0.51.8 (2026-05-13).
**Date:** 2026-05-11

## Context

The fleet has no first-class place to publish *artifacts* — release binaries, tarballs, `.deb`/`.rpm` packages, container images, the agent's own self-update binaries. Today they travel ad hoc: a `file:` resource with `source:` (ships in the config bundle — fine for small files, bad for binaries), `nonrepo_package` with a `url:` pointing at something the operator hosts elsewhere, or the server's static `/dist/agent/<os>/<arch>/…` for the agent binary. There is no fleet-internal container registry at all.

The pieces to build one already exist: the swarm blob substrate (`server/swarm/manifest.go` + `agent/src/swarm/{cache,manifest,scheduler,server,discovery}.rs`) is a content-addressed, chunked, P2P, target-scoped blob transport — exactly what filecast distributes files over; OCI image layers are already sha256-addressed and map 1:1 onto swarm blobs; release binaries / tarballs / packages are just blobs. And gitback (ADR-014/015/019) has the *identity and access* machinery: puddle-keyed ownership, Ed25519 capability/envelope signing anchored to `sha256(founder_pubkey ‖ handle)`, founder-rekey delegation chains, the `*.pending`-sentinel + root-reactor + envelope-gossip + digest-catch-up patterns, and the SO_PEERCRED helper-daemon pattern. So an artifact registry is **filecast's blob transport + the `anchor` layer's identity/access machinery + a thin catalog layer** — a fifth swarm content subsystem alongside filecast / gitback / longdrawer / lockbox. No new storage primitive. (The shared identity/access primitive — capability tokens, ADR-019 delegation envelopes, SO_PEERCRED helper — was extracted out of gitback into `agent/src/swarm/anchor/` once curator gave it a second consumer; the on-wire domain strings stayed byte-preserved to avoid invalidating every existing puddle membership and project chain.)

Constraints, settled in discussion (2026-05-11): **writable only by puddle members** (a publisher signs catalog entries with their puddle key; gated on disk by a usercrate `curator: bool` flag, like `gitback:`); **readable by the whole fleet** (the server resolves and relays — so even swarm-disabled envoys can pull); **public by default** (an artifact is fleet-readable unless its publisher narrows the recipient list); mutable tags; multi-arch; entirely P2P on the write side, with a deliberate server-mediated read side ("a case where server-side resolution is okay").

## Decision

### Name and placement

**`curator`** — the fifth swarm content subsystem. Agent module `agent/src/swarm/curator/` (helper daemon + materializer + catalog cache + trait collector + `vigo swarm curator` multicall verbs); server `server/swarm/curatormesh/` (catalog aggregator) + `server/api/swarm_curator.go` (REST: resolve, blob relay, catalog/fleet views, block); CLI `cli/swarm_curator.go` (`vigocli swarm curator`); config `swarm.curator.{enabled (hostname pattern list), max_bytes}` in `server/config/types.go` + `SwarmRuntimeLimits.curator_max_bytes`; usercrate `curator: bool`. The agent-facing REST endpoints follow the `/api/v1/agent/*` shape (mTLS is the auth) and **must be in `server/auth/auth.go`'s public-paths list** (see ADR-related fix 0.49.4 — the `fleet/pubkeys` / `puddle/names` 401 bug; don't repeat it).

### Identity and the catalog

An artifact is keyed `artifact_id = sha256(founder_puddle_pubkey ‖ name)` — same shape as a gitback `project_id`, so Alice's `foo` ≠ Bob's `foo`. The human-facing reference is `<founder-puddle-name>/<name>` (the puddle name resolves to the pubkey via the ADR-022 fleet name map, then to the artifact_id), with the **operator's puddle as the implicit default namespace** (`artifact:vigo-agent:latest` ≡ `artifact:<operator-puddle>/vigo-agent:latest`). The hex `<artifact_id>/<name>` form always works (self-certifying, like a gitback hex pid).

The **catalog entry** is one signed envelope per artifact (re-signed, with a higher `issued_at`, on every mutation; last-write-wins; domain prefix distinct from every other Vigo signed shape):

```
{ version, artifact_id, name, founder_puddle_pubkey,
  versions: { <semver-or-string>: { platforms: [ {os, arch, blob_sha, size}, … ] } },
  tags:     { <tag>: <version> },          # mutable
  recipients: ["*"] | [ <puddle_pubkey>, … ],   # "*" = fleet-readable (default)
  target:   <hostname-glob | trait-filter>,     # which envoys hold the P2P replica; default "*"
  issued_at, signature }                   # + the founder's delegation chain (ADR-019) when rekeyed
```

Re-signing the whole entry on every `push`/`tag`/`rm` is cheap (it's a list of versions × platforms + a small tag map) and sidesteps "how do I revoke version 3" — you re-publish the entry without it. The signature is the "writable only by puddle members" enforcement: the server's aggregator and any verifying envoy reject an entry whose signature doesn't anchor to `sha256(founder ‖ name) == artifact_id` (walking the delegation chain, exactly as `tokens::verify_chain` does for gitback). A founder cannot mutate another founder's artifact.

### Bytes

Each `{name, version, platform}` is a swarm blob (content-addressed by sha256). On `push`, the founder's envoy's curator helper daemon: stages the blob into the swarm blob cache + records it in the swarm `Manifest` CRDT with `target = <the artifact's target>` (so the existing filecast `Manager` distributes it to matching swarm-enabled envoys exactly like a filecast blob — the server's co-resident envoy gets it like any other); adds `{platform → blob_sha}` to the re-signed catalog entry; drops the entry as a `*.pending` sentinel → root reactor persists it, gossips it peer-to-peer (gitback envelope-gossip + digest-catch-up patterns), and emits the `swarm_curator` trait. Per-blob cap: `swarm.curator.max_bytes` (default 2 GiB, matching the post-0.49.3 gitback default; raise for container images).

### Write side — puddle members only

`vigo swarm curator push/tag/untag/set-recipients/set-target/rm/prune/gc/purge` (the multicall binary), runnable by a Unix user iff: (1) the host matches `swarm.curator.enabled`; (2) the user has `curator: true` in their usercrate (`/var/lib/vigo/usercrate-policy/<user>.json`); (3) the user has a puddle identity, unlocked (the helper daemon signs via the session helper). `vigocli swarm curator <mutating-verb>` is the operator's front-end onto the *same* helper-daemon socket (`/var/run/vigo/curator.sock` on the server host) — same verbs, same backend, two front-ends. Removing `curator: true` from a usercrate ⇒ that user can no longer publish (helper daemon refuses) and `purge`-scrubs their local publish state on the next reconcile; already-published artifacts stay (you can't recall them by losing a flag — same as gitback). `curator: true` is a heavier grant than `gitback: true` — "may publish to the fleet's registry, fleet-readable by default" vs "may found personal git repos." Full verb surface (founder side and operator side) is enumerated in the implementation notes.

### Read side — server-mediated, universal

Every envoy — swarm-enabled or not — resolves via the server: `GET /api/v1/swarm/curator/resolve?artifact=<ns/name>&tag=<t>&os=<os>&arch=<arch>` (or `&version=<v>`) → the server, from its aggregated catalog, resolves tag → version → the platform entry matching the requester → enforces `recipients` against the requesting envoy (`"*"` ⇒ allow; otherwise the envoy must host a user whose puddle ∈ `recipients` — the server already tracks puddle membership) → returns `{blob_sha, size, signed_entry}`. The agent then fetches the bytes: swarm-enabled ⇒ P2P (usually already cached — it holds the DR replica); swarm-disabled ⇒ `GET /api/v1/swarm/curator/blob/<sha>` from the server (the server has it via its co-resident envoy; this is the swarm-disabled envoy's only path and a fallback for everyone). Then verify the sha, materialize → `/var/lib/vigo/artifacts/<ns>/<name>/<version>/<os>-<arch>` and update `/var/lib/vigo/artifacts/cache.json` (`<ns>/<name>:<tag>` and `<ns>/<name>@<version>` → path). Agents also keep a cached copy of the catalog (`/var/lib/vigo/artifacts/catalog.json`, refreshed at startup + on a timer + on check-in, like the ADR-022 name map) so a swarm-enabled envoy can resolve + gate *locally* through a server outage. `vigocli swarm curator list/inspect/versions/resolve/fleet` are the operator views (`fleet` = who-holds-what, `RequirePermission(PermFleetRead)`); `vigocli swarm curator block/unblock <name>` is the operator-moderation backstop — the server stops resolving/relaying an artifact for the whole fleet regardless of who published it (the puddle-signed entry still exists and gossips, and the DR bytes still sit on swarm-enabled envoys, so only a publisher `rm` truly purges).

### Consumers — separate post-v1 slices

A `file:` resource can already point at `/var/lib/vigo/artifacts/…` paths directly. Beyond that, three integrations, each its own slice (not v1): an `artifact:<ns/name>:<tag>` (or `@<version>`) param on the `nonrepo_package` / `source_package` executors, resolving via `cache.json`; a Docker Registry v2 shim on `127.0.0.1:5000` (agent-side HTTP service, gitback-helper-daemon pattern, serving the OCI Distribution v2 subset over the artifact cache); an agent-self-update *accelerator* — pull `vigo-agent:<v>` P2P from the registry if it's cached locally, **falling back to the existing `/api/v1/agent/latest` REST path**, which stays the authoritative, always-available source (the registry never replaces it — the update mechanism must not depend on a P2P-gossiped, puddle-signed, recipient-gated catalog).

## Consequences / tradeoffs

- **"Restrict access" is materialization-gating, not byte-confidentiality, and it's per-envoy, not per-user.** A `--target '*'` artifact's raw bytes sit in `.blobs/<sha>` (root-owned) on every swarm-enabled envoy that holds the DR replica; the materialized form under `/var/lib/vigo/artifacts/…` is a `0644` file — once any authorized user on an envoy triggers materialization, every user on that envoy can read it. This matches gitback's stance (availability, not bytes-on-disk confidentiality — lockbox owns the latter). For true confidentiality, publish *encrypted* blobs (lockbox-style); out of scope for curator. Stated loudly in the docs.
- **"Publish to the full swarm but restrict access" = two orthogonal axes** — `target` (which envoys hold the P2P replica; `'*'` ⇒ everywhere) × `recipients` (which users may materialize/consume it; `'*'` ⇒ everyone). Like gitback's `dr_scope` × `members`. A restricted artifact still consumes disk on every `target`-matched envoy; narrow `target` if you don't want the replica everywhere.
- **The server owns artifact *resolution*.** A hard server outage stops new artifact pulls fleet-wide; the per-agent catalog cache only lets swarm-enabled envoys resolve against their last-known catalog. Accepted — the server is already required for config/policy/everything.
- **Fleet content (agent binaries, packages) inherits the *user* trust model** — it's keyed to *a* puddle (the operator's), survives an operator-puddle rekey only via the delegation chain (like a gitback `project_id`), and is un-republishable if that puddle is deleted. Fine for solo / single-operator setups; a multi-tenant / MSP world might want the operator-authority (filecast) model for fleet content — not pursued here.
- **Two CLI front-ends, one backend.** `vigocli` (operator, root, drives the server-host envoy's puddle) and `vigo` (founders, their own puddle) are both thin clients of `/var/run/vigo/curator.sock`; the duplication is shallow (one socket protocol). Not a hot path (publishes are ad hoc; materialization is convergence-time).

## Implementation notes

Open questions to settle during implementation:
1. **How `vigocli swarm curator <mutating-verb>` gets a puddle.** `vigocli` is `0700 root`; the puddle is per-user. Options: (a) an auto-provisioned **operator puddle** on the server host (unlocked from / derived via the secrets master key, so it's available whenever the server runs) — recommended; (b) relax `vigocli`'s perms for the curator subcommands; (c) `vigocli` is read-only for curator and all writes go through `vigo swarm curator` run as a real user. Pick (a) unless it proves awkward.
2. **The agent-facing catalog endpoint** — fully public (mTLS-only auth, like the name map; the catalog reveals which restricted artifacts exist and their recipients) vs delivered in the check-in response (per-envoy, no separate endpoint) vs `RequirePermission(PermFleetRead)` for the operator view + a leaner agent-facing variant. Lean: deliver via the check-in response (consistent with how filecast manifest entries reach agents); the operator's full `list`/`inspect` hits a fleet-read-gated endpoint.

### v1 slices

- **Slice 0** — this ADR + `_index.md`.
- **Slice 1** — catalog data model + signed-entry envelope (`agent/src/swarm/curator/catalog.rs`, signing reusing `anchor/tokens.rs`-style canonical bytes + `verify_chain`-style anchoring + ADR-019 delegations) + the `swarm_curator` trait + `server/swarm/curatormesh/` aggregator (rejects bad signatures).
- **Slice 2** — the agent helper daemon publish path (sign → stage blob into the swarm cache → `Manifest` record with the artifact target → `*.pending` sentinel → root reactor → trait emit) + `swarm.curator.{enabled, max_bytes}` config + usercrate `curator: bool` + `SwarmRuntimeLimits.curator_max_bytes` + `agent/src/swarm/runtime_limits.rs`.
- **Slice 3** — server REST (`resolve`, `blob`, catalog/`fleet`, `block`/`unblock`) + the agent-side resolver + materializer (resolve → fetch P2P-or-server → verify sha → materialize → `cache.json`) + the catalog cache.
- **Slice 4** — CLI: `vigocli swarm curator` (mutating verbs via the helper-daemon proxy + `list`/`inspect`/`versions`/`resolve`/`fleet` + `block`/`unblock`) and `vigo swarm curator` (mutating verbs `push`/`tag`/`untag`/`set-recipients`/`set-target`/`rm`/`prune`/`gc`/`stale`/`purge` + inspect `list`/`inspect`/`versions`/`resolve` + `pull` + `status`).
- **Slice 5** — `/curator` UI page (HTMX fragment, like `/gitback`) + docs (`docs/concepts/curator.md`, `do../reference/vigocli/{vigocli-swarm-curator,agent-swarm-curator}.md`, `docs/howto/set-up-curator.md`, `server-config.md`, CLAUDE.md Architecture) + version bump (Y bump — new subsystem) + home-lab verify (publish a multi-arch binary as a puddle user, see it land on matched envoys, pull it from a swarm-disabled envoy via the server, `block` it).

### Post-v1 slices

- **Slice 6** — `artifact:<ns/name>:<tag>` param on `nonrepo_package` / `source_package` (resolve via `cache.json`). **Shipped 0.51.5.** Implicit-namespace form (bare `<name>:<tag>`) resolves against the fleet's unique puddle-name claim; ambiguous (0 or 2+ claims) → tailored error pointing at the explicit `<puddle-name>/<name>:<tag>` form. Magic-byte sniffing for format detection — no extension on the materialized file.
- **Slice 7** — Docker Registry v2 shim on `127.0.0.1:5000`.
- **Slice 8** — agent-self-update accelerator (P2P-pull `vigo-agent:<v>` if cached, REST fallback).
- **Later** — per-artifact byte-confidentiality (publish encrypted blobs, lockbox-style); cross-publisher moderation beyond `block` (a server-side `force-rm` tombstone); the operator-authority (server-published, filecast-style) flavor for fleet content if a multi-tenant scenario needs it.
