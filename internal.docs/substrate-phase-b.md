# Phase B — Unified leave / evict vocabulary (cairn + sigil)

*Design locked 2026-04-24 after the substrate audit (`substrate-audit.md`) and the Phase A ship (0.30.14). This plan covers the code; it supersedes the "Phase B sketch" section of `substrate-unified-plan.md`.*

## Context

The audit identified lifecycle-op asymmetry between cairn and sigil. Phase A filled the cairn source-authoring gap. Phase B fills the membership-lifecycle gap: self-unenroll (`leave`) and admin eviction (`evict`) on both substrates, with one vocabulary. Shipping them together as 0.30.15 keeps the verb names aligned across the fleet in a single upgrade.

## Locked design decisions (from Dan, 2026-04-24)

1. **cairn `leave` default scope: publisher-only.** `--purge` additionally runs receiver-side prune for this envoy as an admin-scope convenience.
2. **Keep `evict` as the admin verb name** on both substrates even though cairn's mechanics are just fleet-wide prune. Symmetric UX wins over implementation-mechanics names.
3. **`--pause` variant for cairn leave**: instead of deleting the user's sidecars, set `paused: true` on each. Useful for "going offline for a month, keep enrollment intact."
4. **Single version (0.30.15)** for agent + server + CLI. No split ship.
5. **Admin path uses existing TaskDispatch** (shell commands) — no new proto. Dispatched command for cairn is `vigo cairn prune --seeder <h> [--user <u>] --yes`. Dispatched command for sigil is a new hidden verb `vigo sigil _apply-evict --user <u> --pubkey <p>`.
6. **Cross-member eviction via P2P envelope stays forbidden.** Only self-signed LEAVE for sigil. Cairn has no cryptographic membership to evict through; admin path is the only evict path.

## Verb shape (final)

### Agent-side (`vigo <sub> <verb>`)

```
vigo cairn leave [--purge] [--pause] [--yes]
vigo sigil leave [--purge] [--keep-identity] [--yes]
```

- cairn `leave`: enumerate sidecars owned by invoking user under their `~/cairn/*/.cairn.yaml`. Confirm unless `--yes`. Either delete (default), or set `paused: true` (`--pause`), or delete AND prune receiver mirrors scoped to this user's publishers (`--purge`).
- sigil `leave`: build signed LEAVE envelope, gossip to peers, optionally scrub local identity (default: yes, unless `--keep-identity`). `--purge` additionally wipes `~/sigil/` ciphertext.

### Admin-side (`vigocli <sub> <verb>`)

```
vigocli cairn evict  --host <h> [--user <u>] [--yes]
vigocli sigil evict  --host <h> [--user <u>] [--all-users] [--yes]
vigocli cairn members
vigocli sigil members [--user <u>]
```

- cairn `evict`: fan TaskDispatch of `vigo cairn prune --seeder <h> [--user <u>] --yes` to every online envoy.
- sigil `evict`: fan TaskDispatch of `vigo sigil _apply-evict --user <u> --pubkey <p>` to enrolled envoys. `--all-users` looks up every user the target host is enrolled for and fires the eviction for each.
- `members`: read-only list from the existing `/api/v1/{cairn,sigil}/fleet` projections.

### Hidden agent verb

`vigo sigil _apply-evict --user <u> --pubkey <p>` — not surfaced in `--help`. Invoked by TaskDispatch. Calls shared `apply_leave(user, pubkey, ...)` that removes the pubkey from `peers.json`, regenerates `recipients.txt`, and drops `reencrypt.pending`.

## Implementation plan

### Step 1 — cairn `leave` + `prune` flag extensions (agent)

**Files:**
- `agent/src/cairn/source_cli.rs` — add `cmd_leave` dispatcher + body.
- `agent/src/cairn/cli.rs` — route `vigo cairn leave` to `source_cli::cmd_leave`; add `vigo cairn prune --seeder <h>` and `--user <u>` flag handling in the existing `cmd_prune`.
- `agent/src/cairn/source.rs` — if not already present, a function to list sidecars owned by a given user (reuse `scan_local` + filter by home dir).

**Behavior:**
- `vigo cairn leave`: print planned actions (N sidecars to remove/pause), prompt unless `--yes`, execute.
- `vigo cairn leave --pause`: same enumeration, but flip `paused: true` on each sidecar (reuse `source_cli::set_paused`).
- `vigo cairn leave --purge`: after deleting sidecars, also invoke receiver-side prune for mirrors whose seeder == this envoy hostname AND owner == invoking user. The receiver pruning logic already exists in `cmd_prune`; add a narrower filter path.

**Tests:** unit tests mirroring the Phase A pattern. Cover: delete path, pause path, purge path, no-sources no-op, confirmation flow.

### Step 2 — sigil `leaves.rs` + shared `apply_leave` (agent)

**Files (new):**
- `agent/src/sigil/leaves.rs` — `LeaveEnvelope`, `LeavesTable`, `build_envelope`, `verify_envelope`, `canonical_sign_bytes`, retention constants. Direct adaptation of `tombstones.rs`.

**Files (modified):**
- `agent/src/sigil/config.rs` — add `leaves_path`, `leave_pending_path`, `reencrypt_pending_path`.
- `agent/src/sigil/state.rs` — add `PeerTable::forget_by_pubkey`.
- `agent/src/sigil/mod.rs` — add `apply_leave(user, pubkey, hostname, time)` shared function.

**Verification rule:** the envelope's signature is verified against the `signing_pubkey` registered for `hostname_leaving` in the receiving envoy's `peers.json`. Signer must be the subject; no cross-member eviction via envelope.

**Persistence:** `/var/lib/vigo/sigil/<user>/leaves.json` (root-owned 0600), 90-day retention matching tombstones.

### Step 3 — sigil `vigo sigil leave` + propagation (agent)

**Files:**
- `agent/src/sigil/cli.rs` — add `cmd_leave` (self-leave only; hostname-arg form errors with a pointer to `vigocli sigil evict`).
- `agent/src/sigil/server.rs` — add `POST /sigil/leaves/<user>` (verify + apply + persist) and `GET /sigil/leaves/<user>` (serve table for digest catch-up).
- `agent/src/sigil/pusher.rs` — add `push_leave_one` fan-out to live peers.
- `agent/src/sigil/discovery.rs` + `state.rs` — add `leaves_digest` to announcements; on digest mismatch, pull `/sigil/leaves/<user>` the same way tombstones are caught up at `mod.rs:330–343`.

**Local-scrub default:** delete `~/.sigil/` identity; keep `~/sigil/` ciphertext (orphaned — no key locally). `--purge` also removes `~/sigil/`. `--keep-identity` suppresses local scrub.

### Step 4 — sigil `_apply-evict` hidden verb (agent)

**Files:**
- `agent/src/sigil/cli.rs` — add `_apply-evict` case routing to `apply_leave`. Not in `--help`.

### Step 5 — server admin endpoints (server)

**Files (new):**
- `server/api/sigil.go` — `POST /api/v1/sigil/evict` handler. Input: `{host, user?, all_users?}`. Body: look up target's age pubkey(s) from sigil fleet projection, compute enrolled-envoy set, dispatch `vigo sigil _apply-evict --user <u> --pubkey <p>` via existing task-dispatch. Response: summary.
- `server/api/cairn.go` — `POST /api/v1/cairn/evict` handler. Input: `{host, user?}`. Dispatch `vigo cairn prune --seeder <host> [--user <u>] --yes` to every online envoy.

**Files (modified):**
- `server/api/mesh.go` or wherever `/api/v1/{cairn,sigil}/fleet` lives — if `members` needs a new endpoint shape, add it; otherwise CLI reuses `fleet` output.

### Step 6 — `vigocli` verbs (CLI)

**Files (modified):**
- `cli/sigil.go` — add `evict` and `members` subcommands.
- `cli/cairn.go` — add `evict` and `members` subcommands.

Use Catppuccin Mocha palette via `cli/format.go`. Members output matches the audit-doc example format (host, pubkey prefix, last seen, status).

### Step 7 — docs + tests

- `docs/reference/cli/agent-cairn.md` — document `leave`.
- `docs/reference/cli/agent-sigil.md` — document `leave` (and note `_apply-evict` as internal-only).
- `docs/reference/cli/cairn.md` — document `vigocli cairn evict` and `members`.
- `docs/reference/cli/sigil.md` — document `vigocli sigil evict` and `members`.
- `docs/engineering/subsystems/cairn.md` — "Leaving cairn" section.
- `docs/engineering/subsystems/sigil.md` — "Leaving sigil" section (both paths: self-leave envelope + admin evict).
- Unit tests at each layer.
- Integration test plan for e2e (home-lab verification).

### Step 8 — version bump + commit

- `VERSION` + `agent/Cargo.toml` → 0.30.15.
- Single commit with evening timestamp. Agent + server + CLI all rebuild.

## Out of scope

- **Retire-hook integration.** Deferred to Phase D — `vigocli envoys retire` does not exist; adding it is independent prereq work.
- **Web UI buttons for leave/evict.** CLI first; UI follows in a separate uiux pass once verb vocabulary has settled.
- **Windows parity.** Linux + macOS per Linux-first doctrine.
- **cairn cryptographic membership.** Cairn doesn't have recipient keys; admin evict is a task-dispatched prune, not a signed envelope. Intentional asymmetry with sigil.

## Verification (home-lab)

1. cairn leave on annlap as user `dan`: expect all `~/cairn/*/.cairn.yaml` removed; reactor on next tick stops publishing those labels; other envoys' receiver mirrors persist until their own prune.
2. cairn leave `--pause`: expect `paused: true` on all sidecars; receivers stop getting new bundles but keep their mirrors.
3. cairn leave `--purge`: expect sidecars gone AND this envoy's receiver mirrors of the user's projects cleared.
4. `vigocli cairn evict --host girlslaptop`: expect task dispatched to danlap + annlap + plex; each runs prune; mirrors from girlslaptop vanish fleet-wide.
5. sigil leave on annlap as `dan`: expect LEAVE envelope signed + multicast + unicast to cross-subnet peers; peers drop pubkey within one announce cycle; `reencrypt.pending` drops on each peer; user unlock drains the queue.
6. sigil leave `--purge`: also removes `~/sigil/` ciphertext.
7. `vigocli sigil evict --user dan --host girlslaptop`: task dispatched; envoys apply locally; `reencrypt.pending` drops.
8. Offline catch-up: take a host down, run an evict, bring it up → task fires on reconnect.
9. sigil leave envelope offline catch-up: take plex down, annlap does `vigo sigil leave`, bring plex back up → plex pulls `/sigil/leaves/<user>` on digest mismatch and applies.

## Acceptance criteria

- [ ] All four verbs work: `vigo cairn leave`, `vigo sigil leave`, `vigocli cairn evict`, `vigocli sigil evict`.
- [ ] `members` verbs list enrolled hosts for both substrates.
- [ ] Offline envoys catch up via TaskDispatch retry (admin path) and digest gossip (sigil P2P path).
- [ ] No cross-member eviction via envelope (sigil verification rule enforced).
- [ ] Docs updated for all new verbs.
- [ ] Unit tests for each verb + server endpoint.
- [ ] Single commit at 0.30.15.
