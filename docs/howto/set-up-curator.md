
# Set up Curator

Curator is Vigo's P2P artifact registry — release binaries, tarballs, `.deb`/`.rpm` packages, container images. Operators publish artifacts from any envoy with an unlocked puddle; the fleet pulls them P2P (server-relayed when peer-to-peer can't reach). Read side is fleet-wide by default; write side is puddle-members-only.

## Enable it

Per-envoy: opt in via `server.yaml`:

```yaml
swarm:
  curator:
    enabled:
      - "*"                   # pattern list, first-match-wins; "-" prefix denies
    max_bytes: 2147483648     # per-blob cap; default 2 GiB
```

Per-user: grant `curator: true` on the usercrate (heavier than `gitback:` — write access to a fleet-readable artifact registry):

```yaml
resources:
  - type: user
    username: dan
    puddle: true              # required
    curator: true             # opt-in for curator publish
```

Publish (`sudo vigocli config publish`).

## Use it

From an envoy where the operator has puddle unlocked:

```bash
# Publish a binary or archive (the implicit namespace is the operator's puddle name):
vigo swarm curator push vigo-agent:1.2.3 ./vigo-agent-linux-amd64 \
  --platform linux/amd64

# Tag mutably:
vigo swarm curator tag vigo-agent:latest 1.2.3

# Resolve / pull anywhere (no puddle needed for read):
vigo swarm curator resolve vigo-agent:latest      # show coordinates
vigo swarm curator pull vigo-agent:latest         # fetch to /var/lib/vigo/artifacts/...

# Operator views:
vigo swarm curator list                           # everything published
vigo swarm curator inspect vigo-agent             # full signed entry
vigo swarm curator versions vigo-agent
```

From the admin server (`vigocli`) — same verbs plus moderation:

```bash
vigocli swarm curator list
vigocli swarm curator block <artifact_id>         # server refuses to resolve/relay
vigocli swarm curator unblock <artifact_id>
```

## How it works

The pieces to build a registry already existed in the fleet: the swarm blob substrate is content-addressed, chunked, target-scoped, and P2P; OCI image layers are already sha256-addressed and map 1:1 onto swarm blobs; release binaries / tarballs / packages are just blobs. Gitback already carried the *identity and access* primitives — puddle-keyed ownership, Ed25519 capability/envelope signing anchored to `sha256(founder_pubkey ‖ handle)`, founder-rekey delegation chains, the `*.pending`-sentinel + root-reactor pattern, and the SO_PEERCRED helper-daemon shape. Curator stitches the two together with a signed-catalog layer.

### Identity

An artifact is keyed `artifact_id = sha256(founder_puddle_pubkey ‖ name)` — same shape as a gitback `project_id`, so Alice's `redis` ≠ Bob's `redis`. The human-facing reference is `<founder-puddle-name>/<name>` (the puddle name resolves to the pubkey via the [ADR-022](../adr/022-gitback-friendly-names.md) fleet name map, then to the artifact_id), with **the operator's puddle as the implicit default namespace** — `artifact:vigo-agent:latest` ≡ `artifact:<operator-puddle>/vigo-agent:latest`. The hex `<artifact_id>/<name>` form always works (self-certifying, like a gitback hex pid).

## Catalog entry

One signed envelope per artifact, re-signed last-write-wins on every mutation:

```jsonc
{
  "body": {
    "schema_version": 1,
    "name":                    "vigo-agent",
    "founder_puddle_pubkey":   "<hex 32-byte ed25519 pubkey>",   // anchor, frozen forever
    "signing_puddle_pubkey":   "<hex 32-byte ed25519 pubkey>",   // == founder until rekey
    "versions": {
      "1.2.3": { "platforms": [
        { "os": "linux",  "arch": "amd64", "blob_sha": "<hex>", "size": 12345678 },
        { "os": "linux",  "arch": "arm64", "blob_sha": "<hex>", "size": 12345990 },
        { "os": "darwin", "arch": "arm64", "blob_sha": "<hex>", "size": 13402011 }
      ]}
    },
    "tags":       { "latest": "1.2.3", "stable": "1.2.0" },   // mutable
    "recipients": ["*"],                                       // "*" = fleet-readable (default)
    "target":     "*",                                         // which envoys hold the DR replica
    "issued_at":  1714521600000,                               // unix ms; LWW key
    "delegations": []                                          // ADR-019 rekey-bridge chain
  },
  "signature": "<hex 64-byte ed25519 sig over canonical_bytes(body)>"
}
```

The signature **is** the "writable only by puddle members" enforcement: the server's aggregator and any verifying envoy reject an entry whose signature doesn't anchor (walking the ADR-019 delegation chain) to the pubkey that `artifact_id == sha256(that_pubkey ‖ name)`. A founder cannot mutate another founder's artifact.

Re-signing the whole entry on every `push`/`tag`/`rm` is cheap (versions × platforms + a small tag map; kilobytes, not megabytes) and sidesteps "how do I revoke version 3" — you just re-publish the entry without it.

## Two orthogonal axes

Distribution and access are independent:

| Axis | Default | Meaning |
|---|---|---|
| `target:` | `"*"` (everywhere) | Which swarm-enabled envoys hold the P2P DR replica. A hostname glob or `key=value` trait filter (the `match_target` dialect; same shape filecast uses). |
| `recipients:` | `["*"]` (fleet-readable) | Which users may *materialize/consume* the artifact. `"*"` ⇒ everyone; a list of hex puddle pubkeys gates per puddle. |

The `recipients` field is **currently informational** at the resolve gate — every envoy can `pull` every artifact (Dan's call, 2026-05-12). Per-recipient enforcement is held for the post-v1 encrypted-blob feature; the field stays in the catalog model so a slice 6+ encrypted-blob mode lights it up without a schema change.

A restricted-`recipients` artifact still consumes disk on every `target`-matched envoy. Narrow the `target` if you don't want the replica everywhere.

## Bytes

Each `{name, version, platform}` is a swarm blob (content-addressed by sha256). On `push`:

1. The founder's envoy hashes the file (`sha256` + size), drops it into the curator helper daemon at `/var/run/vigo/curator.sock` along with the re-signed catalog entry.
2. The daemon (root) validates the entry's signature + delegation chain end-to-end, confirms the named build's `blob_sha`/`size` match the streamed bytes, and enforces the per-blob cap `swarm.curator.max_bytes` (default 2 GiB; raise for container images).
3. The daemon stages the blob into the swarm blob cache (`/var/lib/vigo/swarm/.blobs/<sha>`) and records a swarm `Manifest` CRDT entry with `target = <the artifact's target>` — so the existing filecast `Manager` distributes it to matching swarm-enabled envoys **exactly like a filecast blob**.
4. The daemon drops the signed entry as `~<user>/.vigo-curator/<artifact_id>/entry.json.pending`. The root sentinel reactor (60 s sweep) re-verifies it and promotes it to `entry.json`, where the `swarm_curator` trait collector picks it up → `curatormesh` → the fleet catalog.

Re-signs that don't ship new bytes (`tag`/`untag`/`set-recipients`/`set-target`/`rm`) ride the same socket via the `ENTRY` verb (header-only, no blob stream). For `set-target` the daemon also re-records the manifest seed for every blob **this envoy** seeded so the filecast Manager redistributes to the new envoy set on its next pass (peer-seeded blobs are left untouched — no silent ownership transfer).

## Read side — server-mediated, universal

Every envoy — swarm-enabled or not — resolves via the server:

```
GET /api/v1/swarm/curator/resolve
    ?artifact_id=<hex>
    &tag=<t>|&version=<v>
    &os=<os>
    &arch=<arch>
```

The server resolves tag → version → the platform entry from its aggregated catalog (30-second TTL cache; block/unblock invalidates it), checks the operator-block table, and returns `{blob_sha, size, signed_entry}`. The agent then fetches the bytes:

- **Swarm-enabled envoy** — P2P via the existing swarm cache (usually already holds the DR replica because `target` matched it).
- **Swarm-disabled envoy** — `GET /api/v1/swarm/curator/blob/<sha>` from the server, which relays from its co-resident envoy's blob cache. Scoped: a sha that no current (non-blocked) catalog entry references is refused, so this isn't a back door to arbitrary swarm blobs.

Either way, the agent verifies the sha against the bytes, materializes to `/var/lib/vigo/artifacts/<artifact_id>/<name>/<version>/<os>-<arch>` (mode 0644), and updates `/var/lib/vigo/artifacts/cache.json` with both `<aid>/<name>:<tag>` and `<aid>/<name>@<version>` aliases pointing at the path.

Agents also keep a cached copy of the fleet catalog at `/var/lib/vigo/artifacts/catalog.json`, refreshed every check-in. A swarm-enabled envoy can therefore resolve + materialize **locally** through a server outage for any artifact it already holds.

## Authorization

Two layers gate publishing. Both must say yes:

| Layer | Where | Question |
|---|---|---|
| Envoy | `server.yaml#swarm.curator.enabled` pattern list | Is curator available on this envoy at all? |
| User | usercrate `curator: bool` flag, cached at `/var/lib/vigo/usercrate-policy/<user>.json` | May this Unix user publish? |

Plus the operator-puddle layer at signing time: the user's puddle session helper at `$XDG_RUNTIME_DIR/vigo/puddle.sock` must be running (`vigo swarm puddle unlock`).

**`curator: true` is a heavier grant than `gitback: true`** — "may publish to the fleet's registry, fleet-readable by default" vs "may found personal git repos." Removing `curator: true` from a usercrate triggers a scrub on the next reconcile (`ScrubScope::Curator` wipes `~/.vigo-curator/` for that user). Already-published artifacts stay — you can't recall them by losing a flag (same as gitback).

Reading is **ungated** — artifacts are fleet-readable. `vigo swarm curator status/list/inspect/versions/resolve/pull` work for any user (`pull` materializes into a root-owned tree, so it requires `sudo`).

The operator-moderation backstop is `vigocli swarm curator block <ref>` (admin-only). The server stops resolving/relaying the blocked artifact for the whole fleet regardless of who published it. The signed catalog entry still exists and gossips, and the DR bytes still sit on swarm-enabled envoys — only a publisher `vigo swarm curator rm` truly purges.

## CLI surfaces

Two thin clients of the same backend:

- **`vigo swarm curator`** — the founder + consumer surface, runs as the user. Mutating verbs (`push` / `tag` / `untag` / `set-recipients` / `set-target` / `rm`) sign via the puddle session helper and submit to `/var/run/vigo/curator.sock`. Read verbs (`status` / `list` / `inspect` / `versions` / `resolve` / `pull`) work locally + against the server. Cleanup verbs (`stale` / `prune` / `gc` / `purge`) hygiene the envoy.
- **`vigocli swarm curator`** — the operator front-end, runs against the server's REST surface. Read views (`status` / `list` / `show` / `versions` / `resolve`) plus admin moderation (`block` / `unblock`). The agent-side verb name `inspect` is renamed to `show` here to match the gitback / lockbox / longdrawer sibling convention; the REST endpoint stays `/api/v1/swarm/curator/inspect/{aid}` (it's an internal URL). Per [ADR-024 open Q#1 resolved 2026-05-13](../adr/024-curator.md#implementation-notes), mutating verbs deliberately do NOT live on this surface — they need an unlocked per-user puddle, which doesn't fit vigocli's root-only / peer-auth posture. If a server-host operator-puddle ever lands, those verbs join here.

See the [vigo swarm curator CLI reference](../reference/vigocli/agent-swarm-curator.md) and [vigocli swarm curator CLI reference](../reference/vigocli/swarm-curator.md) for the full verb walkthroughs.

## Wire surface

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /api/v1/swarm/curator/resolve` | public (mTLS only) | Resolve `<artifact_id>` + `<tag\|version>` + `<os>/<arch>` → `{blob_sha, size, signed_entry}` |
| `GET /api/v1/swarm/curator/blob/{sha256}` | public (mTLS only) | Relay artifact bytes from the server's co-resident envoy. Scoped to curator artifact shas only. |
| `GET /api/v1/swarm/curator/list` | `fleet.read` | Catalog + operator-block table |
| `GET /api/v1/swarm/curator/inspect/{artifact_id}` | `fleet.read` | One signed entry + block status |
| `GET /api/v1/swarm/curator/versions/{artifact_id}` | `fleet.read` | Versions + tags map |
| `GET /api/v1/swarm/curator/fleet` | `fleet.read` | Participants + artifact-count rollup |
| `POST /api/v1/swarm/curator/block/{artifact_id}` | admin | Operator-block an artifact (optional `?reason=`) |
| `POST /api/v1/swarm/curator/unblock/{artifact_id}` | admin | Reverse a prior block |
| Agent helper daemon `/var/run/vigo/curator.sock` | SO_PEERCRED + usercrate `curator: true` + entry signature | `PUSH` (with blob), `ENTRY` (header-only re-sign) |

The agent-facing endpoints follow `/api/v1/swarm/...` and are public-by-construction (mTLS is the auth) so swarm-disabled envoys can resolve + pull too — same stance the [ADR-022](../adr/022-gitback-friendly-names.md) fleet name map takes.

## State layout

```text
On the publisher's home:
  ~<user>/.vigo-curator/<artifact_id_hex>/
      entry.json                ← canonical signed CatalogEntry (root reactor writes after re-verifying)
      entry.json.pending        ← freshly-published entry the helper daemon dropped, awaiting promotion

On every agent (root-owned):
  /var/lib/vigo/swarm/.blobs/<blob_sha>     ← raw bytes (shared with filecast/gitback/lockbox)
  /var/lib/vigo/artifacts/
      catalog.json              ← cached fleet catalog from the server (per-check-in refresh)
      cache.json                ← materialization index: <aid>/<name>:<tag> and @<version> → path
      <artifact_id>/<name>/<version>/<os>-<arch>   ← materialized 0644 file (hardlink from .blobs/, copy across filesystems)
```

## Slice history (ADR-024 implementation)

| Slice | Commits | What landed |
|---|---|---|
| 0 | `4f341029` | The ADR itself. |
| 1 | `5d3f3c4f` (1a), `8d4de7db` (1b), `920f9ffd` (1c) | Catalog data model + signed-entry envelope + `swarm_curator` trait collector + `server/swarm/curatormesh/` aggregator. |
| 2 | `49007232` (2a), `0b568fb0` (2b) | `swarm.curator.{enabled,max_bytes}` config + usercrate `curator: bool` + `SwarmRuntimeLimits.curator_max_bytes` (proto field 8) + helper-daemon `PUSH` verb + sentinel reactor. |
| 3 | `27f0f893` (3a.1), `39a5fb60` (3a.2), `fc05ce77` (3b) | `CheckInResponse.curator_catalog` + feed reactor in `curatormesh`→`ManifestStore`; server REST (`resolve`/`blob`/`list`/`inspect`/`versions`/`fleet`/`block`/`unblock`) + `curator_blocked_artifacts` table; agent `catalog_cache.rs` + `resolver.rs` + `cache.json` materializer. |
| 4 | `14627ae4` (4a), `056c3e44` (4b), `41093673` (4c), `edfcad26` (4d) | `vigocli swarm curator` read + moderation; `vigo swarm curator` read verbs; mutating verbs + helper-daemon `ENTRY` verb (set-target refreshes manifest seeds we own); local cleanup (stale/prune/gc/purge) + 30 s TTL cache on `curatormesh.Aggregator.Snapshot` (invalidated by block/unblock). |
| 5 | 0.51.0 | Docs (this page + CLI references) + `/curator` UI page + Y version bump + home-lab verify. |

## Documented limits

- **"Restrict access" is materialization-gating, not byte-confidentiality, and it's per-envoy not per-user.** A `target: "*"` artifact's raw bytes sit in `.blobs/<sha>` (root-owned) on every swarm-enabled envoy that holds the DR replica; the materialized form under `/var/lib/vigo/artifacts/…` is mode `0644` — once any authorized user on an envoy triggers materialization, every user on that envoy can read it. This matches gitback's stance (availability, not bytes-on-disk confidentiality — lockbox owns the latter). For true confidentiality, publish *encrypted* blobs (lockbox-style); out of scope for curator v1.
- **The server owns artifact *resolution*.** A hard server outage stops new artifact pulls fleet-wide; the per-agent catalog cache only lets swarm-enabled envoys resolve against their last-known catalog. Accepted — the server is already required for config/policy/everything.
- **Fleet content (agent binaries, packages) inherits the *user* trust model.** It's keyed to *a* puddle (the operator's), survives an operator-puddle rekey only via the [ADR-019](../adr/019-gitback-founder-rekey.md) delegation chain, and is un-republishable if that puddle is deleted. Fine for solo / single-operator setups; a multi-tenant / MSP world might want the operator-authority (filecast-style) model for fleet content — not pursued here.
- **Two CLI front-ends, one backend.** The duplication is shallow — one socket protocol — and only the agent surface mutates. Operator mutations are post-v1 (per [ADR-024](../adr/024-curator.md#implementation-notes) open question #1).

## Operability

Curator is gated per-envoy via the `swarm.curator.enabled` pattern list in `server.yaml` (default-deny; empty = disabled). Both `swarm.enabled` AND `swarm.curator.enabled` must allow the host. Per-blob cap is `swarm.curator.max_bytes`, default 2 GiB; raise for large container images.

```yaml
swarm:
  enabled: ["*"]
  curator:
    enabled: ["annlap", "plex", "girlslaptop"]
    max_bytes: 5368709120   # 5 GiB
```

The destructive lever is the **usercrate `curator: bool` flag** (a heavier grant than `gitback: bool`). Removing it scrubs `~/.vigo-curator/` for that user on the next reconcile pass. Pattern-list flips are pause/resume — already-held catalog entries and bytes are preserved. See [swarm Operability](set-up-swarm.md#operability--pattern-list-authorization).

## Consuming artifacts from modules (slice 6, 0.51.5+)

Modules can reference curator artifacts directly via the `artifact:` param on the [`nonrepo_package`](../reference/vigo/resources/nonrepo_package.md) and [`source_package`](../reference/vigo/resources/source_package.md) executors — no need to manually `pull` first. The executor resolves the artifact, fetches + verifies the bytes (curator's signed `blob_sha` is the integrity guarantee), and installs from the materialized path.

```yaml
# .deb / .rpm / .pkg / .msi
- type: nonrepo_package
  name: my-app
  artifact: alexander4/my-app:latest    # explicit namespace
  state: present

# Bare form — uses the implicit namespace (the fleet's unique puddle name claim).
- type: nonrepo_package
  name: my-app
  artifact: my-app:latest
  state: present

# Tarball / archive
- type: source_package
  name: my-tool
  artifact: alexander4/my-tool@v3       # @ for literal version
  target_path: /usr/local/bin/my-tool
```

The `artifact:` form accepts:

- `<name>:<tag>` — implicit namespace, resolves to whichever puddle has claimed a name in this fleet (errors if 0 or 2+ names are claimed)
- `<puddle-name>/<name>:<tag>` — explicit puddle namespace via the [ADR-022](../adr/022-gitback-friendly-names.md) fleet name map
- `<artifact_id>/<name>:<tag>` — fully self-certifying, 64-hex artifact_id
- `@<version>` instead of `:<tag>` — pin to a literal catalog version

OS/arch auto-detect from the running envoy (`linux`/`darwin` × `amd64`/`arm64`, GNU-style). Format auto-detect from magic bytes — no extension needed since the materialized file lives at `/var/lib/vigo/artifacts/<aid>/<name>/<version>/<os>-<arch>`.

Mutually exclusive with `url:`. `sha256:` alongside `artifact:` is redundant (curator already verified) and rejected at validate time.

## Post-v1

| Slice | Feature |
|---|---|
| 6 | `artifact:<ns/name>:<tag>` param on `nonrepo_package` / `source_package` executors — **shipped in 0.51.5**; see [the section above](#consuming-artifacts-from-modules-slice-6-0515) |
| 7 | Docker Registry v2 shim on `127.0.0.1:5000` (agent-side HTTP service serving the OCI Distribution v2 subset over the artifact cache) |
| 8 | Agent-self-update *accelerator* — P2P-pull `vigo-agent:<v>` from the registry if cached, **falling back to the existing `/api/v1/agent/latest`** which stays the authoritative source |
| Later | Per-artifact byte-confidentiality (publish encrypted blobs, lockbox-style); cross-publisher moderation beyond `block` (a server-side `force-rm` tombstone); the operator-authority (server-published, filecast-style) flavor for fleet content if a multi-tenant scenario needs it |

## Related

- [ADR-024 — curator P2P artifact registry](../adr/024-curator.md) — the foundational design (identity, catalog, wire protocol, slicing, open questions)
- [`vigo swarm curator` CLI reference](../reference/vigocli/agent-swarm-curator.md) — founder + consumer verb walkthroughs
- [`vigocli swarm curator` CLI reference](../reference/vigocli/swarm-curator.md) — operator read + moderation verb walkthroughs
- [Swarm](set-up-swarm.md) — the underlying P2P substrate curator rides on
- [Gitback](set-up-gitback.md) — the puddle-keyed access primitive curator reuses
- [Puddle](set-up-puddle.md) — the per-user identity that anchors a curator artifact
- [Filecast](set-up-swarm.md) — the admin-pushed sibling for *one-off* file fan-out (curator is for named, versioned, mutable-tag artifacts)

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../legal/license.md).*
