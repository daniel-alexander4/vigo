
# Vigogit — Personal-DR Git Mirroring

Vigogit auto-mirrors a small number of a user's personal git repos from one **source** envoy to a bounded handful of **receiver** envoys where the same user has an account. The trigger is `git commit`, not `git push`. Receivers are read-only mirrors that hold a checked-out working tree plus a separate bare repo, so a user who loses their primary machine can `git clone` off any mirror to recover their work.

Vigogit is a personal-disaster-recovery tool, not a fleet content distribution mechanism. Fleet-wide content distribution is what modules and `file:` resources are for. Vigogit carries history.

## MVP Status

The source side and the server-side selection are wired end-to-end and tested. The receiver-side agent module is still pending; until it lands, vigogit will publish bundles into the swarm and the server will compute per-envoy claims, but no envoy will materialize a mirror.

| Piece | Status |
|-------|--------|
| Source-side reactor + bundle publish | ✅ |
| Server-side `ClaimTable` + deterministic mirror selection | ✅ |
| `vigogit` trait collector (operator visibility) | ✅ |
| `CheckInResponse.vigogit_claims` proto field | ✅ |
| Receiver-side materialization (`agent/src/vigogit/receiver.rs`) | ⏳ |
| `vigocli vigogit` subcommands | ⏳ |
| Web UI page at `/vigogit` | ⏳ |

## How It Works

1. **Opt in.** A user creates a git repo at `~<user>/vigogit/<project>/` and commits a `.vigogit.yaml` sidecar at the repo root. The sidecar names the source envoy and how many mirrors to maintain.
2. **Discover.** Every 5 seconds, the agent's vigogit reactor scans every local user's `~/vigogit/*` directory, parses each sidecar with a strict deny-unknown-fields validator, and excludes anything whose `owner` field doesn't match this envoy's hostname (so other envoys' `~/vigogit/` directories are left alone).
3. **Bundle.** For each non-paused source whose `HEAD` has advanced since the last publish, the reactor runs `git bundle create` (as the owning Unix user, never as root) into `/var/lib/vigo/vigogit/tmp/`. The first bundle is full; subsequent bundles are incremental from the last published commit.
4. **Dual-publish.** The source writes two seeds into the swarm seed directory:
   - **Meta seed** `vigogit-meta:<host>:<user>:<project>` containing the literal `.vigogit.yaml` bytes (~1 KB).
   - **Content seed** `vigogit:<host>:<user>:<project>` containing the git bundle.
   The meta seed is written **first** so receivers and the server never see a content seed without its meta companion.
5. **Gossip.** The existing swarm trait collector hashes each new seed file and the check-in loop's `register_seeds_in_manifest` call adds it to the swarm CRDT. From there it propagates fleet-wide via the normal swarm gossip path.
6. **Trait report.** The reactor writes a snapshot file at `/var/lib/vigo/vigogit/traits.json` and the `vigogit` trait collector reads it on each check-in. The trait carries per-source labels, current `git_sha` / `blob_sha`, validated sidecar fields, and any source/publish errors.
7. **Server selects.** The server-side `Builder` consumes each `vigogit.sources[]` entry from the trait report, builds a candidate pool from the fleet (envoys whose `users.human.*` traits include the target user, excluding the seeder itself, revoked envoys, and offline envoys), and runs deterministic selection: `sha256(label || hostname)` ascending, take the first `min(max_mirrors, len(candidates))`. Results live in an in-memory `ClaimTable`.
8. **Deliver claims.** On each subsequent check-in, the server populates `CheckInResponse.vigogit_claims` with the labels that envoy was selected to mirror.
9. **Materialize.** *(Pending.)* The receiver-side agent module diffs `vigogit_claims` against currently-materialized state, downloads the meta + content seeds via the swarm, verifies the seeder identity, unbundles into a root-owned bare repo at `/var/lib/vigo/vigogit/<seeder>/<user>/<project>.git`, and checks out the working tree at `~<user>/vigogit/<project>/` as the target user.

The server never runs `git` on bundle bytes. It reads metadata only from the meta seed (which is just YAML), then trusts the agent's strict sidecar parser for validation. This is enforced architecturally: the server-side Go parser at `server/vigogit/sidecar.go` is a faithful mirror of the Rust parser at `agent/src/vigogit/sidecar.rs` for cross-language parity.

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

1. **Swarm transport security** — every byte moves over mTLS on the existing swarm peer port (1531). An envoy can only be a seeder if it's enrolled and approved.
2. **Swarm seed authorization** — the existing `vigocli swarm allow-seed` allowlist gates which envoys can introduce new seeds. Vigogit publishes through the same path, so the same allowlist applies.
3. **Receiver-side seeder identity check** *(pending with the receiver module)* — every label embeds the seeder hostname as its second segment (`vigogit:<seeder>:<user>:<project>`). The receiver verifies that the mTLS-authenticated peer it actually downloaded the bytes from has a hostname matching that segment. A misbehaving envoy cannot publish a label claiming to seed someone else's source.

Note that the sidecar's `owner` field is **not** a security boundary. It's a single-writer hint that prevents two envoys from accidentally both believing they're the source for the same repo. The actual security boundary is the seeder-segment verification above.

## State Storage

| Path | Owner | Contents |
|------|-------|----------|
| `/var/lib/vigo/vigogit/state.json` | `root:root` 0600 | per-label `{git_sha, blob_sha}` so restarts don't re-publish identical bundles |
| `/var/lib/vigo/vigogit/traits.json` | `root:root` 0600 | latest reactor pass snapshot for the `vigogit` trait collector |
| `/var/lib/vigo/vigogit/tmp/` | mode `1777` (sticky like `/tmp`) | scratch directory for `git bundle create` output before atomic move into the swarm seed dir |
| `/var/lib/vigo/vigogit/<seeder>/<user>/<project>.git` | `root:root` | bare receiver repo (created by the receiver module, pending) |
| `~<user>/vigogit/<project>/` | `<user>:<user>` | source repo (operator-created) or receiver working tree (no `.git/`) |

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
| Receiver `git gc --auto` jitter window | 6 hours |
| Meta-arrival wait on receiver | 30 seconds |

## Platform Support

| Platform | Source side | Receiver side |
|----------|-------------|---------------|
| Linux | ✅ | ⏳ pending |
| macOS | requires git ≥ 2.35 | ⏳ pending |
| FreeBSD / OpenBSD / NetBSD | requires git ≥ 2.35 | ⏳ pending |
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

Until the receiver lands, troubleshooting is mostly source-side and server-visibility-side.

### My source isn't publishing

1. **Check git version.** `git --version` on the source must be 2.35 or newer. The agent log will show `vigogit: git version check failed; reactor will not start until resolved` if it's lower.
2. **Check the sidecar.** Run the sidecar through the agent log filter — `journalctl -u vigo-envoy -e | grep -i vigogit` should show a discovery error if the sidecar is malformed. The most common rejections are unknown fields (typo in `description` etc.) and missing fields.
3. **Check ownership.** The directory under `~<user>/vigogit/<project>/` must be owned by `<user>`, not by root. Vigogit drops privileges to the directory's owner before running `git`.
4. **Check the `.vigogit.yaml` `owner` field.** It must match this envoy's hostname (case-insensitive). If you copied a sidecar from another machine, update the field.
5. **Check the trait.** The agent's `vigogit` trait carries `source_errors` and `publish_errors` lists. `vigocli traits get <envoy>` will surface them once the CLI subcommand lands; until then they appear in the trait JSON in the database.

### The server has no claims for my source

1. **Confirm the source is being published.** Look at the source envoy's `vigogit` trait — the source should appear in `sources[]` with a `git_sha` and `blob_sha` populated.
2. **Confirm the server received the trait.** Check the server log for a `vigogit: trait report had errors` warning, which would indicate a parse problem on the server side.
3. **Confirm the candidate pool isn't empty.** Vigogit needs at least one other envoy in the fleet whose `users.human.*` trait contains the target user. A source whose target user only exists on the source itself will get an empty mirror list.
4. **Confirm the seeder hostname matches.** The label's `<seeder>` segment must equal the reporting envoy's hostname in the fleet index. A mismatch is rejected as a security violation and logged on the server.

### `vigogit.under_replicated` is set

The candidate pool is smaller than the sidecar's requested `max_mirrors`. Either lower `max_mirrors` in the sidecar, or add more envoys whose `users.human.*` trait includes the target user.

## Related

- [Swarm](swarm.md) — the underlying P2P content distribution layer that vigogit publishes through
- [Vigosync](vigosync.md) — sister subsystem for LAN-only file sync (no git, no server)
- [`vigogit` trait reference](../reference/traits/vigogit.md) — full schema of the `vigogit` trait emitted by the source-side reactor

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
