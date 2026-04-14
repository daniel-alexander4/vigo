
# Vigogit — Personal-DR Git Mirroring

Vigogit auto-mirrors a small number of a user's personal git repos from one **source** envoy to a bounded handful of **receiver** envoys where the same user has an account. The trigger is `git commit`, not `git push`. Receivers are read-only mirrors that hold a checked-out working tree plus a separate bare repo, so a user who loses their primary machine can `git clone` off any mirror to recover their work.

Vigogit is a personal-disaster-recovery tool, not a fleet content distribution mechanism. Fleet-wide content distribution is what modules and `file:` resources are for. Vigogit carries history.

## MVP Status

The full source → server → receiver pipeline is wired end-to-end in code and unit-tested. Real home-lab verification is still pending and requires `git >= 2.35` on the source host (`git_ops::check_version` refuses to start the reactor on older hosts).

| Piece | Status |
|-------|--------|
| Source-side reactor + bundle publish | ✅ |
| Server-side `ClaimTable` + deterministic mirror selection | ✅ |
| `vigogit` trait collector (operator visibility) | ✅ |
| `CheckInResponse.vigogit_claims` proto field | ✅ |
| Receiver-side materialization (`agent/src/vigogit/receiver.rs`) | ✅ (code landed, unit-tested; home-lab e2e run pending) |
| Per-label `mirror_failures` trait emission | ⏳ |
| `vigocli vigogit` subcommands | ⏳ |
| Web UI page at `/vigogit` + REST endpoints | ⏳ |

## How It Works

1. **Opt in.** A user creates a git repo at `~<user>/vigogit/<project>/` and commits a `.vigogit.yaml` sidecar at the repo root. The sidecar names the source envoy and how many mirrors to maintain.
2. **Discover.** Every 5 seconds, the agent's vigogit reactor scans every local user's `~/vigogit/*` directory, parses each sidecar with a strict deny-unknown-fields validator, and excludes anything whose `owner` field doesn't match this envoy's hostname (so other envoys' `~/vigogit/` directories are left alone).
3. **Bundle.** For each non-paused source whose `HEAD` has advanced since the last publish, the reactor runs `git bundle create` (as the owning Unix user, never as root) into `/var/lib/vigo/vigogit/tmp/`. The first bundle is full; subsequent bundles are incremental from the last published commit.
4. **Publish.** The source writes a content seed `vigogit:<host>:<user>:<project>` (containing the git bundle) into the swarm seed directory. There is no separate meta seed: the validated sidecar fields are routed to the server through the `vigogit` trait, so a swarm-side meta blob would be dead weight.
5. **Gossip.** The existing swarm trait collector hashes the new seed file and the check-in loop's `register_seeds_in_manifest` call adds it to the swarm CRDT. From there it propagates fleet-wide via the normal swarm gossip path. Vigogit content seeds are flagged as **claim-only** in the manifest (label prefix `vigogit:`), so the standard target-spec self-assignment path explicitly skips them — the only way to download a vigogit bundle is via a server-issued claim.
6. **Trait report.** The reactor writes a snapshot file at `/var/lib/vigo/vigogit/traits.json` and the `vigogit` trait collector reads it on each check-in. The trait carries per-source labels, current `git_sha` / `blob_sha`, validated sidecar fields (`max_mirrors`, `tags`, `description`), and any source/publish errors.
7. **Server selects.** The server-side `Builder` consumes each `vigogit.sources[]` entry from the trait report, builds a candidate pool from the fleet (envoys whose `users.human.*` traits include the target user, excluding the seeder itself, revoked envoys, and offline envoys), and runs deterministic selection: `sha256(label || hostname)` ascending, take the first `min(max_mirrors, len(candidates))`. Results live in an in-memory `ClaimTable`.
8. **Deliver claims.** On each subsequent check-in, the server populates `CheckInResponse.vigogit_claims` with the labels that envoy was selected to mirror.
9. **Materialize.** The receiver-side agent module diffs `vigogit_claims` against currently-materialized state, downloads each newly-claimed content seed via the swarm (merged into the standard download pipeline through a per-envoy wanted-labels handle), verifies its SHA256, unbundles into a root-owned bare repo at `/var/lib/vigo/vigogit/mirrors/<seeder>/<user>/<project>.git`, and checks out the working tree at `~<user>/vigogit/<project>/` as the target user (privilege-dropped via `run_as_user`). On the next reactor tick the cycle can re-run: a new bundle triggers an incremental unbundle; a claim dropped from the server's list waits out a 15-minute hysteresis window (rides out server restarts) then wipes both paths; `git gc --auto` sweeps on a per-repo 6-hour jittered schedule.

The server never runs `git` on bundle bytes and never downloads anything from the swarm. It reads sidecar metadata only from the agent's `vigogit` trait, then trusts the agent's strict sidecar parser for validation. The agent-side parser at `agent/src/vigogit/sidecar.rs` and the server-side parser at `server/vigogit/sidecar.go` are kept in lockstep so the validation rules are identical regardless of which side parses the YAML.

## The `.vigogit.yaml` Sidecar

Every synced repo must have a `.vigogit.yaml` file at its root. This is mandatory opt-in: a `.git/` directory under `~<user>/vigogit/` without a sidecar is left alone. Operators can have personal git work there that vigogit never touches.

```yaml
owner: my-laptop
max_mirrors: 5
tags: [personal, dotfiles]
paused: false
description: "shell config + editor setup"
```

| Field | Required | Validation |
|-------|----------|------------|
| `owner` | yes | DNS hostname `[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)*`, lowercased |
| `max_mirrors` | yes | int, clamped into `[3, 10]` with a warning if out of range |
| `tags` | yes | list of ≤ 16 strings, each ≤ 32 chars (may be `[]`) |
| `paused` | yes | bool — `true` skips bundle creation, existing seeds retention-expire |
| `description` | yes | string ≤ 256 chars (may be `""`) |

Strict parsing: missing fields, unknown fields, malformed YAML, duplicate keys, or files larger than 8 KB are all rejected. The same validator runs on both the source agent (Rust) and the server (Go) — drift between them would let a malformed sidecar smuggle through one side, so they're kept in lockstep.

## Mirror Counts

`MIN_MIRRORS = 3` and `MAX_MIRRORS = 10` are immutable constants. Operators can request anything in `[3, 10]` via the sidecar's `max_mirrors` field; values outside that range are clamped with a warning.

`MIN_MIRRORS` is a **target, not a gate**. If the candidate pool has fewer than 3 eligible envoys, vigogit materializes whatever is available and the server emits a `vigogit.under_replicated` trait. Hard cap enforcement is the other direction: the server's `ClaimTable` runs deterministic selection with `min(max_mirrors, len(candidates))`, so overshoot is impossible by construction. There is no receiver-side jitter or majority-write quorum — the server is the single coordinator.

## Hybrid Layout on Receivers

Receivers hold each mirror as **two distinct paths**:

- **Bare repo** at `/var/lib/vigo/vigogit/<seeder>/<user>/<project>.git` — root-owned, contains the full git history. Never visible to the user.
- **Working tree** at `~<user>/vigogit/<project>/` — owned by the target user, contains a checked-out snapshot of `HEAD`. **Has no `.git/` directory.**

Without a `.git/` in the working tree, a user on a receiver cannot run `git status`, `git log`, or `git commit`. This is intentional and FAQ'd: vigogit mirrors are for disaster recovery, not for editing. If a user wants to recover work, they `git clone /var/lib/vigo/vigogit/<seeder>/<user>/<project>.git ~/recovered-project/` and proceed normally.

The split also closes a class of security holes: a malicious bundle cannot inject git hooks, `gitattributes` smudge filters, or submodule URLs that get executed against the user's environment, because the user-visible path has no `.git/` for git to find.

## Source Identity and Security

Vigogit does not have its own authentication layer. Security is provided by three existing layers stacked together:

1. **Swarm transport security** — every byte moves over mTLS on the existing swarm peer port (1531). An envoy can only be a seeder if it's enrolled and approved. Content integrity is guaranteed by SHA256 verification of every assembled blob (`agent/src/swarm/cache.rs`).
2. **Swarm seed authorization** — the existing `vigocli swarm allow-seed` allowlist gates which envoys can introduce new seeds. Vigogit publishes through the same path, so the same allowlist applies.
3. **Server-mediated seeder→label verification** — every label embeds the seeder hostname as its second segment (`vigogit:<seeder>:<user>:<project>`). The *server* validates the match against authoritative fleet data in `server/vigogit/builder.go::OnTraitReport` before it ever issues a claim: if an envoy reports a `vigogit.sources[]` entry whose label claims a different seeder than the envoy's own hostname in the fleet index, the entry is rejected. Receivers then trust the claim list because it arrives over the existing mTLS-authenticated check-in channel and has already been server-validated. They never act on raw manifest entries.

Note that the check is server-mediated rather than receiver-side because peer-to-peer blob downloads deliberately disable TLS hostname verification (`agent/src/swarm/peer_client.rs::build_peer_agent` calls `disable_verification(true)`) — peers connect by IP and the cert SAN is hostname-based, so the wire identity is unavailable to the downloader. Content integrity still comes from SHA256; identity comes from the fleet index.

Note that the sidecar's `owner` field is **not** a security boundary. It's a single-writer hint that prevents two envoys from accidentally both believing they're the source for the same repo. The actual security boundary is the server's seeder→label verification above.

## State Storage

| Path | Owner | Contents |
|------|-------|----------|
| `/var/lib/vigo/vigogit/state.json` | `root:root` 0600 | publisher: per-label `{git_sha, blob_sha}` so restarts don't re-publish identical bundles |
| `/var/lib/vigo/vigogit/traits.json` | `root:root` 0600 | latest reactor pass snapshot for the `vigogit` trait collector |
| `/var/lib/vigo/vigogit/tmp/` | mode `1777` (sticky like `/tmp`) | scratch directory for `git bundle create` output before atomic move into the swarm seed dir |
| `/var/lib/vigo/vigogit/receiver_state.json` | `root:root` 0600 | receiver: per-label `{blob_sha, git_sha, next_gc_at, ...}` + `pending_wipes` for the hysteresis window |
| `/var/lib/vigo/vigogit/mirrors/<seeder>/<user>/<project>.git` | `root:root` | bare receiver repo. The `mirrors/` subdirectory keeps bare repos namespaced away from publisher state files so the receiver's orphan-cleanup walker has a clean boundary |
| `~<user>/vigogit/<project>/` | `<user>:<user>` | source repo (operator-created, has `.git/`) or receiver working tree (no `.git/`) |

State files survive agent restarts. The tmp directory is GCed on agent startup for any `*.bundle` older than 1 hour (interrupted bundle operations).

## Activation

There is no global configuration switch. The vigogit reactor starts automatically on every agent that:

1. Has a working git binary at version ≥ 2.35 (`git_ops::check_version()` rejects older hosts at startup with a warning, and the reactor declines to start).
2. Has at least one user with a `~<user>/vigogit/<project>/` directory containing a valid `.vigogit.yaml`.

The server-side `ClaimTable` and `Builder` start unconditionally — even without any vigogit sources in the fleet they sit empty, costing nothing.

## Limits

| Limit | Value |
|-------|-------|
| Max bundle size | 2 GB per bundle |
| Min mirrors | 3 (target, not gate — under-replicated emits a trait) |
| Max mirrors | 10 (hard cap, server-enforced) |
| Sidecar size | 8 KB |
| Description length | 256 chars |
| Tags | 16 entries, 32 chars each |
| Project name length | 64 bytes |
| Reactor tick | 5 seconds (also the commit debounce window) |
| Receiver `git gc --auto` jitter window | 6 hours per repo |
| Receiver wipe hysteresis | 15 minutes after a label disappears from `vigogit_claims` |

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
- Branch selection — vigogit always publishes `HEAD`
- Submodules
- `expose_git=true` (writable receiver repos, allowing two-way sync)
- Automatic source handoff
- Keepalive mechanism for paused seeds
- Persistent `ClaimTable` (rebuilt from trait state on server restart)
- Web UI sidecar editor

## What Vigogit Does NOT Do

- It is **not a backup tool.** It carries history but does not retain old versions, snapshots, or deleted commits beyond what git itself keeps.
- It is **not a sync tool.** Receivers are read-only; you cannot edit on a mirror and expect changes to flow back.
- It is **not a fleet distribution mechanism.** Use modules and `file:` resources for that.
- It does **not push from the source.** The trigger is a local commit detected by polling, not a git remote push.
- It does **not maintain `.git/` on the working tree.** This is intentional — see [Hybrid Layout on Receivers](#hybrid-layout-on-receivers).
- It does **not run git on the server.** The server only reads sidecar metadata; the agent owns all bundle creation and bundle application.

## Troubleshooting

### My source isn't publishing

1. **Check git version.** `git --version` on the source must be 2.35 or newer. The agent log will show `vigogit: git version check failed; reactor will not start until resolved` if it's lower.
2. **Check the sidecar.** Run the sidecar through the agent log filter — `journalctl -u vigo-envoy -e | grep -i vigogit` should show a discovery error if the sidecar is malformed. The most common rejections are unknown fields (typo in `description` etc.) and missing fields.
3. **Check ownership.** The directory under `~<user>/vigogit/<project>/` must be owned by `<user>`, not by root. Vigogit drops privileges to the directory's owner before running `git`.
4. **Check the `.vigogit.yaml` `owner` field.** It must match this envoy's hostname (case-insensitive). If you copied a sidecar from another machine, update the field.
5. **Check the trait.** The agent's `vigogit` trait carries `source_errors` and `publish_errors` lists.

### The server has no claims for my source

1. **Confirm the source is being published.** Look at the source envoy's `vigogit` trait — the source should appear in `sources[]` with a `git_sha` and `blob_sha` populated.
2. **Confirm the server received the trait.** Check the server log for a `vigogit: trait report had errors` warning, which would indicate a parse problem on the server side.
3. **Confirm the candidate pool isn't empty.** Vigogit needs at least one other envoy in the fleet whose `users.human.*` trait contains the target user. A source whose target user only exists on the source itself will get an empty mirror list.
4. **Confirm the seeder hostname matches.** The label's `<seeder>` segment must equal the reporting envoy's hostname in the fleet index. A mismatch is rejected as a security violation and logged on the server.

### `vigogit.under_replicated` is set

The candidate pool is smaller than the sidecar's requested `max_mirrors`. Either lower `max_mirrors` in the sidecar, or add more envoys whose `users.human.*` trait includes the target user.

### A receiver refuses to materialize a claimed label

The receiver applies a hard conflict check against anything it doesn't already own: it will not clobber a `.git/` directory (could be an operator's source repo) or a non-empty working tree with unknown contents. Both conditions log a `vigogit receiver: mirror failure` warning naming the label and reason. Recovery: `rm -rf ~<user>/vigogit/<project>/` on the receiver and the next 5-second reactor tick picks up the now-clean slate.

### A mirror keeps getting wiped and re-downloaded

A label is flickering in and out of `vigogit_claims`. Check the source envoy's check-in cadence — if it's been offline for more than 15 minutes (the receiver wipe hysteresis), the server's in-memory `ClaimTable` won't have current data for this label, and every receiver gets an empty claim. The wipe is bounded by the 15-minute window so this shouldn't cascade, but persistent flapping points at a source that's unstable.

### The receiver has a stale bare repo

Bare repos at `/var/lib/vigo/vigogit/mirrors/<seeder>/<user>/<project>.git` that aren't in `receiver_state.json` are auto-removed at agent startup by `cleanup_orphan_bare_repos`. If you want to force a re-materialization without waiting for the next commit, delete both the bare repo and the working tree — the receiver tick will detect the missing state entry and pull a fresh bundle from the swarm.

## Related

- [Swarm](swarm.md) — the underlying P2P content distribution layer that vigogit publishes through
- [Vigosync](vigosync.md) — sister subsystem for LAN-only file sync (no git, no server)
- [`vigogit` trait reference](../reference/traits/vigogit.md) — full schema of the `vigogit` trait emitted by the source-side reactor

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
