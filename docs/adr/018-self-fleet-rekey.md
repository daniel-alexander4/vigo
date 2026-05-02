---
title: "ADR-018: Self-fleet rekey"
---

# ADR-018: Self-fleet rekey

**Status:** Complete. Shipped across two slices: rekey verbs + orchestration (0.35.14) and status surface + ADR-018 + docs (0.35.15). Prereqs landed at 0.35.12 (cryptsync `identity_rewrap` primitive) and 0.35.13 (claim-code refactor). The originally-deferred cryptsync recipient-set widening shipped as a follow-up at 0.35.17 — the founder's `live_pubkeys_for_fleets` filter now accepts both the current fleet pubkey and every unexpired retired fleet pubkey, so mid-window writes on the founder reach unrekeyed peers. Gitmirror founder-rekey was picked up and shipped under **[ADR-019](019-gitmirror-founder-rekey.md)** (anchor-preserving via per-project delegation chains) across five slices at 0.35.18–0.35.22 — see ADR-019 for the design.
**Date:** 2026-04-30

## Context

ADR-017 established that fleet-level revoke is a placebo: the only correct response to a compromised self-fleet keypair is to retire it (re-init the fleet) and rotate every other credential the lost device touched. That decision left an obvious operational pain — naïve re-init blows away surviving envoys' cryptsync state, all gitmirror project_ids the user founded, and offers no path back for envoys that were offline during the rotation. ADR-014 deliberately scoped re-init to "delete `~/.vigo-fleet/` and start over" because the v1 model assumed loss-of-passphrase was the only rotation event; in practice users will rotate after a stolen-laptop scare, after a routine hygiene window, or when a founder envoy is being decommissioned cleanly, and "blow it all away" is a bigger hammer than the situation needs.

This ADR introduces a rotation flow that preserves cryptsync state on surviving envoys, records a 24h graceful retirement window, and surfaces UX hints for envoys that missed the rotation. It does **not** address the gitmirror project_id rotation problem (project_id = sha256(founder_fleet_pubkey)) — that is its own design exercise involving capability-token chain anchoring across the rotation, and is queued for a future ADR.

## Decision

### Two new verbs

```
vigo swarm self-fleet rekey-pair [--close] [--window-secs <N>]
vigo swarm self-fleet rekey --from <host>
```

`rekey-pair` runs on any current fleet member that the operator trusts as the rotation source (typically the same envoy that ran `init`, but any member with the unwrapped key works). It is the rotation event itself: re-prompts for the fleet passphrase, verifies it unwraps the existing key, generates a new keypair under the same passphrase + a fresh salt, atomic-renames `~/.vigo-fleet/identity.{wrapped,salt,pub}` to `.pre-rekey-<unix_ts>` backups (30-day retention via the same GC that catches cryptsync's rewrap backups), runs the cryptsync identity-rewrap primitive (ADR-016 slice 4, extracted in 0.35.12), clears local cryptsync TOFU pins for the retired fleet pubkey, regenerates `recipients.txt`, restarts the self-fleet session helper with the new key, and records a `RetiredFleetEntry` in `~/.vigo-fleet/retired_fleets.json` with `expires_at = retired_at + 24h`. It then issues a 5-minute single-use claim code via `~/.vigo-fleet/rekey.pending` so surviving envoys can claim the new wrapped blob.

Re-running `rekey-pair` inside the active 24h window only reissues a fresh claim code — the keypair and on-disk state are untouched. This lets the founder walk multiple surviving envoys through migration without re-rotating each time. `--close` pulls the active window's `expires_at` forward to `now`, useful when the operator confirms all surviving envoys have migrated and wants to retire the rekey session early. `--window-secs <N>` overrides the 24h default for unusual cadences (e.g., a one-off hygiene rotation where the operator wants only a 1h window).

`rekey --from <host>` runs on each surviving envoy. It requires the OLD fleet passphrase to unwrap the local key (cryptsync rewrap needs the old key in process scope), POSTs the claim code to `https://<host>:1531/swarm/self-fleet/rekey-claim/<user>` over swarm mTLS, unwraps the returned blob with the same passphrase, runs the same atomic swap + cryptsync sync as the founder, locks the local session helper. The operator runs `unlock` afterwards in their own (non-sudo) shell to start a fresh helper under the new key.

### Why these specific shapes

**Re-prompt for the fleet passphrase at `rekey-pair` issuance.** Even with the session helper already unlocked, `rekey-pair` re-prompts. This is one extra prompt at rotation time and defends against a brief unattended-shell threat: an attacker who briefly has access to the founder's already-unlocked shell still needs the passphrase to authorize a rotation. Skipping the prompt is a cheap-feeling shortcut that creates an unnecessary attack window, and the operator is already on the founder envoy with a passphrase prompt right there for `unlock`.

**Graceful retirement, not decisive cutover.** The 24h window means the founder accepts new rekey-claim requests for the rotated-out identity as long as the window is open. This is operational room for the operator to walk envoy-by-envoy without racing a hard cutover. The trust boundary against attackers is the 5-minute claim code, not the window — a stolen pairing code is the actual compromise vector, and the operator dictating an 8-character code over an out-of-band channel (voice / SMS / in-person) is the same security posture as the original `pair` flow.

**No chain-of-trust shortcut from the old key.** A "rekey envelope signed by the OLD key" sounds attractive — surviving envoys would just verify the envelope and accept the new key without operator intervention. Rejected: the threat model includes a compromised old key. An attacker holding the same OLD key can produce a byte-identical signature and direct surviving envoys to the attacker's fleet pubkey instead. Each surviving envoy has to do fresh out-of-band authentication (the pairing code from a trusted founder), exactly like the initial join. This is friction by design, not a UX gap.

**Same passphrase across the rotation.** The new wrapped blob is encrypted under the same passphrase as the old. The operator types one passphrase per envoy, used twice (unwrap-old-locally, unwrap-new-from-claim). Forcing the operator to type a new passphrase would make rotation feel like re-init, undoing the UX win. Operators who want to rotate the passphrase can run `rotate-passphrase` separately afterwards (per-envoy, no fleet-wide ceremony).

**Salt is reused across cryptsync rewrap; refreshed across self-fleet rotation.** Cryptsync's `identity_rewrap` keeps the existing `identity.salt` because the wrap key changes via the password material (different deterministic Ed25519 signature), and a single-file atomic rename owns the migration. Self-fleet's rotation generates a fresh salt because the keypair is fully new and there's no migration constraint demanding a stable salt — fresh salt is the conservative default.

**Local retirement log on every rekeying envoy.** `~/.vigo-fleet/retired_fleets.json` is local-only; no envelope, no gossip. The founder's entry powers `rekey-pair` reissue logic + `--close`; each surviving envoy records its own entry for `status` visibility ("we rotated to ed25519:new on date X"). Cross-envoy gossip of rotation history is intentionally out of scope — it would require a trust-rooted signing primitive that doesn't exist in v1, and the local log already gives operators the audit trail they need.

**Offline-envoy hint in `status`.** When `self-fleet status` runs on an envoy whose local fleet pubkey diverges from peers' announces (visible via the cryptsync peer table), it surfaces "peer X is on a different fleet pubkey; if you rekeyed there, run `rekey --from X`." Read-only inspection — no automatic key-update is attempted. This addresses the offline-during-rekey scenario without requiring a trusted retirement-announcement primitive (which would have the same chain-of-trust shortcut problem the rekey design rejects above).

### What stays out of scope

- **Gitmirror founder rotation.** Originally framed as "every gitmirror project the user founded has a dead URL after a rekey." Picked up under [ADR-019](019-gitmirror-founder-rekey.md) and shipped 0.35.18–0.35.22 — see that ADR for the anchor-preserving design (per-project delegation chains, multi-fleet `re-invite-all` + `rejoin`, `re-found` escape hatch).
- **Cryptsync recipient resolution during the rotation rollout (originally deferred; shipped at 0.35.17).** Between founder rotation and surviving-envoy rotation, the founder's filter previously excluded peers whose stored `fleet_pubkey` was the OLD value, so files written on the founder during the gap encrypted to a smaller recipient set. The follow-up renamed `live_pubkeys_for_fleet` to `live_pubkeys_for_fleets` (taking a slice of accepted fleet pubkeys) and threaded `~/.vigo-fleet/retired_fleets.json` into the call site in `regenerate_recipients`. The founder now accepts both its current fleet pubkey and every unexpired retired fleet pubkey, so mid-window writes on the founder reach unrekeyed peers. The inverse direction (writes from a not-yet-rekeyed envoy reaching the rekeyed founder) still drops because the unrekeyed envoy's TOFU pin rejects the founder's NEW announce until that envoy itself rekeys; that asymmetry is the honest residual cost of the rollout.
- **Multi-founder rekey.** "What if two founders both have the old fleet key and both run `rekey-pair`?" — undefined; both produce different new keypairs; the user would have to pick one and re-rekey the other envoys to reconcile. The flat-trust design of self-fleet (every member is equal) makes this a self-imposed problem the operator avoids by convention.

## Alternatives Considered

**Re-init from scratch.** What ADR-014 originally implied. Rejected because the blast radius is much larger than rekey: surviving envoys' fleet-mode cryptsync `identity.age` becomes undecryptable (wrap key was fleet-key-derived; new fleet key produces a different derivation), every gitmirror project the user founded changes identity, offline envoys are orphaned without any migration path. Rekey solves the cryptsync-state preservation; gitmirror remains orphaned but that's a constant across both approaches.

**In-place keypair rotation that doesn't touch cryptsync.** Generate new fleet key, write new identity files, leave cryptsync's `identity.age` unchanged. Rejected: fleet-mode cryptsync derives its wrap key from the fleet keypair; rotating without rewrapping means the next `cryptsync unlock` on the rekeyed envoy can't unwrap its own identity. The operator would have to manually re-init cryptsync, blowing away the per-envoy age key. The whole point of preserving cryptsync state is gone.

**Frictionless rekey via old-key-signed handoff envelope.** Founder's `rekey-pair` produces an envelope signed by the OLD fleet key naming the NEW fleet pubkey; surviving envoys auto-accept on first sighting. Rejected for the threat-model reason above: an attacker holding the old key signs the same envelope and points victims at their own pubkey. The pairing-code shape is the only honest way to gate a rotation against a key-compromise attacker.

**Deferred TOFU clear (don't touch cryptsync state during rekey).** Skip the `clear_pins_for_fleet` + `regenerate_recipients_now` calls; let the operator run `cryptsync forget <host>` per peer when TOFU rejections fire post-rotation. Rejected: every surviving envoy would have to manually clear pins, and the operator probably wouldn't realize that's what's needed until a missing-file complaint surfaces. Doing it at rotation time is a clear cost localized to one verb.

## Consequences

**Code changes:**
- `agent/src/swarm/self_fleet/codes.rs` — shared claim-code primitives (extracted in 0.35.13 ahead of this ADR).
- `agent/src/swarm/self_fleet/rekey.rs` — `RetiredFleetEntry` persistence, active-session predicate, `apply_post_rotation_cryptsync_sync`, `handle_rekey_claim` HTTP handler.
- `agent/src/swarm/self_fleet/cli.rs` — `cmd_rekey_pair` and `cmd_rekey` verbs; status surface enrichment for retired-fleets visibility and offline-envoy hint.
- `agent/src/swarm/self_fleet/config.rs` — `rekey_pending_path` + `retired_fleets_path`.
- `agent/src/swarm/server.rs` — dispatch `REKEY_CLAIM_PREFIX` to the rekey handler.
- `agent/src/swarm/cryptsync/identity_rewrap.rs` — atomic re-wrap primitive (shipped in 0.35.12 as the slice-1 foundation for this ADR).
- `agent/src/swarm/cryptsync/state.rs` — `clear_pins_for_fleet` for post-rotation TOFU re-pin; `live_pubkeys_for_fleets` accepts a slice of acceptable fleet pubkeys (0.35.17 follow-up — replaces single-fleet `live_pubkeys_for_fleet`, widening the founder's recipient filter during the rekey rollout).
- `agent/src/swarm/cryptsync/mod.rs` — `regenerate_recipients` reads `~/.vigo-fleet/retired_fleets.json` and includes every unexpired retired fleet pubkey in the accepted set (0.35.17 follow-up).

**Documentation:**
- This ADR.
- `docs/reference/cli/agent-swarm-self-fleet.md` — verb reference table extended with rekey-pair / rekey rows; a Rekey walkthrough section.
- `docs/adr/_index.md` — entry added.

**Operational:**
- Z-bumps for the implementation slices: 0.35.11 (ADR-017 docs), 0.35.12 (rewrap primitive), 0.35.13 (codes refactor), 0.35.14 (rekey verbs + orchestration), 0.35.15 for status / docs / ADR-018, 0.35.17 for the recipient-widening follow-up.
- Agent rebuild + cross-compile + redeploy to envoys that should support the new verbs. Envoys without the new agent build refuse the new verb at the CLI; the daemon's swarm-server endpoint is forward-compatible for older agent CLIs (older agents simply don't know to call `/swarm/self-fleet/rekey-claim/`).
- No server change. No DB migration. No proto change.

**Behavioral consequences:**
- The "blow it all away" runbook for compromise scenarios is replaced with: `rekey-pair` on a trusted founder envoy → `rekey --from <host>` on each surviving envoy → re-invite collaborators on any gitmirror projects. Cryptsync state and per-envoy age keys are preserved.
- Any envoy offline during the rotation is genuinely orphaned. `self-fleet status` on the orphaned envoy surfaces a hint pointing at the divergent peer; the operator runs `rekey --from <peer>` to migrate. Without operator action the envoy stays on the retired fleet identity and is silently excluded from the new fleet's recipient sets.
- Operators must accept that during the 24h graceful window, cryptsync recipient sets are asymmetric. As of 0.35.17 the founder includes unrekeyed peers in its recipient set (because retired fleet pubkeys are threaded into the filter), so mid-window writes on the founder propagate normally. The inverse direction stays broken: an unrekeyed envoy's TOFU pin rejects the founder's new-fleet announce, so the unrekeyed envoy's writes during the window still encrypt to a smaller recipient set than steady state until that envoy itself rekeys. This residual is called out in the verb's output and the CLI reference.
