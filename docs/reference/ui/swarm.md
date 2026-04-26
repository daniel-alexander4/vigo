
# Swarm

**Route:** `/swarm` · **Active nav:** `swarm`

Fleet-wide view of the [swarm substrate](../../engineering/subsystems/swarm.md) — the envoy peer-to-peer network that Payload, Gitmirror, Filesync, and Cryptsync all ride on. Admin-only.

The nav group label "Swarm" is itself the link to this page; the four child links (Payload / Filesync / Gitmirror / Cryptsync) remain the drill-downs into each content subsystem's state.

## Sections

### Fleet Status (stat cards)

- **Envoys Online** — reachable within 2× check-in interval, over total enrolled
- **Substrate Active** — envoys with at least one peer record or acting as a seeder
- **Seeding Envoys** — distinct envoys originating at least one payload
- **Fleet Footprint** — sum of cached blob sizes across the fleet

### Content Subsystems (table)

Active-payload count per subsystem plus a deep-link to the subsystem page:

| Subsystem | What it is | Deep link |
|-----------|-----------|-----------|
| Payload | Admin-pushed payloads | `/payload` |
| Gitmirror | Personal-DR repo mirrors | `/gitmirror` |
| Filesync | Per-user plaintext sync | `/filesync` |
| Cryptsync | Per-user encrypted sync | `/cryptsync` |

Filesync and Cryptsync don't surface payload counts here — those are per-user content. Use the dedicated pages for user-level detail.

### Seeding Allowlist (table)

Which envoys are permitted to originate seeds, derived from `swarm.seeding_envoys` in `server.yaml` plus the implicit `server.hostname` entry.

### Peers (table)

One row per non-revoked envoy. Columns: hostname (link to envoy detail), online/offline status, substrate-active flag, seeding count, cached blob count, downloading count, footprint size, last-seen time.

Sort controls above the table: Footprint (default) / Blobs / Hostname. Default limit is 50; pass `?limit=N` for more, `?limit=0` for unbounded.

## Related

- [Swarm Substrate](../../engineering/subsystems/swarm.md) — Concept page
- [`vigocli swarm`](../cli/swarm.md) — CLI equivalent
- [Swarm Operations](../../operations/swarm.md) — Configuration and troubleshooting
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
