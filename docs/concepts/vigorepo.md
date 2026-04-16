
# Vigorepo — Personal-DR Git Mirroring

Vigorepo auto-mirrors a small number of a user's personal git repos from one **source** envoy to a bounded handful of **receiver** envoys where the same user has an account. The trigger is `git commit`, not `git push`. Receivers are read-only mirrors that hold a checked-out working tree plus a separate bare repo, so a user who loses their primary machine can `git clone` off any mirror to recover their work.

Vigorepo is a personal-disaster-recovery tool, not a fleet content distribution mechanism. Fleet-wide content distribution is what modules and `file:` resources are for. Vigorepo carries history.

## MVP Status

The full source → server → receiver pipeline is wired end-to-end in code and unit-tested. Real home-lab verification is still pending and requires `git >= 2.35` on the source host (`git_ops::check_version` refuses to start the reactor on older hosts).

| Piece | Status |
|-------|--------|
| Source-side reactor + bundle publish | ✅ |
| Server-side `ClaimTable` + deterministic mirror selection | ✅ |
| `vigorepo` trait collector (operator visibility) | ✅ |
| `CheckInResponse.vigorepo_claims` proto field | ✅ |
| Receiver-side materialization (`agent/src/vigorepo/receiver.rs`) | ✅ (code landed, unit-tested; home-lab e2e run pending) |
| Per-label `mirror_failures` trait emission | ⏳ |
| `vigocli vigorepo` subcommands | ⏳ |
| Web UI page at `/vigorepo` + REST endpoints | ⏳ |

## How It Works

1. **Opt in.** A user creates a git repo at `~<user>/vigorepo/<project>/` and commits a `.vigorepo.yaml` sidecar at the repo root. The sidecar names the source envoy and how many mirrors to maintain.
2. **Discover.** Every 5 seconds, the agent's vigorepo reactor scans every local user's `~/vigorepo/*` directory, parses each sidecar with a strict deny-unknown-fields validator, and excludes anything whose `owner` field doesn't match this envoy's hostname (so other envoys' `~/vigorepo/` directories are left alone).
3. **Bundle.** For each non-paused source whose `HEAD` has advanced since the last publish, the reactor runs `git bundle create` (as the owning Unix user, never as root) into `/var/lib/vigo/vigorepo/tmp/`. The first bundle is full; subsequent bundles are incremental from the last published commit.
4. **Publish.** The source writes a content seed `vigorepo:<host>:<user>:<project>` (containing the git bundle) into the swarm seed directory. There is no separate meta seed: the validated sidecar fields are routed to the server through the `vigorepo` trait, so a swarm-side meta blob would be dead weight.
5. **Gossip.** The existing swarm trait collector hashes the new seed file and the check-in loop's `register_seeds_in_manifest` call adds it to the swarm CRDT. From there it propagates fleet-wide via the normal swarm gossip path. Vigorepo content seeds are flagged as **claim-only** in the manifest (label prefix `vigorepo:`), so the standard target-spec self-assignment path explicitly skips them — the only way to download a vigorepo bundle is via a server-issued claim.
6. **Trait report.** The reactor writes a snapshot file at `/var/lib/vigo/vigorepo/traits.json` and the `vigorepo` trait collector reads it on each check-in. The trait carries per-source labels, current `git_sha` / `blob_sha`, validated sidecar fields (`max_mirrors`, `tags`, `description`), and any source/publish errors.
7. **Server selects.** The server-side `Builder` consumes each `vigorepo.sources[]` entry from the trait report, builds a candidate pool from the fleet (envoys whose `users.human.*` traits include the target user, excluding the seeder itself, revoked envoys, and offline envoys), and runs deterministic selection: `sha256(label || hostname)` ascending, take the first `min(max_mirrors, len(candidates))`. Results live in an in-memory `ClaimTable`.
8. **Deliver claims.** On each subsequent check-in, the server populates `CheckInResponse.vigorepo_claims` with the labels that envoy was selected to mirror.
9. **Materialize.** The receiver-side agent module diffs `vigorepo_claims` against currently-materialized state, downloads each newly-claimed content seed via the swarm (merged into the standard download pipeline through a per-envoy wanted-labels handle), verifies its SHA256, unbundles into a root-owned bare repo at `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git`, and checks out the working tree at `~<user>/vigorepo/<project>/` as the target user (privilege-dropped via `run_as_user`). On the next reactor tick the cycle can re-run: a new bundle triggers an incremental unbundle; a claim dropped from the server's list waits out a 15-minute hysteresis window (rides out server restarts) then wipes both paths; `git gc --auto` sweeps on a per-repo 6-hour jittered schedule.

The server never runs `git` on bundle bytes and never downloads anything from the swarm. It reads sidecar metadata only from the agent's `vigorepo` trait, then trusts the agent's strict sidecar parser for validation. The agent-side parser at `agent/src/vigorepo/sidecar.rs` and the server-side parser at `server/vigorepo/sidecar.go` are kept in lockstep so the validation rules are identical regardless of which side parses the YAML.

## The `.vigorepo.yaml` Sidecar

Every synced repo must have a `.vigorepo.yaml` file at its root. This is mandatory opt-in: a `.git/` directory under `~<user>/vigorepo/` without a sidecar is left alone. Operators can have personal git work there that vigorepo never touches.

```yaml
owner: my-laptop
max_mirrors: 5
tags: [personal, dotfiles]
paused: false
description: "shell config + editor setup"

# Optional candidate filters — omit entirely for "mirror to any
# eligible envoy". Evaluated server-side in the ClaimTable before
# deterministic selection runs.
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

Before the server runs deterministic mirror selection, it walks the fleet and applies the base eligibility rules (user present, not the seeder, not revoked, not offline), then the three sidecar filters in this exact order:

1. **`include_envoys`** — whitelist. If the list is non-empty, an envoy must match at least one glob to stay in the pool. Empty or absent is "pass all".
2. **`exclude_envoys`** — blacklist. Envoys matching any glob are dropped, even if they passed the whitelist. Exclude always beats include.
3. **`when`** — per-candidate expression using the module-system DSL. Only two builtins are exposed to vigorepo:
   - `tag('name')` — true if the envoy's Tags list contains `name`
   - `trait_eq('key', 'value')` — true if the envoy's flattened trait `key` equals `value`

   Combine with `and` / `or` / `not`. Example: `tag('work') and trait_eq('os.family', 'linux')`. A parse error on the sidecar's `when:` fails the upsert for that source; a per-candidate evaluation error (missing trait, etc.) drops that candidate — vigorepo prefers "exclude on ambiguity" over "include on ambiguity" for personal-DR safety.

Hostname glob syntax is Go's `path.Match`: `*` (any segment), `?` (one char), `[abc]` (char class). Matching is case-insensitive. A malformed glob matches nothing (never accidentally "everything"). If the filtered pool is smaller than `max_mirrors`, the server emits the `vigorepo.under_replicated` trait exactly as it would for a naturally sparse pool — there's no distinction between "small fleet" and "narrow filter".

## Mirror Counts

`MIN_MIRRORS = 3` and `MAX_MIRRORS = 10` are immutable constants. Operators can request anything in `[3, 10]` via the sidecar's `max_mirrors` field; values outside that range are clamped with a warning.

`MIN_MIRRORS` is a **target, not a gate**. If the candidate pool has fewer than 3 eligible envoys, vigorepo materializes whatever is available and the server emits a `vigorepo.under_replicated` trait. Hard cap enforcement is the other direction: the server's `ClaimTable` runs deterministic selection with `min(max_mirrors, len(candidates))`, so overshoot is impossible by construction. There is no receiver-side jitter or majority-write quorum — the server is the single coordinator.

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
2. **Swarm seed authorization** — the existing `vigocli swarm allow-seed` allowlist gates which envoys can introduce new seeds. Vigorepo publishes through the same path, so the same allowlist applies.
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
| Receiver wipe hysteresis | 15 minutes after a label disappears from `vigorepo_claims` |

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
- Persistent `ClaimTable` (rebuilt from trait state on server restart)
- Web UI sidecar editor

## What Vigorepo Does NOT Do

- It is **not a backup tool.** It carries history but does not retain old versions, snapshots, or deleted commits beyond what git itself keeps.
- It is **not a sync tool.** Receivers are read-only; you cannot edit on a mirror and expect changes to flow back.
- It is **not a fleet distribution mechanism.** Use modules and `file:` resources for that.
- It does **not push from the source.** The trigger is a local commit detected by polling, not a git remote push.
- It does **not maintain a working tree on receivers.** Receivers hold a root-owned bare repo only; there is no `~<user>/vigorepo/<project>/` on a receiver. Users recover via `vigo repo recover <project>` — see [Receiver Layout](#receiver-layout).
- It does **not run git on the server.** The server only reads sidecar metadata; the agent owns all bundle creation and bundle application.

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

A label is flickering in and out of `vigorepo_claims`. Check the source envoy's check-in cadence — if it's been offline for more than 15 minutes (the receiver wipe hysteresis), the server's in-memory `ClaimTable` won't have current data for this label, and every receiver gets an empty claim. The wipe is bounded by the 15-minute window so this shouldn't cascade, but persistent flapping points at a source that's unstable.

### The receiver has a stale bare repo

Bare repos at `/var/lib/vigo/vigorepo/mirrors/<seeder>/<user>/<project>.git` that aren't in `receiver_state.json` are auto-removed at agent startup by `cleanup_orphan_bare_repos`. If you want to force a re-materialization without waiting for the next source commit, `sudo rm -rf` the bare repo and the next reactor tick detects the missing state entry and pulls a fresh bundle from the swarm cache.

### Recovered a repo and then the source pushed a new commit — why isn't my clone updating?

Once you run `vigo repo recover <project>`, the cloned path is a normal git repo that the receiver pipeline does not manage. It's yours. Subsequent source commits flow into the bare mirror at `/var/lib/vigo/vigorepo/mirrors/...` (which continues to update), but they do not touch your recovered clone. If you want the latest, re-run `vigo repo recover <project>` into a different path, or `git pull` your clone against the bare mirror if you set it up as a remote.

## Related

- [Swarm](swarm.md) — the underlying P2P content distribution layer that vigorepo publishes through
- [Vigosync](vigosync.md) — sister subsystem for LAN-only file sync (no git, no server)
- [`vigorepo` trait reference](../reference/traits/vigorepo.md) — full schema of the `vigorepo` trait emitted by the source-side reactor

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
