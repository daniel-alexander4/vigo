---
title: "ADR-025: stockpile-in-gitback — service-account puddle + ops-puddle ownership"
---

# ADR-025: stockpile-in-gitback — service-account puddle + ops-puddle ownership

**Status:** Approved — not yet implemented. Pre-Phase-0 design for the tungol redesign (see `/home/dan/vigo-plan-tungol-redesign-2026-05-16.md`).
**Date:** 2026-05-16

## Context

Today's stockpile distribution is hub-mediated: `Hub.SyncConfigToSteorras` (`server/tungol/hub.go`) tars `stacks/.live/` and pushes the archive to every steorra via the hub-internal `SyncConfig` gRPC RPC after `vigocli config publish`. The hub is the canonical config origin; steorras are read-only mirrors. This is one of the five operational properties of the hub + steorras shape that motivated the broader redesign — a failed hub blocks all publishes, partition tolerance is bolted on rather than structural, and there is no "publish from any peer" story.

Phase 0 of the tungol redesign is **rename + config-in-gitback**: move `stockpile/` into a gitback project so every vigosrv clones, pulls on update, and triggers a config reload — retiring `SyncConfigToSpokes` entirely. The mechanical rename is straightforward; the gitback piece bumps into a real design issue.

Gitback (ADR-015) is puddle-based; puddle (ADR-014) is per-human-user. The puddle primitive was designed for human operators: each user generates their own Ed25519 keypair, wraps it under a passphrase at `~/.vigo-puddle/identity.wrapped`, unlocks it interactively, and uses it as their swarm identity. Vigosrv is not a human user — it has no passphrase, no interactive unlock, and no `$HOME` in the usual sense. The puddle primitive needs a service-account extension before vigosrv can participate as a gitback member.

Two ancillary questions follow: **who owns the stockpile project?** (a single human-operator's puddle couples production config distribution to one person and orphans the stockpile on their decommission; per-vigosrv puddles multiply identity infrastructure without giving each vigosrv anything meaningful to own alone); and **how does vigosrv know when to pull?** (today's "publish triggers REST reload" model needs an analog for gitback-driven config updates).

ADR-024 (curator) flagged a related pattern in passing — its Slice 4 implementation notes mention an "auto-provisioned operator puddle on the server host (unlocked from / derived via the secrets master key, so it's available whenever the server runs)" as the recommended option for vigocli's curator-publish path. This ADR formalizes that pattern, generalizes it to a service-account puddle for the vigosrv runtime user, and adds the multi-vigosrv ops-puddle layer that lets several vigosrvs collectively own the stockpile.

## Decision

### Service-account puddle identity for the vigosrv runtime user

Each vigosrv generates an Ed25519 puddle keypair on first startup at `/srv/vigo/puddle/identity.wrapped`. The wrap is derived from the existing secrets master key (`<secrets-dir>/.master.key`, already required for the `local` secrets backend), not a human passphrase — vigosrv can unlock it autonomously at boot without operator intervention. File mode 0600, owner = vigosrv runtime user.

The derivation: `puddle_wrap_key = HKDF-SHA256(master_key, "vigo-puddle-wrap-v1", per-host-salt)`. The per-host salt (random 32 bytes, generated once, stored at `/srv/vigo/puddle/.salt`) ensures every vigosrv has a distinct wrap even if multiple machines hold the same master key. Loss of the master key = loss of the puddle identity = puddle rekey via ADR-018 mechanism.

The service-account puddle behaves identically to a human puddle in every other respect: it has a pubkey advertised in the swarm, it can found gitback projects, it can be a member of ops-puddles (below), it participates in ADR-022 friendly-name claims (typically claiming `vigosrv-<hostname>` as its friendly name). It cannot, however, run the interactive `vigo swarm puddle pair` / `join` flow against another human's session — the helper daemon (`/var/run/vigo/puddle.sock`) is SO_PEERCRED-gated, so the service-account puddle is only reachable by code running as the vigosrv runtime user.

### Ops-puddle — collective ownership of the stockpile

A dedicated puddle, the **ops-puddle**, owns the stockpile gitback project. Founder is the first vigosrv set up; subsequent vigosrvs join via the standard puddle-pair flow (vigocli verb wrappers below). The ops-puddle's friendly name is operator-chosen at founding (e.g., `alexander4-ops`), independent of any individual vigosrv's hostname or any human operator's puddle.

Vigosrv membership in the ops-puddle is what gates **stockpile-write authority** — only ops-puddle members can `git push` to the stockpile project. Other puddle members can be added explicitly (Dan's `alexander4` puddle, an MSP's central-admin puddle) to authorize human operators to push directly via their own `git` toolchain.

A human operator who wants to commit-and-push from their own laptop uses the standard gitback project membership flow (ADR-013/015) to join the ops-puddle. The familiar `git commit` + `git push gitback://alexander4-ops/stockpile` path follows.

The ops-puddle is **not** a single-vigosrv-only construct. A standalone deployment with one vigosrv is a degenerate case (ops-puddle is single-member, founded by the one vigosrv); it still uses the same machinery so a future second vigosrv can join without restructuring. This keeps the standalone / tungol code paths identical.

### Stockpile gitback project

A single gitback project, founded by the ops-puddle, hosts `stockpile/`. Layout:

```
gitback://alexander4-ops/stockpile/
  modules/
  usercrates/
  templates/
  tasks/
  workflows/
  envoys.vgo
  roles.vgo
  common.vgo
  environments.vgo
  waivers.vgo
  matchfiles/
```

The same directory tree currently at `/srv/vigo/stacks/stockpile/` — git-tracked, history-preserved per commit. The `dr_scope: swarm-wide` (ADR-015 default for puddle-wide adoption per ADR-023) ensures every vigosrv that joins the ops-puddle gets a copy of the bytes; `recipients: ["*"]` (the gitback default) is irrelevant since the project is wholly internal to the ops-puddle.

`stacks/.live/` continues to exist as today — it's the operator-visible published-and-validated copy, distinct from `stockpile/` (the editable source). The change: `vigocli config publish` syncs from `stockpile/` (the gitback working tree) into `.live/`, then commits the post-validation state to the gitback project and pushes. Other vigosrvs pull, validate locally, and update their own `.live/`.

### Gitback-event-driven watch loop

A new background goroutine in vigosrv (`server/tungol/stockpile_watcher.go` or similar) subscribes to the existing gitback agent-side update notifications for the stockpile project. The agent's gitback module already emits an event when a project's HEAD advances after a `pull` (this is the same trigger that drives the post-pull reactor for other gitback projects); vigosrv subscribes via the existing helper-daemon notification socket.

On event: the watcher pulls the project (the agent's helper daemon handles the actual git operation), runs the existing config-validation path, and on success triggers the same reload code path that today fires after `vigocli config publish`. Failed validation logs an error and leaves the previous config active — same shape as today's publish-side validation failure.

No new RPC, no new notification primitive. Reuses gitback's existing pubsub infrastructure end-to-end.

### vigocli config publish — behavioral switch

`vigocli config publish` keeps its operator-facing semantics ("validate, install, propagate") but the propagate step splits on deployment shape:

- **Standalone mode** (no tungol, no ops-puddle): write to local `stacks/.live/` and SIGHUP — today's behavior, unchanged.
- **Tungol mode** (Phase 1+): validate, `git commit -m "publish: <description>"` to the local `stockpile/` working tree, `git push gitback://alexander4-ops/stockpile`. The watcher on every vigosrv (including this one) sees the new HEAD, pulls, validates, installs to `.live/`, SIGHUPs. The originating vigosrv's `publish` command waits for its own watcher to confirm the new commit is live before returning, so the operator gets the same "publish complete" feedback as today.

The originating vigosrv has the bytes locally already (from the working tree); the watcher is what closes the loop. Other vigosrvs converge on the next pull tick.

## Consequences / tradeoffs

- **Master key compromise = ops-puddle compromise.** The vigosrv puddle is wrapped under the master key, so anyone who can read `<secrets-dir>/.master.key` and the wrapped puddle file can derive the puddle's private key. This is the same threat boundary as the secrets backend itself — if an attacker has the master key, they already have plaintext access to every operator-stored secret. Stipulating that the puddle is also in their reach is consistent. Defense relies on filesystem permissions + the master key's existing protection model.
- **Stockpile history becomes git history.** Every `vigocli config publish` is a commit; operators can `git log gitback://alexander4-ops/stockpile` to see who published what, when, with the publish description as the commit message. The hash-chained audit log still owns the authoritative "what happened" record (publishes are audit-logged today), but the git history is a useful operator-facing secondary view.
- **Phase 0 of the tungol redesign is gated on this ADR's implementation.** Phase 0 cannot rename + ship config-in-gitback without the service-account puddle infrastructure in place. The plan's Phase 0 effort estimate (~2 weeks) bakes in this ADR's implementation effort.
- **Multi-vigosrv setup adds ops-puddle-pair friction.** Today, a new vigosrv just enrolls envoys. After this ADR ships, a new vigosrv must also join the ops-puddle (via the standard puddle-pair flow) before it can pull the stockpile and operate. This is intentional friction at the right layer — the new vigosrv is requesting access to fleet configuration, which IS a trust event.
- **No backwards-compatibility bridge between gitback-stockpile and `Hub.SyncConfigToSpokes`.** Phase 0 retires `SyncConfigToSpokes` entirely; deployments either run pre-Phase-0 code (hub-spoke with `SyncConfig`) or post-Phase-0 code (gitback-stockpile). Mixed-mode is explicitly unsupported (matches the broader tungol-redesign hybrid-refusal stance in Security invariant #8 of the plan).
- **Standalone deployments still go through gitback** (single-member ops-puddle). This is a small overhead for a one-vigosrv lab — a local-only gitback project — but keeps standalone and tungol code paths identical, so the standalone case is exercised by every developer's local environment and doesn't drift into untested territory.

## Implementation notes

Open questions to settle during implementation:

1. **Per-host salt placement.** `/srv/vigo/puddle/.salt` is the natural location. Should the salt itself be in the secrets backend (so a snapshot of `/srv/vigo` minus the master key still can't derive the puddle), or filesystem-stored under the same protection as the wrapped puddle? Lean filesystem-stored — putting the salt in the secrets backend buys little and complicates startup ordering.
2. **Ops-puddle name conflict.** What happens if two operators install vigosrv with the same ops-puddle friendly name (`alexander4-ops` collision) and the deployments later interact via the swarm? ADR-022 says friendly names are ambiguous-on-collision, no auto-tiebreak; the hex pid is the proof. Same mechanism applies here. Operators see the conflict in the UI and pick distinct names.
3. **Stockpile project on a fresh deployment.** First vigosrv setup creates the ops-puddle AND founds the stockpile gitback project AND seeds it with whatever is currently at `/srv/vigo/stacks/stockpile/`. The seed commit message: `initial: import existing stockpile from /srv/vigo/stacks/stockpile/`. If the existing stockpile is empty, the project is founded with no initial files.
4. **What if vigosrv can't reach the ops-puddle quorum on startup?** A new vigosrv joining the ops-puddle needs at least one existing member online to complete the pair flow. If all peers are offline, the new vigosrv waits (with a clear log message) for a peer to come online. The standalone case is degenerate (single member, always online — itself).
5. **Two CLI front-ends, one backend** (echoing ADR-024's Slice 4 note). `vigocli config publish` (operator, root, drives the vigosrv puddle) and `vigo swarm gitback push` (human user, drives their own puddle) are both thin clients of the gitback helper daemon. The duplication is shallow; the publish-vs-direct-push paths converge on the same gitback project.

### v1 slices

- **Slice 0** — this ADR + `_index.md`.
- **Slice 1** — service-account puddle infrastructure: HKDF derivation from master key, per-host salt generation, wrapped-identity persistence at `/srv/vigo/puddle/identity.wrapped`, autostart unlock at vigosrv boot, helper-daemon SO_PEERCRED gating for the vigosrv runtime user. Unit tests cover key generation, wrap/unwrap round-trip, and master-key-rotation scenarios.
- **Slice 2** — ops-puddle founding + joining: `vigocli tungol ops-puddle init <name>` (founds, first-vigosrv-only), `vigocli tungol ops-puddle pair` (mints a 5-minute code for an existing member to authorize a new vigosrv), `vigocli tungol ops-puddle join --from <peer> --code <c>` (consumes a code on a fresh vigosrv). Standard ADR-014 puddle-pair flow, just wrapped for the operator-facing terminology.
- **Slice 3 (SHIPPED 0.65.8)** — stockpile gitback project provisioning + initial seed commit. `vigocli tungol stockpile init` on the founding vigosrv creates `gitback://<ops-puddle-name>/stockpile` via the Rust helper's new `FOUND_OPS_PROJECT` verb, auto-imports the current `/srv/vigo/stacks/stockpile/` as the first commit (temp work tree → `git init` + `git add .` + `git commit` + `git push file://<bare-repo>`), and starts the in-vigosrv watcher (`server/stockpilewatch/`) that calls `configStore.Reload()` on every ref-dir change (inotify, 250ms quiesce-debounce). `agent/src/swarm/gitback/dr_scope.rs` refuses bundle ingest for any project not locally tracked under any puddle root — the v1 members-only enforcement. **Implementation deviations from the original ADR text**: (a) watcher uses inotify directly rather than a "helper-daemon update events" pubsub (the pubsub doesn't exist yet, inotify is simpler and equivalent in coverage); (b) ops-puddle gitback state lives at `/srv/vigo/ops-puddle/.vigo-gitback/<project-id>/` (the per-user gitback convention reused) rather than a separate `/srv/vigo/ops-puddle/gitback/<project-id>/` — keeps the per-user path helpers untouched, smaller blast radius for the refactor; (c) `mint_founder_project` was parameterized to take a `Signer` closure instead of forking into a separate ops-puddle path. Joining-side flow (`vigocli tungol stockpile join`) + cross-vigosrv distribution remain for Slice 4.
- **Slice 4 (SHIPPED 0.65.9)** — `vigocli config publish` behavioral switch + joining-side flow + bare→.live pull in the watcher + `Hub.SyncConfigToSteorras` retirement, all in one commit. `vigocli config publish` detects tungol mode (`/api/v1/tungol/status`) and switches to a gitback-driven push: clone stockpile bare → temp work tree, mirror `/srv/vigo/stacks/stockpile/` into work tree (delete-aware via `git add -A`), commit, push via `file://`. Operator-side confirmation is via polling the new `GET /api/v1/config/version` until it increments (30s timeout). `vigocli tungol stockpile join` creates local project state on joining vigosrvs via the same `FOUND_OPS_PROJECT` helper verb (deterministic `project_id` means every member computes the same id; the gitback catch-up reactor pulls bytes from any reachable member afterwards). The stockpile watcher in `server/stockpilewatch/` now runs `git --git-dir=<bare> archive HEAD | tar -x -C <tmp>` and syncs the temp dir into `.live/` (delete-aware via `pruneMissing`) before calling `configStore.Reload()`. **Retired in the same commit**: `Hub.SyncConfigToSteorras` + `createConfigArchive` in `server/tungol/hub.go`, the `SyncConfig` gRPC RPC + handler + `extractConfigArchive` in `server/grpc/admin.go`, the `SyncConfigRequest`/`SyncConfigResponse` proto messages + the `SyncConfig` RPC declaration in `protobuf/vigo.proto`, the `configSyncer` interface + `hub` field on `ConfigHandler`. `NewConfigHandler`'s `hub` parameter dropped. No transition window — tungol-mode publishes go through gitback exclusively.
- **Slice 5** — docs + acceptance + version bump (`docs/howto/publish-config.md` update, `docs/concepts/config-distribution.md` new, end-to-end test covering founder-publish → joiner-pull → reload on a multi-vigosrv harness).

After Slice 5 ships, Phase 0 of the tungol redesign can land (the rename plus the now-already-implemented gitback piece).
