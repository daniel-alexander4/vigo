---
title: Envoy Lifecycle (Revoke, Puddle Evict, Delete)
---

# Envoy Lifecycle

Vigo has three operator verbs for removing or disabling an envoy, with deliberately different scopes. Operators routinely confuse them — "revoke" reads like "remove from everything," "delete" reads like "scrub completely" — and the actual behavior is more nuanced. This page documents what each one does, what survives, and the order to run them when retiring a host for good.

For the HIPAA-flavored physical-disposal procedure (sanitize on-disk Vigo state, generate an attestation), see [Decommissioning Procedures](../compliance/decommissioning.md). This page is the conceptual reference; that one is the runbook.

## The Three Verbs

| Verb | CLI | What it does | What survives |
|---|---|---|---|
| **revoke** | `vigocli envoys revoke <host>` | Policy gate. Flips `envoys.revoked=1` so the envoy is rejected on its next check-in and cannot re-enroll with the same UUID. Hides it from the dashboard, envoy lists, and the swarm meshes. Cross-fleet cleanup: evicts the envoy from every other puddle member's `cryptsync` recipients list (added 0.41.3). | Envoy DB row, traits, run history, audit log, sandgorgon asset record, gitmirror project membership. |
| **puddle evict** | `vigocli swarm puddle evict <host>` | Cross-envoy puddle eviction. Tells every other puddle member to drop this host from its cryptsync peer set, regenerate recipients, and tombstone any further messages from it. | Everything revoke preserves, plus the envoy's own `~/.cryptsync/`, `~/.vigo-puddle/`, `~/cryptsync/` (those scrub via `vigo swarm puddle leave` on the *evicted* envoy itself if reachable). |
| **delete** | `vigocli envoys delete <host>` | Revoke + DB cascade. Runs the same cryptsync recipients cleanup, then `DELETE FROM envoys` (CASCADE wipes traits, runs, run_results, scrier_sessions). Removes the in-memory `FleetIndex` entry so the host disappears from operator-facing surfaces immediately. | Audit log (append-only, never deleted), sandgorgon asset records (`ON DELETE SET NULL` — bare-metal inventory outlives enrollment), gitmirror project membership (anchored to puddle pubkeys per [ADR-014](../adr/014-puddle-primitive.md), not envoy IDs). |
| **purge** (alias) | `vigocli envoys purge` | Fleet-wide dedup wrapper that calls `delete` on duplicate hostname entries, keeping only the most-recently-seen UUID per hostname. Same backend, same cascade as `delete`. Not a separate semantic. | Same as `delete`. |

## What Survives Each Verb (Detail)

### After revoke

- **DB row** — present, `revoked=1` flag set. Audit-log queries can still cite the envoy.
- **Traits, runs, run results** — all preserved. Useful for forensics and post-incident review.
- **Audit log** — append-only, untouched.
- **Cryptsync recipients on other envoys** — *cleared* (the envoy can no longer decrypt cryptsync ciphertext that lands on its peers, and the peers stop encrypting to it).
- **Gitmirror project membership** — preserved. The envoy's bare repos under `/var/lib/vigo/swarm/gitmirror/projects/` stay on disk; if the host is re-enrolled under the same puddle, projects come back.
- **Filesync state on other envoys** — unchanged (filesync has no server-side per-envoy state to clean up).
- **Sandgorgon asset record** — preserved.

### After delete (and purge)

- **DB row** — *gone*. CASCADE wipes traits, runs, run_results, scrier_sessions, recent_activity rows.
- **Audit log** — preserved (append-only).
- **Sandgorgon asset record** — preserved (`ON DELETE SET NULL` — inventory outlives the enrollment).
- **Cryptsync recipients on other envoys** — *cleared* (same as revoke).
- **Gitmirror project membership** — preserved by design. Membership is anchored to the puddle pubkey, not the envoy ID. Re-bootstrap the host under the same puddle and projects come back. Counterintuitive — `delete` looks more destructive than it is for swarm-level state. To actually evict from puddle, run `puddle evict` first.
- **FleetIndex entry** — removed in-memory. Host disappears from dashboard, envoy lists, swarm meshes immediately.

### After puddle evict

- Targeted at *cross-envoy* puddle membership, not at the envoy's own state. The other puddle members drop this host from their peer tables, regenerate `~/.cryptsync/recipients.txt` without this host's key, and tombstone any further messages from this host.
- The evicted envoy itself, if still online and reachable, runs `vigo swarm puddle leave` to scrub `~/.vigo-puddle/`, `~/.cryptsync/`, `~/cryptsync/`, `~/.vigo-gitmirror/`. If unreachable (the typical eviction case), state stays on the host until physical decommissioning.

## Operator Flows

### Retiring a host that's still online

```bash
# 1. Block re-enrollment and clean up cryptsync recipients fleet-wide.
vigocli envoys revoke <host>

# 2. Evict from puddle so other envoys regenerate recipients without this host.
vigocli swarm puddle evict <host>

# 3. (On the host itself, if reachable) scrub local swarm state.
ssh <host> 'vigo swarm puddle leave --yes'

# 4. Cascade-delete from the DB so it disappears from operator surfaces.
vigocli envoys delete <host>
```

The CLI does not yet expose this as a single composite verb (filed as a follow-up — see the pending list).

### Retiring a host that's already offline

Skip step 3 (the host is unreachable). The local `~/.vigo-puddle/`, `~/.cryptsync/`, `~/.vigo-gitmirror/` directories remain on the host until you physically dispose of it — see the [decommissioning procedures](../compliance/decommissioning.md) for the disk-sanitization runbook.

### Replacing a host (same hostname, fresh hardware)

```bash
# 1. Revoke + delete the old enrollment.
vigocli envoys revoke <host>
vigocli envoys delete <host>

# 2. Bootstrap the replacement host normally — it gets a fresh UUID.
curl -sSfk https://vigo.example.com/bootstrap | sudo sh
```

**Gitmirror caveat:** if the replacement host joins the same puddle, it picks up the puddle's gitmirror project state automatically. The bare repos under `/var/lib/vigo/swarm/gitmirror/projects/` are populated via fan-out on the next push.

### Pausing an envoy without losing data

Use [maintenance mode](maintenance-windows.md) — temporarily exempts the host from convergence runs without revoking or deleting anything. When maintenance ends, the host resumes normal operation with all state intact.

## Why the Asymmetry

Each verb has a single job, and overlap is deliberate:

- **Revoke** is the policy-level "this host should not act on the fleet." Preserves data because forensics and audit need the historical record.
- **Puddle evict** is the swarm-level "this host should not be part of the encrypted-sync collective." Cryptsync's per-envoy keys (per [ADR-016](../adr/016-cryptsync-membership-on-puddle.md)) preserve compartmentalization — eviction is the operator's tool to drop a compromised or retired envoy from the recipients list across the fleet.
- **Delete** is the storage-level "I'm done with this enrollment record." Cascades the per-envoy DB rows so the dashboard isn't cluttered with retired hosts. Sandgorgon assets and audit log explicitly survive because they belong to a different lifecycle (physical inventory, regulatory record).

Conflating them — making `delete` also do `puddle evict` automatically, for instance — would surprise operators who delete a host expecting the puddle to keep it as a legitimate member (e.g., to swap a UUID after re-enrollment).

## Audit Trail

Every revoke and delete writes to the hash-chained audit log with the operator identity, target hostname/UUID, and timestamp. Audit queries accept revoked and deleted envoys by hostname even after the DB row is gone — the audit log is the system of record for "who removed what and when," and is never CASCADE'd.

## See Also

- [Decommissioning Procedures](../compliance/decommissioning.md) — HIPAA-flavored disk-sanitization runbook for retired hardware.
- [Maintenance Windows](maintenance-windows.md) — temporary pause without revocation.
- [ADR-014: Puddle Identity Model](../adr/014-puddle-primitive.md) — why gitmirror project membership survives delete.
- [ADR-016: Cryptsync Membership on Puddle](../adr/016-cryptsync-membership-on-puddle.md) — why per-envoy cryptsync keys make eviction necessary.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
