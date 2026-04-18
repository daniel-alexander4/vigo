
# Vigorepo — Personal-DR Git Mirroring

Vigorepo auto-mirrors a small number of a user's personal git repos from one **source** envoy to a bounded handful of **receiver** envoys where the same user has an account. The trigger is `git commit`, not `git push`. Receivers are read-only mirrors that hold a checked-out working tree plus a separate bare repo, so a user who loses their primary machine can `git clone` off any mirror to recover their work.

Vigorepo is a personal-disaster-recovery tool, not a fleet content distribution mechanism. Fleet-wide content distribution is what modules and `file:` resources are for. Vigorepo carries history.

## Status

As of **0.23.0**, vigorepo is a fully peer-to-peer subsystem. The server plays no role in selection, delivery, or acceptance — the `server/vigorepo/` package and the `CheckInResponse.vigorepo_claims` proto field were deleted in Phase 8 of the P2P-first rearchitecture. All mirror discovery, selection, and transport runs directly between envoys.

| Piece | Status |
|-------|--------|
| Source-side reactor + bundle publish | ✅ |
| LAN peer discovery (swarm multicast `VIGOREPO:` line) | ✅ Phase 5 |
| LAN deterministic selector + `VIGOREPOCLAIM:` announcements | ✅ Phase 6 |
| Cross-subnet `extra_mirrors:` direct push (`POST /vigorepo/push/<label>`) | ✅ Phase 7 |
| `vigorepo` trait collector (operator visibility) | ✅ |
| Receiver-side materialization (`agent/src/vigorepo/receiver.rs`) | ✅ |
| `vigo repo <verb>` recovery CLI | ✅ |
| `vigo repo prune` manual stale-cleanup | ✅ |
| Home-lab e2e verification | ⏳ (requires `git >= 2.35` on source host) |

## How It Works

1. **Opt in, per envoy.** An operator sets `vars: { vigorepo: true }` in a matchfile or `common.vgo`. The agent reads this from its cached `PolicyBundle.vars_json` at startup and only then starts the vigorepo subsystem. Dormant envoys never announce themselves and never accept pushed mirrors.
2. **Opt in, per repo.** A user creates a git repo at `~<user>/vigorepo/<project>/` and commits a `.vigorepo.yaml` sidecar. The sidecar names the source envoy's hostname, how many mirrors to maintain, optional LAN filter globs, and optional explicit `extra_mirrors:` cross-subnet targets.
3. **Discover.** Every 5 seconds, the agent's vigorepo reactor scans every local user's `~/vigorepo/*` directory, parses each sidecar with a strict deny-unknown-fields validator, and excludes anything whose `owner` field doesn't match this envoy's hostname.
4. **Bundle.** For each non-paused source whose `HEAD` has advanced since the last publish, the reactor runs `git bundle create` (as the owning Unix user, never as root) into `/var/lib/vigo/vigorepo/tmp/`. The first bundle is full; subsequent bundles are incremental from the last published commit.
5. **Publish as swarm seed.** The source writes a content seed `vigorepo:<host>:<user>:<project>` into the swarm seed directory. Vigorepo content seeds are flagged as **claim-only** in the manifest (label prefix `vigorepo:`), so the standard target-spec self-assignment path explicitly skips them.
6. **LAN discovery.** Every vigorepo-enabled envoy announces itself on the existing swarm multicast channel with a `VIGOREPO:<host>:<users>:<tags>` line (Phase 5). Every source envoy builds an in-memory peer table from those announcements.
7. **LAN selection.** For each non-paused source, the reactor filters the peer table by the sidecar's `include_envoys`/`exclude_envoys` + owner-user-exists check, runs deterministic `sha256(label||host)` selection, picks the top `max_mirrors`, and writes the result to a shared `PendingClaims` cache (Phase 6).
8. **LAN claim announcement.** The swarm announcer emits one `VIGOREPOCLAIM:<label>:<host1,host2,...>` line per source per multicast cycle (rate-limited to changes + 60s safety re-announce). LAN receivers parse the line, check whether their own hostname is in the list, and call `ClaimsHandle::ingest_lan_claim` to mark the label as wanted.
9. **Cross-subnet push.** For each source with a non-empty `extra_mirrors:` list, the reactor opens a mTLS connection to `https://<target>:1531/vigorepo/push/<label>` and POSTs the bundle body with `X-Vigo-Seeder` and `X-Vigo-Blob-Sha256` headers (Phase 7). The target's swarm peer server validates the request, writes the blob to its local cache, injects a manifest entry, and calls `ingest_lan_claim` — same final path as a LAN-discovered mirror.
10. **Materialize.** The receiver-side reactor diffs `ClaimsHandle::snapshot()` against currently-materialized state, finds each newly-claimed content seed in the local swarm cache, unbundles into a root-owned bare repo at `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git`. On the next tick the cycle can re-run: a new bundle triggers an incremental unbundle; a claim whose LAN announcement stops arriving ages out after the 90-second TTL; stale mirrors (`now - last_refreshed > 30 days`) surface as warnings and in the `vigorepo.stale_mirrors` trait but are **never auto-evicted**. Manual cleanup is available via `vigo repo prune`.

The server is absent from every step. It never runs `git`, never downloads bundle bytes, never picks mirrors, never delivers claim lists. The `vigorepo.sources[]` trait is still emitted for operator observability but no server component consumes it — it's a read-only window onto source-side state.

## The `.vigorepo.yaml` Sidecar

Every synced repo must have a `.vigorepo.yaml` file at its root. This is mandatory opt-in: a `.git/` directory under `~<user>/vigorepo/` without a sidecar is left alone. Operators can have personal git work there that vigorepo never touches.

```yaml
owner: my-laptop
max_mirrors: 5
tags: [personal, dotfiles]
paused: false
description: "shell config + editor setup"

# Optional candidate filters — omit entirely for "mirror to any
# eligible peer on the LAN". Evaluated on the source envoy during
# LAN selection (Phase 6); `extra_mirrors:` targets bypass filters
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

The LAN selector on the source envoy walks its in-memory peer table (populated from swarm multicast `VIGOREPO:` announcements) and applies the sidecar's filters in this exact order:

1. **Owner user must exist on the peer** — the peer's announced `users` list must contain the repo's `owner`. A repo owned by `dan` can only mirror on envoys where `dan` is a local account.
2. **`exclude_envoys`** — blacklist. Envoys matching any glob are dropped.
3. **`include_envoys`** — whitelist. If the list is non-empty, an envoy must match at least one glob to stay in the pool. Empty or absent is "pass all".

Hostname glob syntax is a simple wildcard matcher supporting `*` (any chars) and `?` (one char), case-insensitive. A malformed glob matches nothing (never accidentally "everything").

**`when:` expressions over full traits are NOT supported in LAN mode.** The multicast announcement frame only carries hostname/users/tags, so richer filtering (e.g. `trait_eq('os.family', 'linux')`) can't be evaluated by the source envoy. Sources that need trait-level filtering should use explicit `extra_mirrors:` entries for those targets instead.

## Mirror Counts

`MIN_MIRRORS = 3` and `MAX_MIRRORS = 10` are immutable constants. Operators can request anything in `[3, 10]` via the sidecar's `max_mirrors` field; values outside that range are clamped with a warning.

`MIN_MIRRORS` is a **target, not a gate**. If the LAN peer pool has fewer than 3 eligible envoys, vigorepo picks whatever is available. Hard cap enforcement runs in both directions: the LAN selector uses `min(max_mirrors, len(candidates))` on the source side, and `MAX_MIRRORS_PER_RECEIVER = 50` + `MAX_PER_SOURCE_MIRRORS = 20` are enforced by receivers on first materialization.

## Receiver Layout

Receivers hold each mirror as a single **root-owned bare repo** at `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git`. There is no working tree at `~<user>/vigorepo/` — the receiver never touches user-space. The bare repo is mode `0755` root-owned, so any user on the system can `git clone` it read-only, but only the agent (running as root) can update it.

Recovery is a deliberate user action, not a passive filesystem shadow. When a user needs to get their repo back, they run:

```
vigo repo recover <project>
```

which clones the bare repo into `~<user>/vigorepo/<project>/` as a normal writable git repo with full history. See [`vigo repo` CLI reference](../reference/cli/vigorepo.md) for the full recovery subcommand tree.

**Why root-owned bare repo, not a user-owned mirror.** The bare repo is operator data managed by the convergence system; the user should not be able to damage it by accident. User-owned files would also mean the agent has to drop privileges for every update, which was the cause of the 0.21.24 → 0.21.25 regression (root-owned `GIT_DIR` + non-root `git checkout` fails at `index.lock`). Root-owned + on-demand clone via `vigo repo recover` eliminates the ownership split from the hot path entirely.

**What recovery gives you.** A real git repo at a writable path, with all tags, the full commit log, and a `.git/` directory. It's indistinguishable from having cloned the original source. From that point forward the recovered repo is yours — subsequent vigorepo updates on the source side do not automatically propagate to your recovered clone, because it's no longer part of the receiver pipeline.

## Source Identity and Security

Vigorepo does not have its own authentication layer. Security is provided by three existing layers stacked together:

1. **Swarm transport security** — every byte moves over mTLS on the existing swarm peer port (1531). An envoy can only be a seeder if it's enrolled and approved. Content integrity is guaranteed by SHA256 verification of every assembled blob (`agent/src/swarm/cache.rs`).
2. **Swarm seed authorization** — the `swarm.seeding_envoys` allowlist in `server.yaml` gates which envoys can introduce new seeds (hostname globs; the server's own envoy is implicit). Vigorepo publishes through the same path, so the same allowlist applies.
3. **Server-mediated seeder→label verification** — every label embeds the seeder hostname as its second segment (`vigorepo:<seeder>:<user>:<project>`). The *server* validates the match against authoritative fleet data in `server/vigorepo/builder.go::OnTraitReport` before it ever issues a claim: if an envoy reports a `vigorepo.sources[]` entry whose label claims a different seeder than the envoy's own hostname in the fleet index, the entry is rejected. Receivers then trust the claim list because it arrives over the existing mTLS-authenticated check-in channel and has already been server-validated. They never act on raw manifest entries.

Note that the check is server-mediated rather than receiver-side because peer-to-peer blob downloads deliberately disable TLS hostname verification (`agent/src/swarm/peer_client.rs::build_peer_agent` calls `disable_verification(true)`) — peers connect by IP and the cert SAN is hostname-based, so the wire identity is unavailable to the downloader. Content integrity still comes from SHA256; identity comes from the fleet index.

Note that the sidecar's `owner` field is **not** a security boundary. It's a single-writer hint that prevents two envoys from accidentally both believing they're the source for the same repo. The actual security boundary is the server's seeder→label verification above.

## State Storage

| Path | Owner | Contents |
|------|-------|----------|
| `/var/lib/vigo/vigorepo/state.json` | `root:root` 0600 | publisher: per-label `{git_sha, blob_sha}` so restarts don't re-publish identical bundles. Root-only because no user-facing command reads it. |
| `/var/lib/vigo/vigorepo/traits.json` | `root:root` 0644 | latest reactor pass snapshot for the `vigorepo` trait collector. World-readable so `vigo repo source list` (an unprivileged-user command) can read it without sudo — contents are non-sensitive (labels, owner usernames, sidecar config, git/blob shas) and already flow through server trait reports and the swarm manifest. |
| `/var/lib/vigo/vigorepo/tmp/` | mode `1777` (sticky like `/tmp`) | scratch directory for `git bundle create` output before atomic move into the swarm seed dir |
| `/var/lib/vigo/vigorepo/receiver_state.json` | `root:root` 0644 | receiver: per-label `{blob_sha, git_sha, next_gc_at, ...}` + `pending_wipes` for the hysteresis window. World-readable so `vigo repo {list,show,log,branches,peers}` can read it without sudo — the bare repo layout under `mirrors/` is already world-traversable, so the file merely serializes what the filesystem reveals. |
| `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git` | `root:root` mode `0755` | bare receiver repo, readable by any local user (so `vigo repo recover` can clone it without privilege), writable only by the agent. The `mirrors/` subdirectory keeps bare repos namespaced away from publisher state files so the receiver's orphan-cleanup walker has a clean boundary |
| `~<user>/vigorepo/<project>/` | `<user>:<user>` | **source side**: operator-created working repo with `.git/`, containing `.vigorepo.yaml`. **receiver side**: does not exist — the receiver never touches user-space. Users recover into this path on demand via `vigo repo recover <project>`, which creates a real writable git clone |

State files survive agent restarts. The tmp directory is GCed on agent startup for any `*.bundle` older than 1 hour (interrupted bundle operations).

If `receiver_state.json` is missing, corrupt, or has a stale schema version, the receiver rescans `mirrors/<seeder>/<user>/*.git` at startup and re-adopts every bare repo whose `git rev-parse HEAD` resolves cleanly. Rescanned entries are recorded with an empty `blob_sha`, which forces a single incremental fetch against the current manifest entry on the next reactor tick — git reuses the existing packs, so bandwidth is minimal. This recovery path is what keeps a corrupt state file from silently deleting every mirror via the orphan cleanup walker.

## Activation

**Vigorepo is opt-in.** The subsystem is dormant by default and only starts on envoys whose matchfile explicitly enables it via a var:

```yaml
# stockpile/envoys/workstations/common.vgo
vars:
  vigorepo: true
```

Every envoy matched by a file under `stockpile/envoys/workstations/` inherits `vigorepo: true` through the existing `common.vgo` directory-inheritance cascade, so one edit enables vigorepo for a whole class of envoys at once. Match blocks can override with their own `vars: { vigorepo: false }` where needed (child-wins semantics are built into the existing var cascade — no new precedence rule).

On a fresh deploy, envoys *without* the var stay dormant — no participation, no publisher scan, no receiver, no multicast announcements, nothing. The subsystem is effectively unstarted.

The flag arrives at the agent through the normal check-in path: the server renders the envoy's merged vars map into `PolicyBundle.vars_json` and the agent caches it in the LMDB state store for offline convergence. At subsystem startup, `vigorepo::spawn` reads the cached var and refuses to start the reactor unless `vigorepo: true` is present. Live flips of the var (toggling in the matchfile and republishing) take effect on the next agent restart; hot reload is not supported.

### Preconditions (must also hold for the reactor to start)

1. The matchfile var `vigorepo: true` is set for this envoy.
2. `git` is installed at version ≥ 2.35 (`git_ops::check_version()` rejects older hosts at startup).
3. At least one user has a `~<user>/vigorepo/<project>/` directory containing a valid `.vigorepo.yaml` — this enables publishing; receive-only envoys need only the var.

### Fresh-enrollment behavior

A just-enrolled envoy has no cached policy bundle yet, so the subsystem stays dormant through its first agent process even if the matchfile has the var set. After the first successful check-in writes the cache, the next agent restart sees the var and starts the reactor. In practice this means one extra `systemctl restart vigo-envoy` after enrollment if you want vigorepo active immediately — or just wait for the next routine restart. The design tradeoff favors "never auto-participate by surprise" over "always participate as soon as possible".

### Values accepted for the var

- JSON/YAML boolean `true` — the canonical shape
- String `"true"`, `"yes"`, or `"1"` (case-insensitive) — tolerant to operator typing
- Integer `1` or any non-zero number — also tolerated

Anything else (including absence, `false`, `null`, `"no"`, `0`, or any other value) means disabled.

## Limits

All caps are immutable constants in `agent/src/vigorepo/mod.rs`. None can be overridden by configuration, environment variable, CLI flag, or sidecar value. The philosophy: the numbers are large enough that tuning is unnecessary for the intended use case, and configurability is where complexity breeds.

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

## LAN peer discovery (Phase 5)

Vigorepo-enabled envoys announce themselves to each other on the existing swarm multicast channel (`224.0.0.42:1531`). Every regular swarm announcement frame gains one optional line:

```text
VIGOREPO:<hostname>:<users-csv>:<tags-csv>
```

Listeners ingest this line into an in-memory `PeerTable` keyed by canonicalized hostname. Entries carry the hostname, the set of human Unix users present on the peer (read from `/etc/passwd` at the peer's agent startup, filtered to UID ∈ [1000, 65534)), and a tag list sourced from the agent's traits. Entries expire after **5 minutes** without a refresh; the vigorepo reactor's tick prunes stale entries on every pass, so the table stays bounded without a separate sweeper.

**Opt-in gate:** an envoy only announces the `VIGOREPO:` line if its matchfile var `vigorepo: true` is set. A dormant subsystem never broadcasts its local info, so it never lands in any peer's table — "presence in the table" IS the participation signal. There's no separate capability bit to check.

**Graceful overflow:** if the swarm announcement is close to the 1400-byte UDP MTU ceiling, the `VIGOREPO:` line is dropped from that announcement (not truncated). The next announcement cycle typically lands 5 seconds later and other peers refresh their TTL then. Worst case: a peer table entry ages up to 5 minutes behind reality, which is well within the TTL window.

**Filtering** happens on the source side. When a source envoy needs to pick mirrors for a repo, it snapshots the peer table and applies the sidecar's `include_envoys` / `exclude_envoys` hostname globs plus the repo's `owner` field — the peer must have the owning user in its local `users` list. `when:` expressions over full traits are **not supported in LAN mode** (the multicast frame only carries hostname/users/tags). Sources that need trait-level filtering should use the explicit `extra_mirrors:` list in the sidecar (Phase 7) for those targets instead.

**No cross-subnet reach yet.** Phase 5 is LAN-only: multicast stays on the broadcast domain. Cross-subnet `extra_mirrors` direct-push lands in Phase 7.

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

Tags and `when:` expressions over full traits are intentionally not supported in LAN mode — richer filtering is deferred to the explicit `extra_mirrors:` list (Phase 7).

### Announcement shape

Source envoys publish their selection as one multicast line per label, appended to the existing swarm announcement frame:

```text
VIGOREPOCLAIM:<label>:<host1,host2,host3>
```

Labels contain colons (`vigorepo:<seeder>:<user>:<project>`), so the parser splits on the **last** colon: everything before it is the label, everything after is the comma-separated host list. Hostnames don't contain colons or commas by convention, so this shape is unambiguous.

### Rate limiting

Claim lines are **rate-limited to changes**. The vigorepo reactor tracks the most recently emitted list per label; when a tick produces the same list, it skips emission. A safety-net re-broadcast fires every 60 seconds regardless of changes, so a receiver that joined the LAN mid-stream or missed an earlier multicast catches up before its LAN-ingested claim ages out (the claim TTL is 90 seconds — 30 seconds of headroom after each refresh).

A newly-paused source calls `PendingClaims::forget(label)` so future announcements stop carrying it, and receivers whose local LAN claim for that label expires drop their interest within the TTL window.

### Receiver ingest

Listeners receive the announcement frame and:

1. Parse each `VIGOREPOCLAIM:` line into `(label, hosts)`.
2. Read the sender's hostname from the paired `VIGOREPO:` line earlier in the same frame.
3. For each `(label, hosts)`, check whether the local hostname is in `hosts`:
   - **Yes** → call `ClaimsHandle::ingest_lan_claim(label, sender, true, now)`, which inserts or refreshes a `(label, sender)` entry with a 90-second expiry.
   - **No** → call `ingest_lan_claim(label, sender, false, now)`, which removes any existing entry for this specific `(label, sender)` pair.
4. The `ClaimsHandle` recomputes its union view (server-delivered + LAN-delivered claims) and makes it visible to the swarm manager (which downloads the blob) and the vigorepo receiver (which materializes the mirror).

### Multi-source robustness

Two source envoys could — in theory — claim the same label at the same time with different host lists. The receiver's `(label, source_hostname)` keying means each source's claim is tracked independently: dropping from one source's list doesn't affect the other's. A label stays in the union as long as **at least one** source still claims it.

Silently-dead sources age out naturally: 90 seconds without a refresh and the claim is pruned by the reactor tick.

### End-to-end

With Phase 6 shipped, a LAN-only fleet with one source envoy and three receivers can sync a repo end-to-end with the server offline:

1. Source's reactor scans `~/vigorepo/vigo-web/`, sees a valid sidecar, runs the publisher → swarm seed lands in `/var/lib/vigo/swarm/seed/`.
2. Source's reactor runs the selector → writes `vigorepo:danlap:dan:vigo-web → [plex, annlap, girlslap]` to `PendingClaims`.
3. Next swarm announcement includes both the `VIGOREPO:` peer line and the `VIGOREPOCLAIM:` claim line.
4. Each receiver parses the frame, sees its own hostname in the claim list, and updates its `ClaimsHandle`.
5. The swarm manager's union view now contains the label; it schedules the download against the swarm seed.
6. The vigorepo receiver tick sees the claim, finds the blob in the cache, and materializes the bare repo under `/var/lib/vigo/vigorepo/mirrors/danlap/dan/vigo-web.git`.

The server is entirely absent from steps 2-6. Phase 7 (below) adds cross-subnet reach; Phase 8 deleted the server-side `ClaimTable` and the `vigorepo_claims` proto field — shipped in 0.23.0.

## Cross-subnet mirrors via `extra_mirrors` (Phase 7)

LAN multicast (Phase 5/6) can't reach envoys on a different broadcast domain. Phase 7 adds an operator-explicit cross-subnet path: a source envoy can list specific hostnames in the sidecar's `extra_mirrors` field, and the source envoy will push the bundle to each of them directly over mTLS.

```yaml
# ~/vigorepo/vigo-web/.vigorepo.yaml
owner: dan
max_mirrors: 5
tags: [website]
description: "Vigo marketing site"

# Explicit cross-subnet targets on top of whatever LAN discovery picks.
extra_mirrors:
  - colo-box.dr.example.net
  - nas.offsite.example.net
```

### Wire format

Source envoys push to `POST /vigorepo/push/<url-encoded-label>` on the target's swarm peer port (1531). Required headers:

- `X-Vigo-Seeder` — canonical hostname of the source envoy
- `X-Vigo-Blob-Sha256` — 64-hex sha256 of the bundle body
- `Content-Type: application/octet-stream`

Body: the bundle bytes. HTTP over the same mTLS transport the swarm peer server uses.

### Receiver validation

The receiver's `/vigorepo/push/*` handler gates every request through a specific validation chain. Each rejection returns a distinct 4xx with a human-readable reason (surfaced in the source's push-error log):

1. **Vigorepo must be enabled** on the receiver. A dormant envoy (no `vars: { vigorepo: true }` in the matchfile) returns `403 vigorepo not enabled on this envoy`.
2. **Label must parse** into `vigorepo:<seeder>:<user>:<project>` segments. Malformed labels → `400 malformed vigorepo label`.
3. **Seeder match**: the `<seeder>` segment inside the label must equal the `X-Vigo-Seeder` header. A compromised envoy (with a valid fleet cert) cannot push a mirror labeled as a different seeder — the seeder claim is validated against both self-reported sources.
4. **Blob sha header format**: must be a 64-character lowercase hex sha256.
5. **Local user exists**: `<user>` from the label must be a real Unix account on this envoy (same gate as LAN mode).
6. **Body size** ≤ `MAX_BUNDLE_SIZE` (20 GiB).
7. **Body sha matches header**: computed sha256 of the received body must equal `X-Vigo-Blob-Sha256`. Catches transport corruption and misbehaving senders.

On accept, the receiver:

1. Writes the body atomically to the local swarm blob cache at the computed sha path.
2. Records a manifest entry via `Manifest::record_seed` so the vigorepo receiver reactor can resolve the label → blob. The `seeder_address` field is set to the literal `"direct-push"` to distinguish manual injections from gossip.
3. Calls `ClaimsHandle::ingest_lan_claim(label, sender, true, now)` so the reactor picks up the claim on its next tick — same path as a LAN-discovered mirror.

Returns `200 {"status":"accepted"}`.

### Source-side push loop

The source reactor, on every tick, iterates `src.sidecar.extra_mirrors` for each non-paused source:

1. Reads the bundle file from the swarm seed directory (published by the normal Phase 0 publisher pass earlier in the tick).
2. Computes the bundle sha256.
3. Checks per-`(label, target)` dedup state (`ExtraPushState`): is this the first push, or has the sha changed, or has the `RE_PUSH_INTERVAL_SECS = 60` window elapsed?
4. If yes, POSTs over mTLS to `https://<target>:1531/vigorepo/push/<label>` with the required headers.
5. On success, records the `(label, target) -> (sha, now)` in the dedup state. On failure, logs the specific error once (edge-triggered — repeated identical errors go silent).

Failure modes are non-fatal: DNS resolution misses, TCP connect refused, mTLS handshake failures, and 4xx/5xx responses all log and retry on the next reactor tick. One broken `extra_mirrors` target doesn't starve the others — the iteration continues.

### Re-push safety net

The `RE_PUSH_INTERVAL_SECS = 60` window sits below the receiver's `LAN_CLAIM_TTL_SECS = 90`, so even with an unchanged bundle the source re-pushes often enough that the receiver's LAN claim never ages out. If the source restarts and the dedup state clears, the next tick's push lands unconditionally — refreshing the receiver's claim and preserving the mirror.

### Trust model

Cross-subnet push inherits the same trust model as LAN mode:

- **Fleet CA** attests both ends of the mTLS connection. The receiver trusts the connection because the sender's client cert chains to the fleet CA; the sender trusts the server because the target's cert does the same.
- **Seeder-match invariant** (point 3 above) ensures a compromised envoy can only push mirrors labeled as its own seeder — it can't impersonate another envoy.
- **Local-user gate** means a compromised source can't push mirrors to accounts that don't exist on the receiver.
- Blast radius on a compromised fleet cert: the attacker can push arbitrary bundles (capped at 20 GiB) to any receiver that has vigorepo enabled and matching local users. Bundles land in root-owned seeder-namespaced dirs with no working tree, so the worst case is wasted disk, not user-data tampering. The existing `MAX_MIRRORS_PER_RECEIVER` and `MAX_PER_SOURCE_MIRRORS` guardrails bound the damage further.

### When to use `extra_mirrors` vs LAN discovery

**LAN (Phase 5/6)** is zero-config: just enable vigorepo on both source and target, put them on the same broadcast domain, and mirrors happen automatically. Use this for everything that's locally reachable.

**`extra_mirrors` (Phase 7)** is explicit and per-repo: name a specific target, and the source pushes to it every tick regardless of multicast reach. Use this for:

- Off-site DR (`colo-box.dr.example.net`)
- A cloud receiver for a home-lab source
- A receiver behind a NAT the LAN can't reach
- Any situation where multicast doesn't propagate

Both paths coexist: a single repo can have LAN mirrors (picked automatically) and `extra_mirrors` (explicit) at the same time. The union is what the operator gets.

### Example deployment

Two envoys in Dan's homelab plus one off-site colo:

```yaml
# stockpile/envoys/workstations/danlap.vgo
- match: "danlap.home.arpa"
  vars: { vigorepo: true }

# stockpile/envoys/homelab/plex.vgo
- match: "plex.home.arpa"
  vars: { vigorepo: true }

# stockpile/envoys/offsite/colo-box.vgo
- match: "colo-box.dr.example.net"
  vars: { vigorepo: true }
```

```yaml
# ~dan/vigorepo/vigo/.vigorepo.yaml on danlap.home.arpa
owner: dan
max_mirrors: 5
tags: [primary]
description: "Vigo main repo"
extra_mirrors:
  - colo-box.dr.example.net
```

Result: `plex.home.arpa` picks up the mirror via LAN multicast (Phase 6's deterministic selector), and `colo-box.dr.example.net` receives the bundle via direct mTLS push. Two receivers, one source, both materialized. Server: entirely absent.

## Staleness and manual pruning

A receiver flags a mirror as **stale** when its last successful refresh
(`last_refreshed` in `receiver_state.json`) is more than
`STALE_THRESHOLD_SECS` (30 days) old. Staleness is surfaced two ways:

1. A `warn!` log line on the receiver, rate-limited to once per 24 hours per label so a permanently-dead source doesn't flood the journal.
2. The `vigorepo.stale_mirrors` trait: a list of stale labels that flows to the server on check-in and is visible in the UI and `vigocli`.

**Stale mirrors are never automatically removed.** The whole point of vigorepo is that your backup is still reachable after your source envoy dies — and a dead source envoy is precisely what makes a mirror look stale. Auto-pruning would destroy the backup at the moment it's most valuable.

Manual cleanup is available via `vigo repo prune` (added in Phase 4 of the P2P-first rearchitecture). That command reads `receiver_state.json`, lists mirrors older than a threshold (default 30 days, configurable via `--older-than`), prints the candidates, and exits. Operators must re-invoke with `--yes` or name a specific label to actually delete.

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
- Branch selection — vigorepo always publishes `HEAD`
- Submodules
- `expose_git=true` (writable receiver repos, allowing two-way sync)
- Automatic source handoff
- Keepalive mechanism for paused seeds
- Web UI sidecar editor

## What Vigorepo Does NOT Do

- It is **not a backup tool.** It carries history but does not retain old versions, snapshots, or deleted commits beyond what git itself keeps.
- It is **not a sync tool.** Receivers are read-only; you cannot edit on a mirror and expect changes to flow back.
- It is **not a fleet distribution mechanism.** Use modules and `file:` resources for that.
- It does **not push from the source.** The trigger is a local commit detected by polling, not a git remote push.
- It does **not maintain a working tree on receivers.** Receivers hold a root-owned bare repo only; there is no `~<user>/vigorepo/<project>/` on a receiver. Users recover via `vigo repo recover <project>` — see [Receiver Layout](#receiver-layout).
- It does **not run on the server at all** (as of 0.23.0). Selection, delivery, and materialization are all peer-to-peer. The server plays no role in vigorepo.

## Troubleshooting

### My source isn't publishing

1. **Check git version.** `git --version` on the source must be 2.35 or newer. The agent log will show `vigorepo: git version check failed; reactor will not start until resolved` if it's lower.
2. **Check the sidecar.** Run the sidecar through the agent log filter — `journalctl -u vigo-envoy -e | grep -i vigorepo` should show a discovery error if the sidecar is malformed. The most common rejections are unknown fields (typo in `description` etc.) and missing fields.
3. **Check ownership.** The directory under `~<user>/vigorepo/<project>/` must be owned by `<user>`, not by root. Vigorepo drops privileges to the directory's owner before running `git`.
4. **Check the `.vigorepo.yaml` `owner` field.** It must match this envoy's hostname (case-insensitive). If you copied a sidecar from another machine, update the field.
5. **Check the trait.** The agent's `vigorepo` trait carries `source_errors` and `publish_errors` lists.

### The server has no claims for my source

1. **Confirm the source is being published.** Look at the source envoy's `vigorepo` trait — the source should appear in `sources[]` with a `git_sha` and `blob_sha` populated.
2. **Confirm the server received the trait.** Check the server log for a `vigorepo: trait report had errors` warning, which would indicate a parse problem on the server side.
3. **Confirm the candidate pool isn't empty.** Vigorepo needs at least one other envoy in the fleet whose `users.human.*` trait contains the target user. A source whose target user only exists on the source itself will get an empty mirror list.
4. **Confirm the seeder hostname matches.** The label's `<seeder>` segment must equal the reporting envoy's hostname in the fleet index. A mismatch is rejected as a security violation and logged on the server.

### `vigorepo.under_replicated` is set

The candidate pool is smaller than the sidecar's requested `max_mirrors`. Either lower `max_mirrors` in the sidecar, or add more envoys whose `users.human.*` trait includes the target user.

### A receiver refuses to materialize a claimed label

The receiver applies a conflict check against the bare repo path it doesn't already own. If `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git` exists with real git data (pack files, branch refs, loose objects) but isn't recorded in `receiver_state.json`, the receiver refuses to clobber it and emits a `vigorepo receiver: mirror failure` warning whose reason includes `pack_empty=X heads_empty=Y loose_empty=Z` so you can see which component triggered the refusal. Recovery: `sudo rm -rf /var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git` and the next 5-second reactor tick picks up the now-clean slate. (Empty `git init --bare` skeletons are allowed — the receiver recognizes its own post-failed-init leftovers and wipes them automatically.)

### A mirror keeps getting wiped and re-downloaded

A label is flickering in and out of a receiver's wanted set. Check the source envoy's LAN multicast cadence — if it's been offline for more than 90 seconds (the LAN claim TTL), receivers stop seeing the `VIGOREPOCLAIM:` refresh and age out the entry. The safety-net re-announce interval is 60 seconds, so a source that stays up but has intermittent network loss may drop in and out. Persistent flapping points at an unstable source envoy; check multicast reachability with `tcpdump -i any port 1531` on a known-good peer.

### The receiver has a stale bare repo

Bare repos at `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git` that aren't in `receiver_state.json` are first re-adopted by the startup rescan (if a valid `HEAD` resolves) and only then subject to `cleanup_orphan_bare_repos` if the rescan could not reconstruct them. If you want to force a full re-materialization, `sudo rm -rf` the bare repo *and* clear the matching `receiver_state.json` entry — the next reactor tick detects the missing state entry and pulls a fresh bundle from the swarm cache.

### Recovered a repo and then the source pushed a new commit — why isn't my clone updating?

Once you run `vigo repo recover <project>`, the cloned path is a normal git repo that the receiver pipeline does not manage. It's yours. Subsequent source commits flow into the bare mirror at `/var/lib/vigo/vigorepo/mirrors/...` (which continues to update), but they do not touch your recovered clone. If you want the latest, re-run `vigo repo recover <project>` into a different path, or `git pull` your clone against the bare mirror if you set it up as a remote.

## Related

- [Swarm](swarm.md) — the underlying P2P content distribution layer that vigorepo publishes through
- [Vigosync](vigosync.md) — sister subsystem for LAN-only file sync (no git, no server)
- [`vigorepo` trait reference](../reference/traits/vigorepo.md) — full schema of the `vigorepo` trait emitted by the source-side reactor

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
