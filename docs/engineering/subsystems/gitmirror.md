
# Gitmirror — Personal-DR Git Mirroring

Gitmirror auto-mirrors a small number of a user's personal git repos from one **source** envoy to a bounded handful of **receiver** envoys where the same user has an account. The trigger is `git commit`, not `git push`. Receivers are read-only mirrors that hold a checked-out working tree plus a separate bare repo, so a user who loses their primary machine can `git clone` off any mirror to recover their work.

Gitmirror is a personal-disaster-recovery tool, not a fleet content distribution mechanism. Fleet-wide content distribution is what modules and `file:` resources are for. Gitmirror carries history.

Gitmirror is one of four content subsystems that ride the [swarm](swarm.md) substrate. The server plays no role in selection or delivery — gitmirror reuses swarm's mTLS peer channel (port 1531) for bundle pushes and swarm's multicast group (`224.0.0.42:1531`) for LAN discovery via `GITMIRROR:` announcement lines.

## Status

As of **0.23.0**, gitmirror is a fully peer-to-peer subsystem. The server plays no role in selection, delivery, or acceptance — the `server/gitmirror/` package and the `CheckInResponse.gitmirror_claims` proto field were deleted in Phase 8 of the P2P-first rearchitecture. All mirror discovery, selection, and transport runs directly between envoys.

| Piece | Status |
|-------|--------|
| Source-side reactor + bundle publish | ✅ |
| LAN peer discovery (swarm multicast `GITMIRROR:` line) | ✅ Phase 5 |
| LAN deterministic selector + `GITMIRRORCLAIM:` announcements | ✅ Phase 6 |
| Cross-subnet `cross_subnet_mirrors:` direct push (`POST /gitmirror/push/<label>`) | ✅ Phase 7 |
| `gitmirror` trait collector (operator visibility) | ✅ |
| Receiver-side materialization (`agent/src/gitmirror/receiver.rs`) | ✅ |
| `vigo swarm gitmirror <verb>` recovery CLI | ✅ |
| `vigo swarm gitmirror prune` manual stale-cleanup | ✅ |
| Home-lab e2e verification | ⏳ (requires `git >= 2.35` on source host) |

## How It Works

1. **Default on, per envoy.** Gitmirror runs on every enrolled envoy by default. Operators deny specific hosts via hostname globs in `swarm.gitmirror.blacklist` in server.yaml. The server emits `gitmirror_blacklisted: true|false` in `PolicyBundle.vars_json` on every check-in; the agent re-reads the cached bundle and flips a shared runtime gate. Blacklisted envoys never announce themselves and never accept pushed mirrors; adding/removing a host takes effect within one check-in cycle. Adding a host also auto-dispatches `_apply-blacklist` which wipes existing mirrors locally.
2. **Opt in, per repo.** A user creates a git repo at `~<user>/gitmirror/<project>/` and commits a `.gitmirror.yaml` sidecar. The sidecar names the source envoy's hostname, how many mirrors to maintain, optional LAN filter globs, and optional explicit `cross_subnet_mirrors:` cross-subnet targets.
3. **Discover.** Every 5 seconds, the agent's gitmirror reactor scans every local user's `~/gitmirror/*` directory, parses each sidecar with a strict deny-unknown-fields validator, and excludes anything whose `owner` field doesn't match this envoy's hostname.
4. **Bundle.** For each non-paused source whose `HEAD` has advanced since the last publish, the reactor runs `git bundle create` (as the owning Unix user, never as root) into `/var/lib/vigo/swarm/gitmirror/tmp/`. The first bundle is full; subsequent bundles are incremental from the last published commit. Whether a given bundle was full or incremental — and its size — is persisted in `state.json` and surfaced through the `gitmirror.sources[].last_publish_full` / `last_bundle_size` trait fields so the `/payload` UI and `vigocli swarm gitmirror` can explain why a seed is only a few KB (an incremental is expected to be small; a full bundle should be repo-sized).
5. **Publish as swarm seed.** The source writes a content seed `gitmirror:<host>:<user>:<project>` into the swarm seed directory. Gitmirror content seeds are flagged as **claim-only** in the manifest (label prefix `gitmirror:`), so the standard target-spec self-assignment path explicitly skips them.
6. **LAN discovery.** Every gitmirror-enabled envoy announces itself on the existing swarm multicast channel with a `GITMIRROR:<host>:<users>:<tags>` line (Phase 5). Every source envoy builds an in-memory peer table from those announcements.
7. **LAN selection.** For each non-paused source, the reactor filters the peer table by the sidecar's `include_envoys`/`exclude_envoys` + owner-user-exists check, runs deterministic `sha256(label||host)` selection, picks the top `max_mirrors`, and writes the result to a shared `PendingClaims` cache (Phase 6).
8. **LAN claim announcement.** The swarm announcer emits one `CAIRNCLAIM:<label>:<host1,host2,...>` line per source per multicast cycle (rate-limited to changes + 60s safety re-announce). LAN receivers parse the line, check whether their own hostname is in the list, and call `ClaimsHandle::ingest_lan_claim` to mark the label as wanted.
9. **Cross-subnet push.** For each source with a non-empty `cross_subnet_mirrors:` list, the reactor opens a mTLS connection to `https://<target>:1531/gitmirror/push/<label>` and POSTs the bundle body with `X-Vigo-Seeder` and `X-Vigo-Blob-Sha256` headers (Phase 7). The target's swarm peer server validates the request, writes the blob to its local cache, injects a manifest entry, and calls `ingest_lan_claim` — same final path as a LAN-discovered mirror.
10. **Materialize.** The receiver-side reactor diffs `ClaimsHandle::snapshot()` against currently-materialized state, finds each newly-claimed content seed in the local swarm cache, unbundles into a root-owned bare repo at `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git`. On the next tick the cycle can re-run: a new bundle triggers an incremental unbundle; a claim whose LAN announcement stops arriving ages out after the 90-second TTL; stale mirrors (`now - last_refreshed > 30 days`) surface as warnings and in the `gitmirror.stale_mirrors` trait but are **never auto-evicted**. Manual cleanup is available via `vigo swarm gitmirror prune`.

The server is absent from every step. It never runs `git`, never downloads bundle bytes, never picks mirrors, never delivers claim lists. The `gitmirror.sources[]` trait is still emitted for operator observability but no server component consumes it — it's a read-only window onto source-side state.

## The `.gitmirror.yaml` Sidecar

Every synced repo must have a `.gitmirror.yaml` file at its root. This is mandatory opt-in: a `.git/` directory under `~<user>/gitmirror/` without a sidecar is left alone. Operators can have personal git work there that gitmirror never touches.

### Authoring: CLI, not hand-editing

The supported way to create, edit, pause, and remove sidecars is the `vigo swarm gitmirror source` CLI (see [CLI reference](../../reference/cli/agent-swarm-gitmirror.md)):

- `vigo swarm gitmirror source add <path>` writes a validated sidecar with the full schema enforced at write time, not at reactor-tick time.
- `vigo swarm gitmirror source pause|resume <project>` flips the `paused` field without rewriting the file by hand.
- `vigo swarm gitmirror source show|validate` surface parse errors on demand.

Hand-editing the YAML still works — the reactor doesn't care where the file came from — but the CLI catches mistakes that hand-editing silently lets slip through (unknown fields, missing mandatory keys, out-of-range `max_mirrors`, overlong descriptions). The on-disk format remains the single source of truth so a sidecar committed to a git repo rides along with clones.

```yaml
owner: my-laptop
max_mirrors: 5
tags: [personal, dotfiles]
paused: false
description: "shell config + editor setup"

# Optional candidate filters — omit entirely for "mirror to any
# eligible peer on the LAN". Evaluated on the source envoy during
# LAN selection (Phase 6); `cross_subnet_mirrors:` targets bypass filters
# entirely.
include_envoys:
  - "laptop-*"
  - "*.office.example.com"
exclude_envoys:
  - homeserver
when: "tag('work')"
```

| Field | Required | Validation |
|-------|----------|------------|
| `owner` | yes | DNS hostname `[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)*`, lowercased |
| `max_mirrors` | yes | int, clamped into `[3, 10]` with a warning if out of range |
| `tags` | yes | list of ≤ 16 strings, each ≤ 32 chars (may be `[]`) |
| `paused` | yes | bool — `true` skips bundle creation, existing seeds retention-expire |
| `description` | yes | string ≤ 256 chars (may be `""`) |
| `include_envoys` | no | list of ≤ 16 hostname glob patterns, each ≤ 253 bytes. When non-empty, a candidate must match at least one. Omit for pass-all. |
| `exclude_envoys` | no | list of ≤ 16 hostname glob patterns, each ≤ 253 bytes. Applied after `include_envoys` — matches are dropped. |
| `when` | no | `when:`-expression string ≤ 512 bytes. See "Candidate filters" below for the builtin set. Empty means no filter. |

Strict parsing: missing mandatory fields, unknown fields, malformed YAML, duplicate keys, or files larger than 8 KB are all rejected. The three filter fields are strictly opt-in — an existing sidecar that predates them continues to parse unchanged. The same validator runs on both the source agent (Rust) and the server (Go) — drift between them would let a malformed sidecar smuggle through one side, so they're kept in lockstep.

### Candidate filters

The LAN selector on the source envoy walks its in-memory peer table (populated from swarm multicast `GITMIRROR:` announcements) and applies the sidecar's filters in this exact order:

1. **Owner user must exist on the peer** — the peer's announced `users` list must contain the repo's `owner`. A repo owned by `dan` can only mirror on envoys where `dan` is a local account.
2. **`exclude_envoys`** — blacklist. Envoys matching any glob are dropped.
3. **`include_envoys`** — whitelist. If the list is non-empty, an envoy must match at least one glob to stay in the pool. Empty or absent is "pass all".

Hostname glob syntax is a simple wildcard matcher supporting `*` (any chars) and `?` (one char), case-insensitive. A malformed glob matches nothing (never accidentally "everything").

**`when:` expressions over full traits are NOT supported in LAN mode.** The multicast announcement frame only carries hostname/users/tags, so richer filtering (e.g. `trait_eq('os.family', 'linux')`) can't be evaluated by the source envoy. Sources that need trait-level filtering should use explicit `cross_subnet_mirrors:` entries for those targets instead.

## Mirror Counts

`MIN_MIRRORS = 3` and `MAX_MIRRORS = 10` are immutable constants. Operators can request anything in `[3, 10]` via the sidecar's `max_mirrors` field; values outside that range are clamped with a warning.

`MIN_MIRRORS` is a **target, not a gate**. If the LAN peer pool has fewer than 3 eligible envoys, gitmirror picks whatever is available. Hard cap enforcement runs in both directions: the LAN selector uses `min(max_mirrors, len(candidates))` on the source side, and `MAX_MIRRORS_PER_RECEIVER = 50` + `MAX_PER_SOURCE_MIRRORS = 20` are enforced by receivers on first materialization.

## Receiver Layout

Receivers hold each mirror as a single **root-owned bare repo** at `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git`. There is no working tree at `~<user>/gitmirror/` — the receiver never touches user-space. The bare repo is mode `0755` root-owned, so any user on the system can `git clone` it read-only, but only the agent (running as root) can update it.

Recovery is a deliberate user action, not a passive filesystem shadow. When a user needs to get their repo back, they run:

```
vigo swarm gitmirror recover <project>
```

which clones the bare repo into `~<user>/gitmirror/<project>/` as a normal writable git repo with full history. See [`vigo swarm gitmirror` CLI reference](../../reference/cli/agent-swarm-gitmirror.md) for the full recovery subcommand tree.

**Why root-owned bare repo, not a user-owned mirror.** The bare repo is operator data managed by the convergence system; the user should not be able to damage it by accident. User-owned files would also mean the agent has to drop privileges for every update, which was the cause of the 0.21.24 → 0.21.25 regression (root-owned `GIT_DIR` + non-root `git checkout` fails at `index.lock`). Root-owned + on-demand clone via `vigo swarm gitmirror recover` eliminates the ownership split from the hot path entirely.

**What recovery gives you.** A real git repo at a writable path, with all tags, the full commit log, and a `.git/` directory. It's indistinguishable from having cloned the original source. From that point forward the recovered repo is yours — subsequent gitmirror updates on the source side do not automatically propagate to your recovered clone, because it's no longer part of the receiver pipeline.

## Source Identity and Security

Gitmirror does not have its own authentication layer. Security is provided by two existing layers stacked together:

1. **Swarm transport security** — every byte moves over mTLS on the existing swarm peer port (1531). An envoy can only be a seeder if it's enrolled and approved. Content integrity is guaranteed by SHA256 verification of every assembled blob (`agent/src/swarm/cache.rs`).
2. **Server-mediated seeder→label verification** — every label embeds the seeder hostname as its second segment (`gitmirror:<seeder>:<user>:<project>`). The *server* validates the match against authoritative fleet data in `server/gitmirror/builder.go::OnTraitReport` before it ever issues a claim: if an envoy reports a `gitmirror.sources[]` entry whose label claims a different seeder than the envoy's own hostname in the fleet index, the entry is rejected. Receivers then trust the claim list because it arrives over the existing mTLS-authenticated check-in channel and has already been server-validated. They never act on raw manifest entries.

Note that the check is server-mediated rather than receiver-side because peer-to-peer blob downloads deliberately disable TLS hostname verification (`agent/src/swarm/peer_client.rs::build_peer_agent` calls `disable_verification(true)`) — peers connect by IP and the cert SAN is hostname-based, so the wire identity is unavailable to the downloader. Content integrity still comes from SHA256; identity comes from the fleet index.

Note that the sidecar's `owner` field is **not** a security boundary. It's a single-writer hint that prevents two envoys from accidentally both believing they're the source for the same repo. The actual security boundary is the server's seeder→label verification above.

## State Storage

| Path | Owner | Contents |
|------|-------|----------|
| `/var/lib/vigo/swarm/gitmirror/state.json` | `root:root` 0600 | publisher: per-label `{git_sha, blob_sha}` so restarts don't re-publish identical bundles. Root-only because no user-facing command reads it. |
| `/var/lib/vigo/swarm/gitmirror/traits.json` | `root:root` 0644 | latest reactor pass snapshot for the `gitmirror` trait collector. World-readable so `vigo swarm gitmirror source list` (an unprivileged-user command) can read it without sudo — contents are non-sensitive (labels, owner usernames, sidecar config, git/blob shas) and already flow through server trait reports and the swarm manifest. |
| `/var/lib/vigo/swarm/gitmirror/tmp/` | mode `1777` (sticky like `/tmp`) | scratch directory for `git bundle create` output before atomic move into the swarm seed dir |
| `/var/lib/vigo/swarm/gitmirror/receiver_state.json` | `root:root` 0644 | receiver: per-label `{blob_sha, git_sha, next_gc_at, ...}` + `pending_wipes` for the hysteresis window. World-readable so `vigo swarm gitmirror {list,show,log,branches,peers}` can read it without sudo — the bare repo layout under `mirrors/` is already world-traversable, so the file merely serializes what the filesystem reveals. |
| `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git` | `root:root` mode `0755` | bare receiver repo, readable by any local user (so `vigo swarm gitmirror recover` can clone it without privilege), writable only by the agent. The `mirrors/` subdirectory keeps bare repos namespaced away from publisher state files so the receiver's orphan-cleanup walker has a clean boundary |
| `~<user>/gitmirror/<project>/` | `<user>:<user>` | **source side**: operator-created working repo with `.git/`, containing `.gitmirror.yaml`. **receiver side**: does not exist — the receiver never touches user-space. Users recover into this path on demand via `vigo swarm gitmirror recover <project>`, which creates a real writable git clone |

State files survive agent restarts. The tmp directory is GCed on agent startup for any `*.bundle` older than 1 hour (interrupted bundle operations).

If `receiver_state.json` is missing, corrupt, or has a stale schema version, the receiver rescans `mirrors/<seeder>/<user>/*.git` at startup and re-adopts every bare repo whose `git rev-parse HEAD` resolves cleanly. Rescanned entries are recorded with an empty `blob_sha`, which forces a single incremental fetch against the current manifest entry on the next reactor tick — git reuses the existing packs, so bandwidth is minimal. This recovery path is what keeps a corrupt state file from silently deleting every mirror via the orphan cleanup walker.

## Activation

**Gitmirror is default-on.** Every enrolled envoy participates unless its hostname matches a glob in `swarm.gitmirror.blacklist` in server.yaml:

```yaml
# server.yaml
swarm:
  gitmirror:
    blacklist:
      - "raspberrypi"
      - "*.test.example.com"
```

Adding a host to the blacklist does two things on the next check-in cycle:

1. The agent receives `gitmirror_blacklisted: true` in `PolicyBundle.vars_json` and flips its shared runtime gate OFF — multicast announcer, `/gitmirror/push` handler, and reactor all idle on the next sample point.
2. The server dispatches the hidden agent verb `vigo swarm gitmirror _apply-blacklist` via `TaskDispatch` to the affected envoy. That verb wipes every locally-held mirror regardless of seeder and persists the cleared `receiver_state.json`. Symmetric with cryptsync's `_apply-evict` for receiver-side eviction.

Removing a host from the blacklist re-enables the gate but does not auto-replicate previous bundles. The next reactor tick on the publishers picks fresh receivers; existing publishers that previously had the un-blacklisted envoy in their `selected_mirrors` will deterministically re-select it via the sha256 score, restoring the mirror at the next bundle publish.

The gate state arrives through the normal check-in path: the server renders per-envoy gates into `PolicyBundle.vars_json` and the agent caches the bundle in LMDB for offline convergence. The agent re-reads the cached `gitmirror_blacklisted` after every accepted policy bundle. Default-open semantics: any ambiguous case (no bundle yet, parse failure, missing field) is treated as "not blacklisted", so a fresh-enroll runs gitmirror immediately.

### Fleet observability: the `/gitmirror` page

The `gitmirror` trait carries two cross-envoy fields that together let the server reconstruct the whole mesh without any central coordination:

- `sources[].selected_mirrors` — on the publisher side, the LAN hostnames the reactor most recently chose for this label. A snapshot of `pending_claims` at trait-write time.
- `mirrors[]` — on the receiver side, the materialized bare repos this envoy holds. One entry per (label, seeder, user, project) with the latest `git_sha` / `blob_sha` / `last_refreshed` / `stale`.

The server's `server/gitmirrormesh` package reads both fields from every envoy's latest trait row and joins by label, computing per-cell status (`active` = selected + materialized, `pending` = selected + not yet materialized, `orphan` = materialized but publisher no longer selects). The REST endpoint is `GET /api/v1/swarm/gitmirror/fleet`; the result is cached for 30 seconds so repeated UI hits don't re-parse every envoy's trait blob.

`/gitmirror` is a dedicated page for the gitmirror mesh, separate from the payload (admin-distributed payloads) page that shows the underlying blobs:

- `/gitmirror` list — fleet-wide view of every gitmirror repo. Each row's primary identifier is the project (repo) name; the publisher hostname sits in the next column. The full `gitmirror:<host>:<user>:<project>` label is never shown in the UI — it's an internal addressing key, not an operator-facing identifier.
- `/gitmirror/{label}` detail — page heading is the project name; a Publisher / User metadata table and sidecar config (max_mirrors, tags, description, cross_subnet_mirrors, git HEAD) sit below, followed by a per-mirror status table with last-refreshed and status pill per host.
- `/payload` — admin-distributed payloads only. Gitmirror bundles are filtered out of both the list and the detail view; requests to `/payload/{gitmirror_bundle_sha}` 303 to `/gitmirror/{label}`.

Nothing on the server is authoritative for mesh state — the truth still lives in each agent's memory + multicast announcements. The trait is a periodic projection that makes the P2P state observable without breaking it.

OFF→ON transitions run the activation sequence (cleanup of orphan bundles, reset of per-label publisher state, git version check) on the next reactor tick, so the first publish after enable emits a full bundle rather than a delta whose base commit no receiver has. ON→OFF transitions clear the local LAN peer table and pending-claims map immediately — a deactivated envoy doesn't hold stale state.

### Preconditions (must also hold for the reactor to start)

1. This envoy's hostname is not matched by any glob in `swarm.gitmirror.blacklist`.
2. `git` is installed at version ≥ 2.35 (`git_ops::check_version()` rejects older hosts at startup).
3. At least one user has a `~<user>/gitmirror/<project>/` directory containing a valid `.gitmirror.yaml` — this enables publishing; receive-only envoys need only an unblocked gate.

### Fresh-enrollment behavior

A just-enrolled envoy has no cached policy bundle yet. Default-open semantics treat that as "not blacklisted", so gitmirror starts in the ON state immediately. The first check-in delivers the canonical `gitmirror_blacklisted` bit; if the envoy actually is blacklisted, the gate flips OFF on that tick and the reactor stops on the next pass.

## Limits

All caps are immutable constants in `agent/src/gitmirror/mod.rs`. None can be overridden by configuration, environment variable, CLI flag, or sidecar value. The philosophy: the numbers are large enough that tuning is unnecessary for the intended use case, and configurability is where complexity breeds.

| Limit | Value | Rationale |
|-------|-------|-----------|
| `MAX_BUNDLE_SIZE` | 20 GiB per bundle | Publisher refuses to publish larger bundles; receiver refuses to materialize them. Git bundles carry history, not working-tree contents — 20 GiB is absurdly high for a personal repo and exists only to bound runaway behavior. |
| `MAX_MIRRORS_PER_RECEIVER` | 50 | Total distinct mirrors a single receiver will hold across all sources. Implicit storage ceiling is `MAX_BUNDLE_SIZE × 50 = 1 TiB`, but the realistic homelab case is 1-5. Cap exists to catch runaway behavior. |
| `MAX_PER_SOURCE_MIRRORS` | 20 | Mirrors one source envoy can register on a single receiver. Stops a single compromised source from filling the whole `MAX_MIRRORS_PER_RECEIVER` budget. |
| `MIN_MIRRORS` | 3 | Target, not gate — under-replicated emits a trait but doesn't fail. |
| `MAX_MIRRORS` (sidecar) | 10 | Hard cap on the `max_mirrors:` sidecar field. |
| `STALE_THRESHOLD_SECS` | 30 days | Mirrors not refreshed in this window are flagged as stale (log warning + trait). **Never auto-evicted.** See "Staleness and manual pruning" below. |
| Sidecar size | 8 KB |
| Description length | 256 chars |
| Tags | 16 entries, 32 chars each |
| Project name length | 64 bytes |
| Reactor tick | 5 seconds (also the commit debounce window) |
| Receiver `git gc --auto` jitter window | 6 hours per repo |
| Receiver wipe hysteresis | 15 minutes after a label drops out of the receiver's `ClaimsHandle` |

**Disk-pressure guardrail.** `cross_subnet_mirrors::push_bundle` probes each target at `GET /health/storage?for=gitmirror` before running sha256 or opening the bundle file — a 20 GiB bundle push into a 9%-free peer would land the filesystem at zero, so we skip before burning cycles. The receiver's `/gitmirror/push/<label>` handler re-checks `/var/lib/vigo/swarm/gitmirror` on entry and returns **507 Insufficient Storage** before streaming the body. LAN claim delivery rides the swarm pull substrate, so its guardrail lives in `swarm::cache::check_disk_space` (size-reserve AND ≥10% free — either gate trips aborts the download). Probes are cached 60 s per (peer, purpose) so the 5 s reactor tick doesn't hammer the health endpoint. See [Disk Space in swarm operations](../../operations/swarm.md#disk-space).

## LAN peer discovery (Phase 5)

Gitmirror-enabled envoys announce themselves to each other on the existing swarm multicast channel (`224.0.0.42:1531`). Every regular swarm announcement frame gains one optional line:

```text
GITMIRROR:<hostname>:<users-csv>:<tags-csv>:<ed25519_pubkey_hex>
```

The trailing `ed25519_pubkey_hex` (64 chars) is the sender's Ed25519 verifying key, loaded from `/var/lib/vigo/agent.key` (the same keypair used for gRPC mTLS authentication). It pairs with a `SIG:<unix_ts>:<hex64>` line appended as the final line of every announcement; the signature covers the whole packet body above the SIG line, preventing both tamper and replay. Pre-0.30.11 envoys (3-field `GITMIRROR:` lines without pubkey, no SIG line) are not accepted by 0.30.11+ receivers — fleets must upgrade atomically.

Listeners ingest this line into an in-memory `PeerTable` keyed by canonicalized hostname. Entries carry the hostname, the set of human Unix users present on the peer (read from `/etc/passwd` at the peer's agent startup, filtered to UID ∈ [1000, 65534)), and a tag list sourced from the agent's traits. Entries expire after **5 minutes** without a refresh; the gitmirror reactor's tick prunes stale entries on every pass, so the table stays bounded without a separate sweeper.

**Default-on gate:** every enrolled envoy announces the `GITMIRROR:` line unless it's blacklisted via `swarm.gitmirror.blacklist`. A blacklisted envoy stops broadcasting, so it never lands in any peer's table — "presence in the table" IS the participation signal. There's no separate capability bit to check.

**Graceful overflow:** if the swarm announcement is close to the 1400-byte UDP MTU ceiling, the `GITMIRROR:` line is dropped from that announcement (not truncated). The next announcement cycle typically lands 5 seconds later and other peers refresh their TTL then. Worst case: a peer table entry ages up to 5 minutes behind reality, which is well within the TTL window.

**Filtering** happens on the source side. When a source envoy needs to pick mirrors for a repo, it snapshots the peer table and applies the sidecar's `include_envoys` / `exclude_envoys` hostname globs plus the repo's `owner` field — the peer must have the owning user in its local `users` list. `when:` expressions over full traits are **not supported in LAN mode** (the multicast frame only carries hostname/users/tags). Sources that need trait-level filtering should use the explicit `cross_subnet_mirrors:` list in the sidecar (Phase 7) for those targets instead.

**No cross-subnet reach yet.** Phase 5 is LAN-only: multicast stays on the broadcast domain. Cross-subnet `cross_subnet_mirrors` direct-push lands in Phase 7.

### Signature verification + TOFU cache (0.30.11+)

Every gitmirror announcement is signed by the sender's Ed25519 key. Receivers:

1. Extract the `SIG:<ts>:<hex>` line from the packet. Missing SIG = reject the packet's `GITMIRROR:` + `GITMIRRORCLAIM:` content (swarm `HAVE:`/`WANT:` lines are still honored — those aren't security-critical).
2. Decode the `GITMIRROR:` line's embedded pubkey. Verify the signature against it using `auth::signer::verify_payload` (SHA256(body || timestamp), matches the Go agent's signing convention).
3. Cross-check the pubkey against a **trust-on-first-use (TOFU) cache** at `/var/lib/vigo/swarm/gitmirror/peer_pubkeys.json`. First sighting of a hostname caches the key; subsequent announcements must match. Mismatch rejects the packet with a `warn!` pointing at the cache file.

**Admin recovery.** A legitimate pubkey rotation (e.g., re-enrolling an envoy) looks like a mismatch to receivers. Resolution: operators delete `peer_pubkeys.json` on each receiver, which re-TOFUs on the next announcement. A log entry flagging the mismatch tells operators exactly what to do.

**Threat coverage.** Protects against on-LAN spoofing of `GITMIRRORCLAIM:` (an attacker can't announce `gitmirror:danlap:dan:vigo:attacker-host` without `danlap`'s private key) and replay of stale announcements (timestamp is in the signed payload). The remaining window is a "race-to-first-announcement" attack — mitigation ships in a later phase via server-distributed pubkey bootstrap.

## LAN claim selection and announcement (Phase 6)

Once source envoys know about their LAN peers (Phase 5), they pick which ones should mirror each repo and tell them. The selection is deterministic: given the same peer table + sidecar, every source envoy produces the same answer.

### Selection algorithm

For each candidate peer `P` and target label `L`:

```text
score(L, P) = sha256( BE_u32(len(L)) || L || 0x00 || hostname(P) )
```

Peers are sorted by score ascending (lexicographic over the 32 bytes), with hostname as a stable tie-breaker. The top `min(max_mirrors, len(filtered))` are selected. The length-prefix framing prevents collisions where a label and hostname differ in where the split falls but would produce the same concatenated bytes.

**Candidate filter** (same as Phase 5): for a repo owned by `dan`:

1. Exclude any peer matching an `exclude_envoys` glob.
2. If `include_envoys` is non-empty, keep only peers matching at least one entry.
3. Require `"dan"` to be in the peer's `users` list.

Tags and `when:` expressions over full traits are intentionally not supported in LAN mode — richer filtering is deferred to the explicit `cross_subnet_mirrors:` list (Phase 7).

### Announcement shape

Source envoys publish their selection as one multicast line per label, appended to the existing swarm announcement frame:

```text
CAIRNCLAIM:<label>:<host1,host2,host3>
```

Labels contain colons (`gitmirror:<seeder>:<user>:<project>`), so the parser splits on the **last** colon: everything before it is the label, everything after is the comma-separated host list. Hostnames don't contain colons or commas by convention, so this shape is unambiguous.

### Rate limiting

Claim lines are **rate-limited to changes**. The gitmirror reactor tracks the most recently emitted list per label; when a tick produces the same list, it skips emission. A safety-net re-broadcast fires every 60 seconds regardless of changes, so a receiver that joined the LAN mid-stream or missed an earlier multicast catches up before its LAN-ingested claim ages out (the claim TTL is 90 seconds — 30 seconds of headroom after each refresh).

A newly-paused source calls `PendingClaims::forget(label)` so future announcements stop carrying it, and receivers whose local LAN claim for that label expires drop their interest within the TTL window.

### Receiver ingest

Listeners receive the announcement frame and:

1. Parse each `GITMIRRORCLAIM:` line into `(label, hosts)`.
2. Read the sender's hostname from the paired `GITMIRROR:` line earlier in the same frame.
3. For each `(label, hosts)`, check whether the local hostname is in `hosts`:
   - **Yes** → call `ClaimsHandle::ingest_lan_claim(label, sender, true, now)`, which inserts or refreshes a `(label, sender)` entry with a 90-second expiry.
   - **No** → call `ingest_lan_claim(label, sender, false, now)`, which removes any existing entry for this specific `(label, sender)` pair.
4. The `ClaimsHandle` recomputes its union view (server-delivered + LAN-delivered claims) and makes it visible to the swarm manager (which downloads the blob) and the gitmirror receiver (which materializes the mirror).

### Multi-source robustness

Two source envoys could — in theory — claim the same label at the same time with different host lists. The receiver's `(label, source_hostname)` keying means each source's claim is tracked independently: dropping from one source's list doesn't affect the other's. A label stays in the union as long as **at least one** source still claims it.

Silently-dead sources age out naturally: 90 seconds without a refresh and the claim is pruned by the reactor tick.

### End-to-end

With Phase 6 shipped, a LAN-only fleet with one source envoy and three receivers can sync a repo end-to-end with the server offline:

1. Source's reactor scans `~/gitmirror/vigo-web/`, sees a valid sidecar, runs the publisher → swarm seed lands in `/var/lib/vigo/swarm/seed/`.
2. Source's reactor runs the selector → writes `gitmirror:danlap:dan:vigo-web → [plex, annlap, girlslap]` to `PendingClaims`.
3. Next swarm announcement includes both the `GITMIRROR:` peer line and the `GITMIRRORCLAIM:` claim line.
4. Each receiver parses the frame, sees its own hostname in the claim list, and updates its `ClaimsHandle`.
5. The swarm manager's union view now contains the label; it schedules the download against the swarm seed.
6. The gitmirror receiver tick sees the claim, finds the blob in the cache, and materializes the bare repo under `/var/lib/vigo/swarm/gitmirror/mirrors/danlap/dan/vigo-web.git`.

The server is entirely absent from steps 2-6. Phase 7 (below) adds cross-subnet reach; Phase 8 deleted the server-side `ClaimTable` and the `gitmirror_claims` proto field — shipped in 0.23.0.

## Cross-subnet mirrors via `cross_subnet_mirrors` (Phase 7)

LAN multicast (Phase 5/6) can't reach envoys on a different broadcast domain. Phase 7 adds an operator-explicit cross-subnet path: a source envoy can list specific hostnames in the sidecar's `cross_subnet_mirrors` field, and the source envoy will push the bundle to each of them directly over mTLS.

```yaml
# ~/gitmirror/vigo-web/.gitmirror.yaml
owner: dan
max_mirrors: 5
tags: [website]
description: "Vigo marketing site"

# Explicit cross-subnet targets on top of whatever LAN discovery picks.
cross_subnet_mirrors:
  - colo-box.dr.example.net
  - nas.offsite.example.net
```

### Wire format

Source envoys push to `POST /gitmirror/push/<url-encoded-label>` on the target's swarm peer port (1531). Required headers:

- `X-Vigo-Seeder` — canonical hostname of the source envoy
- `X-Vigo-Blob-Sha256` — 64-hex sha256 of the bundle body
- `Content-Type: application/octet-stream`

Body: the bundle bytes. HTTP over the same mTLS transport the swarm peer server uses.

### Receiver validation

The receiver's `/gitmirror/push/*` handler gates every request through a specific validation chain. Each rejection returns a distinct 4xx with a human-readable reason (surfaced in the source's push-error log):

1. **Gitmirror must be enabled** on the receiver. A blacklisted envoy (hostname matches `swarm.gitmirror.blacklist` in server.yaml) returns `403 gitmirror not enabled on this envoy`.
2. **Label must parse** into `gitmirror:<seeder>:<user>:<project>` segments. Malformed labels → `400 malformed gitmirror label`.
3. **Seeder match**: the `<seeder>` segment inside the label must equal the `X-Vigo-Seeder` header. A compromised envoy (with a valid fleet cert) cannot push a mirror labeled as a different seeder — the seeder claim is validated against both self-reported sources.
4. **Blob sha header format**: must be a 64-character lowercase hex sha256.
5. **Local user exists**: `<user>` from the label must be a real Unix account on this envoy (same gate as LAN mode).
6. **Body size** ≤ `MAX_BUNDLE_SIZE` (20 GiB).
7. **Body sha matches header**: computed sha256 of the received body must equal `X-Vigo-Blob-Sha256`. Catches transport corruption and misbehaving senders.

On accept, the receiver:

1. Writes the body atomically to the local swarm blob cache at the computed sha path.
2. Records a manifest entry via `Manifest::record_seed` so the gitmirror receiver reactor can resolve the label → blob. The `seeder_address` field is set to the literal `"direct-push"` to distinguish manual injections from gossip.
3. Calls `ClaimsHandle::ingest_lan_claim(label, sender, true, now)` so the reactor picks up the claim on its next tick — same path as a LAN-discovered mirror.

Returns `200 {"status":"accepted"}`.

### Source-side push loop

The source reactor, on every tick, iterates `src.sidecar.cross_subnet_mirrors` for each non-paused source:

1. Reads the bundle file from the swarm seed directory (published by the normal Phase 0 publisher pass earlier in the tick).
2. Computes the bundle sha256.
3. Checks per-`(label, target)` dedup state (`ExtraPushState`): is this the first push, or has the sha changed, or has the `RE_PUSH_INTERVAL_SECS = 60` window elapsed?
4. If yes, POSTs over mTLS to `https://<target>:1531/gitmirror/push/<label>` with the required headers.
5. On success, records the `(label, target) -> (sha, now)` in the dedup state. On failure, logs the specific error once (edge-triggered — repeated identical errors go silent).

Failure modes are non-fatal: DNS resolution misses, TCP connect refused, mTLS handshake failures, and 4xx/5xx responses all log and retry on the next reactor tick. One broken `cross_subnet_mirrors` target doesn't starve the others — the iteration continues.

### Re-push safety net

The `RE_PUSH_INTERVAL_SECS = 60` window sits below the receiver's `LAN_CLAIM_TTL_SECS = 90`, so even with an unchanged bundle the source re-pushes often enough that the receiver's LAN claim never ages out. If the source restarts and the dedup state clears, the next tick's push lands unconditionally — refreshing the receiver's claim and preserving the mirror.

### Trust model

Cross-subnet push inherits the same trust model as LAN mode:

- **Fleet CA** attests both ends of the mTLS connection. The receiver trusts the connection because the sender's client cert chains to the fleet CA; the sender trusts the server because the target's cert does the same.
- **Seeder-match invariant** (point 3 above) ensures a compromised envoy can only push mirrors labeled as its own seeder — it can't impersonate another envoy.
- **Local-user gate** means a compromised source can't push mirrors to accounts that don't exist on the receiver.
- Blast radius on a compromised fleet cert: the attacker can push arbitrary bundles (capped at 20 GiB) to any receiver that has gitmirror enabled and matching local users. Bundles land in root-owned seeder-namespaced dirs with no working tree, so the worst case is wasted disk, not user-data tampering. The existing `MAX_MIRRORS_PER_RECEIVER` and `MAX_PER_SOURCE_MIRRORS` guardrails bound the damage further.

### When to use `cross_subnet_mirrors` vs LAN discovery

**LAN (Phase 5/6)** is zero-config: just enable gitmirror on both source and target, put them on the same broadcast domain, and mirrors happen automatically. Use this for everything that's locally reachable.

**`cross_subnet_mirrors` (Phase 7)** is explicit and per-repo: name a specific target, and the source pushes to it every tick regardless of multicast reach. Use this for:

- Off-site DR (`colo-box.dr.example.net`)
- A cloud receiver for a home-lab source
- A receiver behind a NAT the LAN can't reach
- Any situation where multicast doesn't propagate

Both paths coexist: a single repo can have LAN mirrors (picked automatically) and `cross_subnet_mirrors` (explicit) at the same time. The union is what the operator gets.

### Example deployment

Two envoys in Dan's homelab plus one off-site colo. Gitmirror runs on all three by default — no matchfile configuration is required to opt in. To deny a specific envoy, add it to `swarm.gitmirror.blacklist` in `server.yaml`:

```yaml
# server.yaml — example: deny gitmirror on a noisy raspberrypi
swarm:
  gitmirror:
    blacklist:
      - "raspberrypi"
```

```yaml
# ~dan/gitmirror/vigo/.gitmirror.yaml on danlap.home.arpa
owner: dan
max_mirrors: 5
tags: [primary]
description: "Vigo main repo"
cross_subnet_mirrors:
  - colo-box.dr.example.net
```

Result: `plex.home.arpa` picks up the mirror via LAN multicast (Phase 6's deterministic selector), and `colo-box.dr.example.net` receives the bundle via direct mTLS push. Two receivers, one source, both materialized. Server: entirely absent.

## Staleness and manual pruning

A receiver flags a mirror as **stale** when its last successful refresh
(`last_refreshed` in `receiver_state.json`) is more than
`STALE_THRESHOLD_SECS` (30 days) old. Staleness is surfaced two ways:

1. A `warn!` log line on the receiver, rate-limited to once per 24 hours per label so a permanently-dead source doesn't flood the journal.
2. The `gitmirror.stale_mirrors` trait: a list of stale labels that flows to the server on check-in and is visible in the UI and `vigocli`.

**Stale mirrors are never automatically removed.** The whole point of gitmirror is that your backup is still reachable after your source envoy dies — and a dead source envoy is precisely what makes a mirror look stale. Auto-pruning would destroy the backup at the moment it's most valuable.

Manual cleanup is available via `vigo swarm gitmirror prune` (added in Phase 4 of the P2P-first rearchitecture). That command reads `receiver_state.json`, lists mirrors older than a threshold (default 30 days, configurable via `--older-than`), prints the candidates, and exits. Operators must re-invoke with `--yes` or name a specific label to actually delete.

## Observability (`/gitmirror` UI)

Every envoy's `gitmirror` trait (publisher sources + receiver mirrors) is joined across the fleet by `server/gitmirrormesh/` and surfaced at `/gitmirror` (list of labels) and `/gitmirror/{label}` (per-label mirror table, sidecar config, publisher metadata). The page is admin-only — fleet-wide per-user repo inventory exposes project names and filesystem paths that aren't appropriate for non-admin roles. Viewer and compliance-role users get a 403 on direct access.

The page opens with a health banner + problems list covering orphan mirrors (peer holds a mirror the publisher no longer selects), stale mirrors (not refreshed in >30 days), and pending mirrors (selected but not yet materialized). The JSON shape is available at `GET /api/v1/swarm/gitmirror/fleet`.

Gitmirror bundles are excluded from `/payload` entirely — that page shows only admin-distributed payloads. Every gitmirror publish is visible on `/gitmirror` and `/gitmirror/{label}`; pasting a gitmirror blob sha into `/payload/{sha}` redirects to `/gitmirror/{label}`.

## Publisher rotation and bundle lifecycle

Every gitmirror publish writes a new content seed under the same label (`gitmirror:<host>:<user>:<project>`). The seed's sha256 is derived from the bundle bytes, so every incremental produces a *new* sha. The old sha's last-seen timestamp never refreshes — nothing points at it anymore — and the server's retention pruner (`server/swarm/pruner.go`) eventually calls `manifestStore.Revoke()` on it after the seed's 7-day retention window elapses. Revoked entries gossip fleet-wide and every envoy evicts the blob from cache.

Mechanically this is correct: the bundle is gone. The bare repo on every receiver still holds every commit the old bundle delivered; nothing on the receiver side reacts to the revocation except the cache eviction. The operator never has to act on rotated-out bundles — they're internal gitmirror state, not a UI surface.

## Platform Support

| Platform | Source side | Receiver side |
|----------|-------------|---------------|
| Linux (git ≥ 2.35) | ✅ | ✅ (code landed; home-lab e2e run pending) |
| macOS (git ≥ 2.35) | expected to work | expected to work |
| FreeBSD / OpenBSD / NetBSD (git ≥ 2.35) | expected to work | expected to work |
| Windows | out of scope for MVP | out of scope for MVP |

The MVP target is Linux source + Linux receiver. Other Unix-family platforms are expected to work but aren't actively tested.

## Out of Scope for MVP

Each of these was considered and explicitly deferred — see the locked-in design decisions in the in-progress checkpoint for the rationale.

- `target_path`, `target_users`, `readonly`, `require_signed`, `allowed_seeders`, `retention_days`, `debounce_seconds` sidecar fields
- Branch selection — gitmirror always publishes `HEAD`
- Submodules
- `expose_git=true` (writable receiver repos, allowing two-way sync)
- Automatic source handoff
- Keepalive mechanism for paused seeds
- Web UI sidecar editor

## What Gitmirror Does NOT Do

- It is **not a backup tool.** It carries history but does not retain old versions, snapshots, or deleted commits beyond what git itself keeps.
- It is **not a sync tool.** Receivers are read-only; you cannot edit on a mirror and expect changes to flow back.
- It is **not a fleet distribution mechanism.** Use modules and `file:` resources for that.
- It does **not push from the source.** The trigger is a local commit detected by polling, not a git remote push.
- It does **not maintain a working tree on receivers.** Receivers hold a root-owned bare repo only; there is no `~<user>/gitmirror/<project>/` on a receiver. Users recover via `vigo swarm gitmirror recover <project>` — see [Receiver Layout](#receiver-layout).
- It does **not run on the server at all** (as of 0.23.0). Selection, delivery, and materialization are all peer-to-peer. The server plays no role in gitmirror.

## Troubleshooting

### My source isn't publishing

1. **Check git version.** `git --version` on the source must be 2.35 or newer. The agent log will show `gitmirror: git version check failed; reactor will not start until resolved` if it's lower.
2. **Check the sidecar.** Run the sidecar through the agent log filter — `journalctl -u vigo-envoy -e | grep -i gitmirror` should show a discovery error if the sidecar is malformed. The most common rejections are unknown fields (typo in `description` etc.) and missing fields.
3. **Check ownership.** The directory under `~<user>/gitmirror/<project>/` must be owned by `<user>`, not by root. Gitmirror drops privileges to the directory's owner before running `git`.
4. **Check the `.gitmirror.yaml` `owner` field.** It must match this envoy's hostname (case-insensitive). If you copied a sidecar from another machine, update the field.
5. **Check the trait.** The agent's `gitmirror` trait carries `source_errors` and `publish_errors` lists.

### The server has no claims for my source

1. **Confirm the source is being published.** Look at the source envoy's `gitmirror` trait — the source should appear in `sources[]` with a `git_sha` and `blob_sha` populated.
2. **Confirm the server received the trait.** Check the server log for a `gitmirror: trait report had errors` warning, which would indicate a parse problem on the server side.
3. **Confirm the candidate pool isn't empty.** Gitmirror needs at least one other envoy in the fleet whose `users.human.*` trait contains the target user. A source whose target user only exists on the source itself will get an empty mirror list.
4. **Confirm the seeder hostname matches.** The label's `<seeder>` segment must equal the reporting envoy's hostname in the fleet index. A mismatch is rejected as a security violation and logged on the server.

### `gitmirror.under_replicated` is set

The candidate pool is smaller than the sidecar's requested `max_mirrors`. Either lower `max_mirrors` in the sidecar, or add more envoys whose `users.human.*` trait includes the target user.

### A receiver refuses to materialize a claimed label

The receiver applies a conflict check against the bare repo path it doesn't already own. If `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git` exists with real git data (pack files, branch refs, loose objects) but isn't recorded in `receiver_state.json`, the receiver refuses to clobber it and emits a `gitmirror receiver: mirror failure` warning whose reason includes `pack_empty=X heads_empty=Y loose_empty=Z` so you can see which component triggered the refusal. Recovery: `sudo rm -rf /var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git` and the next 5-second reactor tick picks up the now-clean slate. (Empty `git init --bare` skeletons are allowed — the receiver recognizes its own post-failed-init leftovers and wipes them automatically.)

### A mirror keeps getting wiped and re-downloaded

A label is flickering in and out of a receiver's wanted set. Check the source envoy's LAN multicast cadence — if it's been offline for more than 90 seconds (the LAN claim TTL), receivers stop seeing the `GITMIRRORCLAIM:` refresh and age out the entry. The safety-net re-announce interval is 60 seconds, so a source that stays up but has intermittent network loss may drop in and out. Persistent flapping points at an unstable source envoy; check multicast reachability with `tcpdump -i any port 1531` on a known-good peer.

### The receiver has a stale bare repo

Bare repos at `/var/lib/vigo/swarm/gitmirror/mirrors/<seeder>/<user>/<project>.git` that aren't in `receiver_state.json` are first re-adopted by the startup rescan (if a valid `HEAD` resolves) and only then subject to `cleanup_orphan_bare_repos` if the rescan could not reconstruct them. If you want to force a full re-materialization, `sudo rm -rf` the bare repo *and* clear the matching `receiver_state.json` entry — the next reactor tick detects the missing state entry and pulls a fresh bundle from the swarm cache.

### Recovered a repo and then the source pushed a new commit — why isn't my clone updating?

Once you run `vigo swarm gitmirror recover <project>`, the cloned path is a normal git repo that the receiver pipeline does not manage. It's yours. Subsequent source commits flow into the bare mirror at `/var/lib/vigo/swarm/gitmirror/mirrors/...` (which continues to update), but they do not touch your recovered clone. If you want the latest, re-run `vigo swarm gitmirror recover <project>` into a different path, or `git pull` your clone against the bare mirror if you set it up as a remote.

## Related

- [Swarm](swarm.md) — the underlying P2P content distribution layer that gitmirror publishes through
- [Filesync](filesync.md) — sister subsystem for LAN-only file sync (no git, no server)
- [`gitmirror` trait reference](../../reference/traits/gitmirror.md) — full schema of the `gitmirror` trait emitted by the source-side reactor

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
