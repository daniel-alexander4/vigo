---
title: "ADR-013: Multi-member gitback"
---

# ADR-013: Multi-member gitback

**Status:** Superseded by ADR-015 (2026-04-29) before implementation. Decision content preserved below for historical reasoning; the unified design that absorbs and extends this ADR is in `015-unified-gitback-substrate.md`.
**Date:** 2026-04-29

## Context

Single-user gitback is a one-publisher fanout subsystem: one user on one envoy holds the source of truth in `~user/gitback/<project>/`, the agent packs bundles, and every other (non-blacklisted) envoy in the fleet receives a root-owned bare-repo replica at `mirrors/<seeder>/<user>/<project>.git`. Recovery into a user-owned clone is one-way via `vigo swarm gitback recover`. The model assumes a single writer per `(seeder, user, project)` tuple; multi-writer collaboration is explicitly out of scope.

Multiple users on different envoys collaborating on a single logical repo — small-team / family-scale git workflow without a central forge — is the use case this ADR addresses. The constraint set is unusual for a distributed-collaboration system: there is no central server in the data plane (P2P is required), every member needs git's normal push/pull semantics, and identity must be stable enough to gate writes without re-creating a forge's account model. The natural anchor is git itself — git's fast-forward semantics provide all the conflict arbitration the system needs — but the membership question still has to be answered.

A first-principles P2P design eliminates shared mutable membership state entirely by replacing "who's in the group" with "who can produce a valid capability token." This shifts the design closer to SSH CA mode, Macaroons, and Keybase sigchain than to a forge.

## Decision

### Identity & membership: capability tokens anchored to a self-certifying project ID

- **Project ID is the founder's pubkey fingerprint.** `vigo swarm gitback multi init <handle>` generates a project ed25519 keypair locally; the project's URL is `gitback://<sha256(project_pubkey)>/<handle>`. The handle is human-readable but the cryptographic identity is the fingerprint. Name squatting is impossible by construction — different fingerprints, different URLs.
- **Membership is a stack of capability tokens, not a list.** Each member's envoy holds a signed token of the form `(project_id, holder_envoy_fingerprint, holder_username, role, [expiry])`, signed by the project key (or by an admin token). Tokens live at `~/.vigo-gitback/<project_id>/token` on the holder's envoy. **There is no `members.toml`.**
- **Roles are `admin` and `member`.** Admin tokens authorize their holder to sub-issue further tokens. Verification walks the chain back to the project pubkey. Cap delegation depth at 3 to bound runaway issuance.
- **Authority on push: token signature chain + revocation list.** A push from member C presents C's token; the receiving envoy verifies the chain back to the project pubkey baked into the URL, then checks that no envelope in the local revocation list names C's token-id. Both must pass.

### Revocation: append-only signed envelopes, gossiped on the swarm substrate

- Admin issues a `Revoke(token_id, revoked_at)` envelope, signed by the issuing admin's token. Envelope gossips via the existing swarm transport (same shape as `agent/src/swarm/lockbox/leaves.rs`).
- Each envoy pins received revocations to a per-project local list. Pushes naming a revoked token are rejected.
- Retention: 90 days, reaped via the same scheduler lockbox uses.

### Transport: `gitback://` URL handled by a multi-call helper

- Users do `git remote add origin gitback://<project_id>/<handle>` then plain `git push` and `git pull`.
- The `vigo` binary itself dispatches as `git-remote-gitback` when invoked under that argv[0] (multi-call binary, not a separate executable — saves ~3–5 MiB of duplicate static linkage).
- Helper runs as the calling user. It connects to the agent over a `SO_PEERCRED`-gated Unix socket at `$XDG_RUNTIME_DIR/vigo/gitback.sock` (mirroring lockbox's session-helper pattern). Username comes from SO_PEERCRED; envoy fingerprint comes from the agent's mTLS cert.
- The agent (running as root) enforces token validity, ref policy, and proxies to `git-receive-pack` / `git-upload-pack` against the local bare repo.

### Storage: per-member bare repo at a flat path

- Multi-member mirrors land at `/var/lib/vigo/swarm/gitback/multi/<project_id>.git`, root-owned, 0755 — same posture as single-user mirrors today.
- Single-user gitback's `mirrors/<seeder>/<user>/<project>.git` layout is unchanged. The two modes are fully orthogonal — a project name can exist in both shapes without conflict.
- Distribution scope: **only listed members hold the multi-member repo.** Non-members in the fleet do not get a copy. Members get full repos (bare + working tree) → an N-member project = N independent recoverable replicas.

### Conflict policy: fast-forward only, `--force-with-lease` accepted

- Default policy is FF on every branch.
- `--force-with-lease` is accepted (atomic check-and-replace using the SHA the client thinks is current).
- Bare `--force` is rejected at the daemon's pre-receive hook — before any objects land.
- Concurrent same-branch pushes from different members: one wins, the other receives a non-fast-forward error with `"pull and rebase first"`. Standard git workflow handles divergence; this is acceptable for personal/small-team collaboration. **Multi-member is not a forge replacement.**

### Fan-out: post-pack ref-delta, gated on actual change

- After `git-receive-pack` completes successfully on the local bare repo, the daemon computes `git rev-parse --all` before/after. If any ref changed, the daemon fans out a bundle to other listed members' envoys using the existing swarm peer-port (1531) machinery.
- If no refs changed (idempotent ingest of already-present commits), no fan-out fires. **This is the cycle-prevention mechanism** — works correctly across partitions, rejoins, and concurrent pushes.

### Server's role: phonebook only

The server is **not authoritative for membership.** It provides three thin services:

1. **gitbackmesh aggregator extension:** surfaces `(project_id, member_count, last_rotation_ts)` per multi-member project. **No member roster on check-in** — that bloats the hot path. Detailed rosters are fetched on-demand by the helper at push time via REST. This is the lazy / push-time resolution decision (2026-04-29 scrutinize).
2. **Cross-subnet address resolution:** existing mechanism, scope extended to multi-member envoys.
3. **Audit log entries:** join, leave, admin-rotation, revoke events flow through the existing audit hash chain.

A multi-member project can in principle operate without the server if members manually distribute envoy addresses (compatible with air-gapped setups). Server is convenience, not authority.

### Founder-loss recovery

The repo content is always recoverable as long as one member is alive — every member holds a full bare repo and a full working tree.

If founder + all admins are lost (no one with a valid admin token), the project enters **frozen membership**: existing token-holders continue working; no new members can join, no revocations can issue. Two paths:

1. **Soldier on.** Existing roster keeps working indefinitely. Acceptable for stable small teams.
2. **Hard-fork via `vigo swarm gitback multi reanchor <old_project_id>`.** A surviving member generates a fresh project keypair, the new project ID is `sha256(new_pubkey)`, all repo content is imported wholesale, members manually re-add the new remote. Old URL is dead. This is a deliberate ceremony.

**No quorum / threshold-signature recovery in v1.** Adding it requires an `(K, N)` declaration at init time and is significant crypto complexity. Defer until a real user needs it.

### CLI verbs

- `vigocli swarm gitback multi init <handle>` — founder; generates project keypair + initial admin token for caller.
- `vigocli swarm gitback multi invite <project_id> --envoy <fp> --user <u> --role <r>` — admin only; mints a token for the named recipient.
- `vigo swarm gitback multi join <project_id> <founder-host>` — joining envoy; fetches the repo, presents its invite token, pins the founder pubkey via TOFU.
- `vigo swarm gitback multi leave <project_id>` — self-leave; emits a self-revoke envelope and clears local state.
- `vigocli swarm gitback multi list` — fleet-wide multi-member projects via gitbackmesh.
- `vigocli swarm gitback multi revoke <project_id> --token-id <id>` — admin only; gossips a revocation envelope.
- `vigo swarm gitback multi reanchor <project_id>` — surviving member; hard-fork ceremony.

## Alternatives Considered

**In-repo signed `members.toml` + `members.toml.sig`.** Initial proposal. Rejected because the file lives in N working trees on N envoys with no single authority — any member with admin role can edit their local copy and self-add, and propagation of authoritative changes through bundles vs. working-tree files is a coordination problem the design didn't solve. Capability tokens eliminate the shared file entirely.

**Server-authoritative membership.** Membership lives on the vigosrv, agents pull authoritative member lists during check-in. Rejected because it breaks "P2P-first" — the explicit framing for this subsystem. The capability-token model preserves P2P semantics with the server reduced to a phonebook role.

**Threshold / k-of-n signatures for membership rotation.** Robust against founder loss, but requires upfront `(K, N)` declaration, more sophisticated crypto (FROST or similar), and additional protocol surface for collective signing ceremonies. Deferred — soldier-on / hard-fork covers v1; quorum can be added later if real demand surfaces.

**Web-of-trust vouching** (members vouch for new members; threshold of vouches admits). Rejected — bootstrap problem in small groups (founder needs trust before there's anyone to vouch), and the guarantees become weak at the family / small-team scale this is designed for.

**Git-native signed-tag chain** (membership as commits on `refs/vigo/members` branch, signed). Considered. Cleaner reuse of git's own infrastructure, but requires every member to have signed-commit support enabled and verifying chains over a possibly-long history is slower than a flat token check. Capability tokens are simpler at the same trust strength.

## Consequences

**Files to modify (agent — Rust):**
- `agent/src/swarm/gitback/` — new modules for token issuance/parse/verify (mirroring `lockbox/leaves.rs` shape), the multi-call helper dispatch (argv[0] check), receive-pack proxy with ref-policy enforcement, fan-out-on-push trigger, revocation list (gossip + persist + 90-day reap).
- `agent/src/swarm/gitback/cli.rs` — new `multi {init,invite,join,leave,list,revoke,reanchor}` verbs.
- `agent/src/swarm/gitback/sidecar.rs` — no schema change for v1 (multi-member is detected by the presence of `~/.vigo-gitback/<project_id>/` state, not a sidecar field).
- New Unix socket daemon under `agent/src/swarm/gitback/socket.rs` (or similar) — `SO_PEERCRED`-gated, mirroring lockbox's session-helper.

**Files to modify (server — Go):**
- `server/swarm/gitbackmesh/` — extend aggregator to surface multi-member project metadata (id, member_count, last_rotation_ts). No detailed rosters in check-in payload (lazy fetch on push).
- New REST endpoint for member-roster lookup (push-time) under `server/api/swarm_gitback.go`.
- Audit-log entries for join / leave / admin-rotation / revoke events.

**Documentation:**
- `docs/concepts/gitback-multi.md` — the multi-member model, the founder-loss recovery posture, the conflict-resolution semantics.
- `docs/reference/cli/agent-swarm-gitback.md` and `docs/reference/cli/swarm-gitback.md` — new `multi` subcommand tree.

**Operational:**
- Agent rebuild + cross-compile + redeploy required (Z-level version bump).
- Server rebuild required.
- No new dependencies — reuses `ed25519-dalek`, `serde`, `tokio`, `hyper`, `nix` already linked.

**Binary weight (estimated):**
- Agent: ~50–150 KiB (≤2 % of current 7.7 MiB).
- Server: ~100–200 KiB (sub-percent of vigosrv).
- `git-remote-gitback` ships as a multi-call dispatch from `vigo`, not a separate binary.

**Hot-path posture:**
- Only the gitbackmesh check-in extension lands on the convergence-cycle path. Bounded by including project metadata only (no per-project rosters); rosters fetch on-demand at push time.
- Bundle ingestion on swarm peer-port and fan-out workers are off the convergence loop, capped at 2 concurrent (matching filecast distribution).
- Trait collection for multi-member status caches snapshots and recomputes only on state change (mark-dirty pattern from lockbox).
- Revocation list lookup is O(1) per push; not on convergence path.

**Behavioral consequences:**
- Multi-member is opt-in (created via `multi init`). Existing single-user gitback users see no change.
- Multi-member repos distribute only to members, not fleet-wide. Operators wanting fleet-wide DR for a multi-member project can also configure single-user gitback for the same content, in parallel.
- Concurrent same-branch pushes can produce non-FF rejections; convergence is via standard `pull --rebase`. Documented; acceptable for the target use case.
