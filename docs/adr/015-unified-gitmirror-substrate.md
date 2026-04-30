---
title: "ADR-015: Unified gitmirror substrate"
---

# ADR-015: Unified gitmirror substrate

**Status:** Accepted (pending implementation). Supersedes ADR-013.
**Date:** 2026-04-29

## Context

ADR-013 designed multi-member gitmirror as a separate subsystem layered alongside today's single-user personal-DR gitmirror, with capability tokens, self-certifying project IDs, and a parallel storage layout (`mirrors/multi/<project_id>.git`). Single-user gitmirror was left untouched. Under scrutiny that left two parallel mechanisms doing fundamentally the same thing (P2P git substrate with identity, transport, storage) — which the zero-users doctrine in `CLAUDE.md` says to avoid in favor of unifying behind a single primitive.

Two further observations made unification the correct call:

1. **Single-user gitmirror's permission posture is a security gap.** Bare repos at `mirrors/<seeder>/<user>/<project>.git` are `0755 root:root`, world-readable; any user on any receiver can `git clone` them via plain git. The per-user CLI filter is cosmetic. Multi-member gitmirror was going to need helper-enforced access control; rolling that into single-user simultaneously fixes the gap.

2. **A genuinely new use case falls out naturally.** With single-user and multi-member sharing one substrate plus a `dr_fanout` axis, "team repo with fleet-wide DR" — currently inexpressible — becomes free.

Self-fleet (ADR-014) provides the per-user identity that lets single-user collapse into multi-member as the trivial case `members={my-fleet}`.

## Decision

### One substrate, two orthogonal axes

Every gitmirror project is identified by a **project ID** = `sha256(founder_fleet_pubkey)`. Project URL: `vigo://<project_id>/<handle>`. The handle is human-readable; cryptographic identity is the fingerprint. Name squatting is impossible by construction.

Project shape is described by two orthogonal axes:

| `members:` | `dr_fanout:` | Resulting use case |
|---|---|---|
| `{my-fleet}` | `fleet-wide` | Today's personal DR |
| `{my-fleet}` | `members-only` | Personal repo, only on my envoys |
| `{team1, team2, …}` | `members-only` | Multi-member collaboration |
| `{team1, team2, …}` | `fleet-wide` | Team repo + fleet-wide DR (new, previously inexpressible) |

`members:` is a list of fleet pubkeys (one per participating user-fleet). `dr_fanout` controls whether non-member envoys in the fleet receive replicas as backups.

### Identity & membership

Membership is a stack of capability tokens, anchored to the founder's fleet pubkey. `vigo swarm gitmirror multi init <handle>` requires self-fleet to be active (per ADR-014); the founder fleet pubkey signs the initial admin token. Admins (any fleet holding an admin-role token) can mint further tokens for other fleets.

When envoy X (which is part of fleet F) wants to push:
1. The agent on X presents a token signed by some admin-token chain back to founder fleet F0.
2. The receiving envoy verifies the signature chain against the project pubkey baked into the URL.
3. The receiving envoy checks the local revocation list for any envelope naming this token's id.
4. Both pass → push proceeds; either fails → reject.

Envoys-within-a-fleet trust is implicit: any envoy holding fleet F's wrapped key (via self-fleet activate) can sign on behalf of fleet F. Subsystems see the fleet, not its envoys.

### Revocation

Admin issues `Revoke(token_id, revoked_at)` envelope, signed by the issuing admin's token. Gossip via the existing swarm transport (mirroring `agent/src/swarm/cryptsync/leaves.rs`). Each envoy pins received revocations locally. 90-day retention before reap, matching cryptsync.

### Transport

Users use plain git: `git remote add origin vigo://<project_id>/<handle>` then `git push` and `git pull`. The `vigo` binary dispatches as `git-remote-vigo` when invoked under that argv[0] (multi-call binary, no separate executable).

The helper:
1. Runs as the calling user.
2. Connects to a `SO_PEERCRED`-gated Unix socket at `$XDG_RUNTIME_DIR/vigo/gitmirror.sock`.
3. Username from SO_PEERCRED; envoy fingerprint from agent's mTLS cert; fleet identity from the active self-fleet session helper.
4. Agent (running as root) verifies token chain, enforces ref policy, proxies to `git-receive-pack` / `git-upload-pack` against the local bare repo.

### Storage

**One layout for everything:** `/var/lib/vigo/swarm/gitmirror/projects/<project_id>.git`. Root-owned, **`0700 root:root`** (tightened from today's 0755) — the helper-enforced access path is the only way for non-root users to read the repo.

The legacy single-user layout `mirrors/<seeder>/<user>/<project>.git` is fully replaced. Per zero-users doctrine, no migration shim — the old code path is removed.

### Distribution scope

Determined by `dr_fanout`:
- `members-only` — bare repo lands only on envoys whose fleet is in `members:`.
- `fleet-wide` — bare repo lands on every (non-blacklisted) envoy in the fleet for DR purposes; non-member envoys hold the bytes but cannot read or push (helper rejects).

Non-member fleet-wide receivers store the bare repo with the same `0700 root:root` perms; the helper checks token presence before allowing fetch/push. There is no encryption-at-rest for non-members in v1 — they hold plaintext git objects but the helper gates access. Encryption for non-member receivers is a deferred enhancement (would couple to cryptsync's age recipient model).

### Conflict policy

Fast-forward only on every branch by default. `--force-with-lease` is accepted (atomic check-and-replace using the SHA the client thinks is current). Bare `--force` is rejected at the daemon's pre-receive hook before any objects land.

Concurrent same-branch pushes from different fleets: one wins, the other receives non-fast-forward error with `pull and rebase first`. Standard git semantics; acceptable for personal/small-team. **Not a forge replacement.**

### Fan-out

After `git-receive-pack` completes successfully, the daemon computes `git rev-parse --all` before/after. If any ref changed, fan-out a bundle to other listed members' envoys (and, if `dr_fanout=fleet-wide`, to non-blacklisted fleet receivers) using the existing swarm peer-port (1531) machinery.

If no refs changed (idempotent ingest), no fan-out fires. **Cycle prevention via this delta check** — works correctly across partitions, rejoins, and concurrent pushes.

### Server's role

Server is **not authoritative for membership.** It provides three thin services:

1. **Gitmirrormesh aggregator extension** surfaces `(project_id, member_fleet_count, last_rotation_ts)` per project. **No member roster on check-in** — too bloating for the hot path. Detailed rosters fetch on-demand via REST at push time (lazy-fetch decision from the prior scrutinize round).
2. **Cross-subnet address resolution** — existing mechanism, scope extended to multi-member envoys.
3. **Audit log entries** — join, leave, admin-rotation, revoke, project-init events flow through the existing audit hash chain.

A multi-member project can in principle operate without the server if members manually distribute envoy addresses. Server is convenience, not authority.

### CLI verbs

Single namespace: `vigo swarm gitmirror project *` (replaces both today's `multi *` ADR-013 sketch and the implicit single-user CLI):

- `vigo swarm gitmirror project init <handle> [--fanout fleet-wide|members-only]` — founder.
- `vigo swarm gitmirror project invite <project_id> --fleet <fp> --role <r>` — admin only.
- `vigo swarm gitmirror project join <project_id> <founder-host>` — joining envoy / fleet.
- `vigo swarm gitmirror project leave <project_id>` — self-leave; emits self-revoke envelope.
- `vigo swarm gitmirror project list` — fleet-wide via gitmirrormesh.
- `vigo swarm gitmirror project revoke <project_id> --token-id <id>` — admin only.
- `vigo swarm gitmirror project reanchor <project_id>` — surviving member, hard-fork ceremony if all admins are lost.
- `vigo swarm gitmirror project status <project_id>` — local state.

### Founder-loss recovery

Repo content is recoverable as long as one fleet holds it. If the founder fleet + all admin fleets are gone, the project enters **frozen membership**: existing token-holders work indefinitely; no new fleets can join, no revocations issue. Two paths: soldier on, or hard-fork via `reanchor` (creates a new project ID; old URL is dead; content imports wholesale; remotes manually updated). **No quorum recovery in v1.**

## Alternatives Considered

**Keep ADR-013's split design (single-user untouched, multi-member parallel).** Rejected after scrutiny: two parallel mechanisms for the same problem, plus the world-readable bare-repo gap in single-user mode survives forever. Unification is strictly better in a zero-users codebase.

**Per-envoy identity (no self-fleet) for multi-member.** Was the original ADR-013 model. Rejected: forces O(N²) per-pair declarations for personal multi-envoy use, makes "dan-on-laptop and dan-on-desktop are the same person" require explicit per-pair invites for every project. Self-fleet collapses this to O(N) (one wrapped key per envoy).

**Encryption-at-rest for non-member fleet-wide DR receivers.** Considered. Would require encrypting bundles to each member fleet's pubkey and storing ciphertext on non-member receivers. Real engineering surface (age-encrypted git objects, on-demand decrypt during recover). Deferred — v1 ships with helper-enforced access only; non-member receivers hold plaintext bytes that they can't access via the helper but could theoretically be read with root + manual git invocation. Acceptable for personal/small-team trust models.

**Single-user as a degenerate `members={}` case (no fleet at all).** Rejected: makes the trust model inconsistent (some projects authenticated, some not) and re-creates the "any local user can clone" gap. Unifying everything under self-fleet ensures one trust model fleet-wide.

## Consequences

**Files to modify (agent — Rust):**
- `agent/src/swarm/gitmirror/` — substantial rework. Token issuance/parse/verify (new module mirroring `cryptsync/leaves.rs`); the multi-call helper dispatch (new argv[0] check on `vigo` binary); receive-pack proxy with ref-policy enforcement (new); fan-out-on-push trigger (replaces today's working-tree-watcher trigger for projects); revocation list (new, gossip + persist + 90-day reap); SO_PEERCRED-gated Unix socket daemon.
- `agent/src/swarm/gitmirror/cli.rs` — replaced with `project {init,invite,join,leave,list,revoke,reanchor,status}` verbs. Today's `recover`, `source {add,remove,…}` verbs go away (replaced by project-level operations).
- `agent/src/swarm/gitmirror/sidecar.rs` — sidecar concept replaced. Project state lives at `~/.vigo-gitmirror/<project_id>/` (token, project_pubkey for TOFU, local config). The repo's working tree no longer carries a `.gitmirror.yaml`.
- `agent/src/swarm/gitmirror/receiver.rs` / `publisher.rs` — heavy rewrite. Single fan-out path keyed by project_id; no more per-seeder hierarchy.

**Files to modify (server — Go):**
- `server/swarm/gitmirrormesh/` — extend aggregator to surface project metadata; no member rosters in check-in.
- `server/api/swarm_gitmirror.go` — new REST endpoint for member-roster lookup at push time.
- Audit-log entries for project lifecycle events.

**Documentation:**
- `docs/concepts/gitmirror.md` — full rewrite. The unified model, the four use-case combinations, the recovery posture, the conflict semantics.
- `docs/reference/cli/agent-swarm-gitmirror.md` and `docs/reference/cli/swarm-gitmirror.md` — replace existing trees with project-namespaced verbs.

**Removed:**
- The old single-user CLI verbs (`source add|remove|pause|resume|show|validate|list`, `recover`).
- The `.gitmirror.yaml` sidecar format.
- The legacy `mirrors/<seeder>/<user>/<project>.git` storage layout.
- The cosmetic `caller_visible_mirrors` filter in `cli.rs:178-187` — replaced by real helper-enforced access.

**Operational:**
- Agent rebuild + cross-compile + redeploy required.
- Server rebuild required.
- Y-level version bump (this is a meaningful behavioral and storage change, even if the codebase has zero users).

**Behavioral consequences:**
- Existing gitmirror users (zero) would re-init projects under the new model. No migration shim.
- `git clone /var/lib/vigo/swarm/gitmirror/...` from arbitrary user accounts no longer works — perms tightened to 0700, helper-enforced. This closes the world-readable bare-repo gap from today's single-user mode.
- "Dan can recover his own repo on his other envoy" requires self-fleet to be active on both envoys. One-time setup per envoy, persists across all projects.
- "Team repo + fleet-wide DR" becomes expressible — set `members={team-fleets}, dr_fanout=fleet-wide`.

**Estimated weight:** ~80–200 KiB on the agent (combined token machinery + helper proxy + revocation + storage rework, partially offset by removal of sidecar/recover code paths). ~100–200 KiB on the server. No new dependencies.
