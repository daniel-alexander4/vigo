---
title: "ADR-018: Puddle rekey"
---

# ADR-018: Puddle rekey

**Status:** Complete. Shipped across two slices: rekey verbs + orchestration (0.35.14) and status surface + ADR-018 + docs (0.35.15). Prereqs landed at 0.35.12 (lockbox `identity_rewrap` primitive) and 0.35.13 (claim-code refactor). The originally-deferred lockbox recipient-set widening shipped as a follow-up at 0.35.17 — the founder's `live_pubkeys_for_fleets` filter now accepts both the current puddle pubkey and every unexpired retired puddle pubkey, so mid-window writes on the founder reach unrekeyed peers. Swarmgit founder-rekey was picked up and shipped under **[ADR-019](019-swarmgit-founder-rekey.md)** (anchor-preserving via per-project delegation chains) across five slices at 0.35.18–0.35.22 — see ADR-019 for the design.
**Date:** 2026-04-30

## Context

ADR-017 established that puddle-level revoke is a placebo: the only correct response to a compromised puddle keypair is to retire it (re-init the puddle) and rotate every other credential the lost device touched. That decision left an obvious operational pain — naïve re-init blows away surviving envoys' lockbox state, all swarmgit project_ids the user founded, and offers no path back for envoys that were offline during the rotation. ADR-014 deliberately scoped re-init to "delete `~/.vigo-puddle/` and start over" because the v1 model assumed loss-of-passphrase was the only rotation event; in practice users will rotate after a stolen-laptop scare, after a routine hygiene window, or when a founder envoy is being decommissioned cleanly, and "blow it all away" is a bigger hammer than the situation needs.

This ADR introduces a rotation flow that preserves lockbox state on surviving envoys, records a 24h graceful retirement window, and surfaces UX hints for envoys that missed the rotation. It does **not** address the swarmgit project_id rotation problem (project_id = sha256(founder_puddle_pubkey)) — that is its own design exercise involving capability-token chain anchoring across the rotation, and is queued for a future ADR.

## Decision

### Two new verbs

```
vigo swarm puddle rekey-pair [--close] [--window-secs <N>]
vigo swarm puddle rekey --from <host>
```

`rekey-pair` runs on any current puddle member that the operator trusts as the rotation source (typically the same envoy that ran `init`, but any member with the unwrapped key works). It is the rotation event itself: re-prompts for the puddle passphrase, verifies it unwraps the existing key, generates a new keypair under the same passphrase + a fresh salt, atomic-renames `~/.vigo-puddle/identity.{wrapped,salt,pub}` to `.pre-rekey-<unix_ts>` backups (30-day retention via the same GC that catches lockbox's rewrap backups), runs the lockbox identity-rewrap primitive (ADR-016 slice 4, extracted in 0.35.12), clears local lockbox TOFU pins for the retired puddle pubkey, regenerates `recipients.txt`, restarts the puddle session helper with the new key, and records a `RetiredFleetEntry` in `~/.vigo-puddle/retired_puddles.json` with `expires_at = retired_at + 24h`. It then issues a 5-minute single-use claim code via `~/.vigo-puddle/rekey.pending` so surviving envoys can claim the new wrapped blob.

Re-running `rekey-pair` inside the active 24h window only reissues a fresh claim code — the keypair and on-disk state are untouched. This lets the founder walk multiple surviving envoys through migration without re-rotating each time. `--close` pulls the active window's `expires_at` forward to `now`, useful when the operator confirms all surviving envoys have migrated and wants to retire the rekey session early. `--window-secs <N>` overrides the 24h default for unusual cadences (e.g., a one-off hygiene rotation where the operator wants only a 1h window).

`rekey --from <host>` runs on each surviving envoy. It requires the OLD puddle passphrase to unwrap the local key (lockbox rewrap needs the old key in process scope), POSTs the claim code to `https://<host>:1531/swarm/puddle/rekey-claim/<user>` over swarm mTLS, unwraps the returned blob with the same passphrase, runs the same atomic swap + lockbox sync as the founder, locks the local session helper. The operator runs `unlock` afterwards in their own (non-sudo) shell to start a fresh helper under the new key.

### Why these specific shapes

**Re-prompt for the puddle passphrase at `rekey-pair` issuance.** Even with the session helper already unlocked, `rekey-pair` re-prompts. This is one extra prompt at rotation time and defends against a brief unattended-shell threat: an attacker who briefly has access to the founder's already-unlocked shell still needs the passphrase to authorize a rotation. Skipping the prompt is a cheap-feeling shortcut that creates an unnecessary attack window, and the operator is already on the founder envoy with a passphrase prompt right there for `unlock`.

**Graceful retirement, not decisive cutover.** The 24h window means the founder accepts new rekey-claim requests for the rotated-out identity as long as the window is open. This is operational room for the operator to walk envoy-by-envoy without racing a hard cutover. The trust boundary against attackers is the 5-minute claim code, not the window — a stolen pairing code is the actual compromise vector, and the operator dictating an 8-character code over an out-of-band channel (voice / SMS / in-person) is the same security posture as the original `pair` flow.

**No chain-of-trust shortcut from the old key.** A "rekey envelope signed by the OLD key" sounds attractive — surviving envoys would just verify the envelope and accept the new key without operator intervention. Rejected: the threat model includes a compromised old key. An attacker holding the same OLD key can produce a byte-identical signature and direct surviving envoys to the attacker's puddle pubkey instead. Each surviving envoy has to do fresh out-of-band authentication (the pairing code from a trusted founder), exactly like the initial join. This is friction by design, not a UX gap.

**Same passphrase across the rotation.** The new wrapped blob is encrypted under the same passphrase as the old. The operator types one passphrase per envoy, used twice (unwrap-old-locally, unwrap-new-from-claim). Forcing the operator to type a new passphrase would make rotation feel like re-init, undoing the UX win. Operators who want to rotate the passphrase can run `rotate-passphrase` separately afterwards (per-envoy, no puddle-wide ceremony).

**Salt is reused across lockbox rewrap; refreshed across puddle rotation.** Lockbox's `identity_rewrap` keeps the existing `identity.salt` because the wrap key changes via the password material (different deterministic Ed25519 signature), and a single-file atomic rename owns the migration. Puddle's rotation generates a fresh salt because the keypair is fully new and there's no migration constraint demanding a stable salt — fresh salt is the conservative default.

**Local retirement log on every rekeying envoy.** `~/.vigo-puddle/retired_puddles.json` is local-only; no envelope, no gossip. The founder's entry powers `rekey-pair` reissue logic + `--close`; each surviving envoy records its own entry for `status` visibility ("we rotated to ed25519:new on date X"). Cross-envoy gossip of rotation history is intentionally out of scope — it would require a trust-rooted signing primitive that doesn't exist in v1, and the local log already gives operators the audit trail they need.

**Offline-envoy hint in `status`.** When `puddle status` runs on an envoy whose local puddle pubkey diverges from peers' announces (visible via the lockbox peer table), it surfaces "peer X is on a different puddle pubkey; if you rekeyed there, run `rekey --from X`." Read-only inspection — no automatic key-update is attempted. This addresses the offline-during-rekey scenario without requiring a trusted retirement-announcement primitive (which would have the same chain-of-trust shortcut problem the rekey design rejects above).

### What stays out of scope

- **Swarmgit founder rotation.** Originally framed as "every swarmgit project the user founded has a dead URL after a rekey." Picked up under [ADR-019](019-swarmgit-founder-rekey.md) and shipped 0.35.18–0.35.22 — see that ADR for the anchor-preserving design (per-project delegation chains, multi-puddle `re-invite-all` + `rejoin`, `re-found` escape hatch).
- **Lockbox recipient resolution during the rotation rollout (originally deferred; shipped at 0.35.17).** Between founder rotation and surviving-envoy rotation, the founder's filter previously excluded peers whose stored `puddle_pubkey` was the OLD value, so files written on the founder during the gap encrypted to a smaller recipient set. The follow-up renamed `live_pubkeys_for_fleet` to `live_pubkeys_for_fleets` (taking a slice of accepted puddle pubkeys) and threaded `~/.vigo-puddle/retired_puddles.json` into the call site in `regenerate_recipients`. The founder now accepts both its current puddle pubkey and every unexpired retired puddle pubkey, so mid-window writes on the founder reach unrekeyed peers. The inverse direction (writes from a not-yet-rekeyed envoy reaching the rekeyed founder) still drops because the unrekeyed envoy's TOFU pin rejects the founder's NEW announce until that envoy itself rekeys; that asymmetry is the honest residual cost of the rollout.
- **Multi-founder rekey.** "What if two founders both have the old puddle key and both run `rekey-pair`?" — undefined; both produce different new keypairs; the user would have to pick one and re-rekey the other envoys to reconcile. The flat-trust design of puddle (every member is equal) makes this a self-imposed problem the operator avoids by convention.

## Alternatives Considered

**Re-init from scratch.** What ADR-014 originally implied. Rejected because the blast radius is much larger than rekey: surviving envoys' puddle-mode lockbox `identity.age` becomes undecryptable (wrap key was puddle-key-derived; new puddle key produces a different derivation), every swarmgit project the user founded changes identity, offline envoys are orphaned without any migration path. Rekey solves the lockbox-state preservation; swarmgit remains orphaned but that's a constant across both approaches.

**In-place keypair rotation that doesn't touch lockbox.** Generate new puddle key, write new identity files, leave lockbox's `identity.age` unchanged. Rejected: puddle-mode lockbox derives its wrap key from the puddle keypair; rotating without rewrapping means the next `lockbox unlock` on the rekeyed envoy can't unwrap its own identity. The operator would have to manually re-init lockbox, blowing away the per-envoy age key. The whole point of preserving lockbox state is gone.

**Frictionless rekey via old-key-signed handoff envelope.** Founder's `rekey-pair` produces an envelope signed by the OLD puddle key naming the NEW puddle pubkey; surviving envoys auto-accept on first sighting. Rejected for the threat-model reason above: an attacker holding the old key signs the same envelope and points victims at their own pubkey. The pairing-code shape is the only honest way to gate a rotation against a key-compromise attacker.

**Deferred TOFU clear (don't touch lockbox state during rekey).** Skip the `clear_pins_for_fleet` + `regenerate_recipients_now` calls; let the operator run `lockbox forget <host>` per peer when TOFU rejections fire post-rotation. Rejected: every surviving envoy would have to manually clear pins, and the operator probably wouldn't realize that's what's needed until a missing-file complaint surfaces. Doing it at rotation time is a clear cost localized to one verb.

## Consequences

**Code changes:**
- `agent/src/swarm/puddle/codes.rs` — shared claim-code primitives (extracted in 0.35.13 ahead of this ADR).
- `agent/src/swarm/puddle/rekey.rs` — `RetiredFleetEntry` persistence, active-session predicate, `apply_post_rotation_lockbox_sync`, `handle_rekey_claim` HTTP handler.
- `agent/src/swarm/puddle/cli.rs` — `cmd_rekey_pair` and `cmd_rekey` verbs; status surface enrichment for retired-puddles visibility and offline-envoy hint.
- `agent/src/swarm/puddle/config.rs` — `rekey_pending_path` + `retired_puddles_path`.
- `agent/src/swarm/server.rs` — dispatch `REKEY_CLAIM_PREFIX` to the rekey handler.
- `agent/src/swarm/lockbox/identity_rewrap.rs` — atomic re-wrap primitive (shipped in 0.35.12 as the slice-1 foundation for this ADR).
- `agent/src/swarm/lockbox/state.rs` — `clear_pins_for_fleet` for post-rotation TOFU re-pin; `live_pubkeys_for_fleets` accepts a slice of acceptable puddle pubkeys (0.35.17 follow-up — replaces single-puddle `live_pubkeys_for_fleet`, widening the founder's recipient filter during the rekey rollout).
- `agent/src/swarm/lockbox/mod.rs` — `regenerate_recipients` reads `~/.vigo-puddle/retired_puddles.json` and includes every unexpired retired puddle pubkey in the accepted set (0.35.17 follow-up).

**Documentation:**
- This ADR.
- `docs/reference/cli/agent-swarm-puddle.md` — verb reference table extended with rekey-pair / rekey rows; a Rekey walkthrough section.
- `docs/adr/_index.md` — entry added.

**Operational:**
- Z-bumps for the implementation slices: 0.35.11 (ADR-017 docs), 0.35.12 (rewrap primitive), 0.35.13 (codes refactor), 0.35.14 (rekey verbs + orchestration), 0.35.15 for status / docs / ADR-018, 0.35.17 for the recipient-widening follow-up.
- Agent rebuild + cross-compile + redeploy to envoys that should support the new verbs. Envoys without the new agent build refuse the new verb at the CLI; the daemon's swarm-server endpoint is forward-compatible for older agent CLIs (older agents simply don't know to call `/swarm/puddle/rekey-claim/`).
- No server change. No DB migration. No proto change.

**Behavioral consequences:**
- The "blow it all away" runbook for compromise scenarios is replaced with: `rekey-pair` on a trusted founder envoy → `rekey --from <host>` on each surviving envoy → re-invite collaborators on any swarmgit projects. Lockbox state and per-envoy age keys are preserved.
- Any envoy offline during the rotation is genuinely orphaned. `puddle status` on the orphaned envoy surfaces a hint pointing at the divergent peer; the operator runs `rekey --from <peer>` to migrate. Without operator action the envoy stays on the retired puddle identity and is silently excluded from the new puddle's recipient sets.
- Operators must accept that during the 24h graceful window, lockbox recipient sets are asymmetric. As of 0.35.17 the founder includes unrekeyed peers in its recipient set (because retired puddle pubkeys are threaded into the filter), so mid-window writes on the founder propagate normally. The inverse direction stays broken: an unrekeyed envoy's TOFU pin rejects the founder's new-puddle announce, so the unrekeyed envoy's writes during the window still encrypt to a smaller recipient set than steady state until that envoy itself rekeys. This residual is called out in the verb's output and the CLI reference.
