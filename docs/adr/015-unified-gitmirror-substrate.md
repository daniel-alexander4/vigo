---
title: "ADR-015: Unified gitmirror substrate"
---

# ADR-015: Unified gitmirror substrate

**Status:** In progress. Outer-arc steps 1–3 shipped (multi-call dispatch 0.34.9; capability-token primitives 0.34.10; per-user state + CLI verb tree 0.35.0). Outer-arc step 4 is itself a 10-substep arc; first slice (helper daemon, `git-remote-vigo` body, revoke / leave envelopes + sentinel reactor) shipped 0.35.1; second slice (push-driven fan-out + receive-side `POST /swarm/gitmirror/push/<project_id_hex>` endpoint) shipped 0.35.2; third slice (LAN auto-discovery via `GITMIRRORPROJECT:` announce lines + project-peer index) shipped 0.35.3; fourth slice (cross-envoy envelope gossip with digest-based catch-up) shipped 0.35.4 — all on 2026-04-30. Remaining step-4 substeps — server-side mesh rewrite, REST member-roster endpoint, trait re-emit, `/gitmirror` UI rewrite, `reanchor` byte migration — pending. Supersedes ADR-013.
**Date:** 2026-04-29
**Updated 2026-04-30:** Helper-daemon (`/var/run/vigo/gitmirror.sock`, SO_PEERCRED-gated) + `git-remote-vigo` body shipped using the `connect`-capability transparent-tunnel pattern (no fetch / push state machine — git's smart protocol passes through unchanged). Revoke / leave envelopes (`vigo-gitmirror-revoke-v1` / `vigo-gitmirror-leave-v1` domain prefixes) signed by the user's self-fleet session helper at CLI time, persisted by a root-side reactor that drains `*.pending` sentinels into `/var/lib/vigo/swarm/gitmirror/envelopes/<project_id>/`. 90-day reap. Push-driven fan-out + receive-side `POST /swarm/gitmirror/push/<project_id_hex>` endpoint shipped same day: after a successful local `git-receive-pack`, the helper daemon snapshots ref state pre/post; on delta, builds a `git bundle --all` and POSTs it to every known peer for the project over swarm mTLS (port 1531). The receive-side handler verifies the chain header against the URL's project_id, runs `git bundle verify`, ingests via `git fetch <bundle>`, and returns `{accepted, refs_changed}` — the `refs_changed=false` response is the cycle-prevention signal for any future receive-side re-fan-out (not implemented in this slice; pushes propagate one hop). LAN auto-discovery shipped via a `GITMIRRORPROJECT:<host>:<pid_hex>` announce-line extension and a `ProjectPeers` global index that the helper daemon's fan-out hook consults alongside `project.json::cross_subnet_members`. Envelope gossip shipped with four endpoints (`POST /swarm/gitmirror/envelopes/<pid>/{revokes,leaves}` for inbound envelopes, `GET /swarm/gitmirror/envelopes/<pid>/{digest,all}` for digest-based catch-up): after `save_revoke` / `save_leave`, the reactor fans out the signed envelope to every known peer; a 5-minute reconciliation tick GETs each peer's digest and pulls `/all` on mismatch, idempotent-merging missing records.
**Updated 2026-04-29:** Access-control story reframed — `0700 root:root` perms + the SO_PEERCRED helper exist for **(a) access control for non-root local users** and **(b) identity binding for multi-member push**, not for bytes-on-disk confidentiality. Root retains direct filesystem access by design (matches Vigo's overall posture). Encryption-at-rest for non-member fleet-wide receivers dropped from the roadmap entirely (gitmirror is for *availability*; use cryptsync for *confidentiality*). Multi-call binary dispatch (`git-remote-vigo`) sequenced as the first commit of the implementation arc. `reanchor` gains an interactive-confirmation requirement at run time. Self-fleet verb references updated to the renamed surface from ADR-014's amendment (`unlock`/`join` instead of `activate`).

## Context

ADR-013 designed multi-member gitmirror as a separate subsystem layered alongside today's single-user personal-DR gitmirror, with capability tokens, self-certifying project IDs, and a parallel storage layout (`mirrors/multi/<project_id>.git`). Single-user gitmirror was left untouched. Under scrutiny that left two parallel mechanisms doing fundamentally the same thing (P2P git substrate with identity, transport, storage) — which the zero-users doctrine in `CLAUDE.md` says to avoid in favor of unifying behind a single primitive.

Two further observations made unification the correct call:

1. **Single-user gitmirror's permission posture is an access-control gap.** Bare repos at `mirrors/<seeder>/<user>/<project>.git` are `0755 root:root`; any **non-root** user on any receiver can `git clone` them via plain git. The per-user CLI filter is cosmetic. Multi-member gitmirror was going to need helper-enforced access control for identity-bound push; rolling that into single-user simultaneously fixes the gap. (This is access control for non-root local users, not bytes-on-disk confidentiality — root retains direct filesystem access by design.)

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

Membership is a stack of capability tokens, anchored to the founder's fleet pubkey. `vigo swarm gitmirror project init <handle>` requires self-fleet to be unlocked (per ADR-014); the founder fleet pubkey signs the initial admin token. Admins (any fleet holding an admin-role token) can mint further tokens for other fleets.

When envoy X (which is part of fleet F) wants to push:
1. The agent on X presents a token signed by some admin-token chain back to founder fleet F0.
2. The receiving envoy verifies the signature chain against the project pubkey baked into the URL.
3. The receiving envoy checks the local revocation list for any envelope naming this token's id.
4. Both pass → push proceeds; either fails → reject.

Envoys-within-a-fleet trust is implicit: any envoy holding fleet F's wrapped key (via `self-fleet unlock` or post-`join`) can sign on behalf of fleet F. Subsystems see the fleet, not its envoys.

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

**One layout for everything:** `/var/lib/vigo/swarm/gitmirror/projects/<project_id>.git`. Root-owned, **`0700 root:root`** (tightened from today's 0755).

The 0700 perms exist to gate **non-root local users** — without the helper, any user on a receiver could `git clone` bare repos that aren't theirs. After the tightening, non-root users go through the SO_PEERCRED helper, which checks fleet membership against the project's `members:` list before allowing fetch/push. **Root retains direct filesystem access** by design, matching Vigo's overall posture (audit log, secrets master key, agent runtime — root is privileged on the box).

The legacy single-user layout `mirrors/<seeder>/<user>/<project>.git` is fully replaced. Per zero-users doctrine, no migration shim — the old code path is removed.

### Distribution scope

Determined by `dr_fanout`:
- `members-only` — bare repo lands only on envoys whose fleet is in `members:`.
- `fleet-wide` — bare repo lands on every (non-blacklisted) envoy in the fleet for DR purposes; non-member envoys hold the bytes but cannot read or push (helper rejects non-root callers; root can read but is trusted).

Non-member fleet-wide receivers store the bare repo with the same `0700 root:root` perms; the helper checks token presence before allowing non-root fetch/push.

**Bytes-on-disk confidentiality is explicitly not a goal for gitmirror.** `dr_fanout=fleet-wide` distributes plaintext git objects to every (non-blacklisted) fleet envoy, where root can read them directly. If you want confidentiality, use cryptsync — gitmirror is for *availability*, cryptsync is for *confidentiality*. The two subsystems are deliberately complementary; no encryption-at-rest layer is added to gitmirror in v1, v2, or any future version of the design.

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
- `vigo swarm gitmirror project reanchor <project_id>` — surviving member, hard-fork ceremony if all admins are lost. **Interactive confirmation required at run time:** the verb prints the consequences (old URL becomes permanently dead, all members must re-add the new remote, existing tokens are not portable) and refuses to proceed unless the user types the project handle to confirm intent. Same anti-footgun pattern as `git push --force-with-lease`.
- `vigo swarm gitmirror project status <project_id>` — local state.

### Founder-loss recovery

Repo content is recoverable as long as one fleet holds it. If the founder fleet + all admin fleets are gone, the project enters **frozen membership**: existing token-holders work indefinitely; no new fleets can join, no revocations issue. Two paths: soldier on, or hard-fork via `reanchor` (creates a new project ID; old URL is dead; content imports wholesale; remotes manually updated). **No quorum recovery in v1.**

## Alternatives Considered

**Keep ADR-013's split design (single-user untouched, multi-member parallel).** Rejected after scrutiny: two parallel mechanisms for the same problem, plus the world-readable bare-repo gap in single-user mode survives forever. Unification is strictly better in a zero-users codebase.

**Per-envoy identity (no self-fleet) for multi-member.** Was the original ADR-013 model. Rejected: forces O(N²) per-pair declarations for personal multi-envoy use, makes "dan-on-laptop and dan-on-desktop are the same person" require explicit per-pair invites for every project. Self-fleet collapses this to O(N) (one wrapped key per envoy).

**Encryption-at-rest for non-member fleet-wide DR receivers.** Considered and **rejected outright** (not just deferred). Would require encrypting bundles to each member fleet's pubkey and storing ciphertext on non-member receivers, with on-demand decrypt during recover. Real engineering surface, and the wrong abstraction layer: gitmirror is for *availability* (fleet-wide replicas, fast recovery, fast clones); cryptsync is for *confidentiality* (encryption-at-rest, age recipients, controlled access). Coupling encryption into gitmirror would blur the boundary between two subsystems whose value lies in being orthogonal. Users wanting confidential storage of git content should put `~/cryptsync/<repo>/` instead; gitmirror is plaintext by design.

**Single-user as a degenerate `members={}` case (no fleet at all).** Rejected: makes the trust model inconsistent (some projects authenticated, some not) and re-creates the "any local user can clone" gap. Unifying everything under self-fleet ensures one trust model fleet-wide.

## Consequences

### Implementation sequencing

**Multi-call binary dispatch lands as the first commit of the implementation arc.** The `vigo` agent binary becomes a multi-call binary that dispatches as `git-remote-vigo` when invoked under that argv[0]. This is a structural change to the agent's entry point and is too easy to bolt on poorly at the end of a large diff; landing it first gives every subsequent commit a working transport to test against.

Three failure modes to handle up front:

1. **Argv[0] dispatch in `agent/src/main.rs`** — first thing in `main()`, before any clap parsing. Strip directory prefix and `.exe` suffix. Match against an explicit table: `git-remote-vigo` → git remote helper mode; anything else → normal `vigo` CLI parsing.
2. **Install the symlink in package recipes.** Update `scripts/upgrade.sh`, `Dockerfile.server` (where applicable), and any deb/rpm packaging so install creates `/usr/local/bin/git-remote-vigo` → `/usr/local/sbin/vigo` (or wherever `vigo` lives on the platform). Document the symlink path in `docs/getting-started/`.
3. **Integration test exercising the dispatch.** A test that runs the binary with argv[0] set to `git-remote-vigo` (using `Command::new(...).arg0(...)` on Unix) and confirms it enters remote-helper mode. Catches regressions if anyone refactors `main()`.

Linux + macOS at launch per CLAUDE.md's Linux-first doctrine; Windows multi-call dispatch is a follow-up ticket against the rest of the Windows backlog.

### Files to modify (agent — Rust):
- `agent/src/main.rs` — argv[0] dispatch as the first thing in `main()` (see above).
- `agent/src/swarm/gitmirror/` — substantial rework. Token issuance/parse/verify (new module mirroring `cryptsync/leaves.rs`); the multi-call helper dispatch (new argv[0] check on `vigo` binary); receive-pack proxy with ref-policy enforcement (new); fan-out-on-push trigger (replaces today's working-tree-watcher trigger for projects); revocation list (new, gossip + persist + 90-day reap); SO_PEERCRED-gated Unix socket daemon.
- `agent/src/swarm/gitmirror/cli.rs` — replaced with `project {init,invite,join,leave,list,revoke,reanchor,status}` verbs. Today's `recover`, `source {add,remove,…}` verbs go away (replaced by project-level operations). `reanchor` gets an interactive confirmation prompt requiring the user to type the project handle.
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
- `git clone /var/lib/vigo/swarm/gitmirror/...` from non-root user accounts no longer works — perms tightened to 0700, helper-enforced. Root retains direct filesystem access by design. This closes the non-root-readable bare-repo gap from today's single-user mode.
- "Dan can recover his own repo on his other envoy" requires self-fleet to be unlocked on both envoys (per-envoy setup is `init` once on the first envoy and `join --from <host>` once per additional envoy, then `unlock` daily). Persists across all projects.
- "Team repo + fleet-wide DR" becomes expressible — set `members={team-fleets}, dr_fanout=fleet-wide`.

**Estimated weight:** ~80–200 KiB on the agent (combined token machinery + helper proxy + revocation + storage rework, partially offset by removal of sidecar/recover code paths). ~100–200 KiB on the server. No new dependencies.
