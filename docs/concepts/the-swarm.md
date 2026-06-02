# The swarm

**Why do envoys talk to each other at all, and what flows between them?**

Most of Vigo is server↔agent. The **swarm** is the exception: a peer-to-peer substrate where envoys exchange content directly, over mTLS, without routing every byte through the server. It exists so that distributing a 2 GB image, a git repo, or a fleet-wide file doesn't hammer one server — the blob moves node-to-node, the server only publishes pointers.

## One substrate, six content subsystems

The substrate handles seed management, manifest CRDT gossip, and peer tracking. Six subsystems ride it (agents serve blobs over mTLS on port 1531):

| Subsystem | What it moves | Confidentiality |
|---|---|---|
| **filecast** | Admin-pushed fleet file distribution | — |
| **gitback** | Git over the swarm (personal-DR repos, ADR-015) | availability, not secrecy |
| **longdrawer** | LAN per-user directory sync, plaintext | none (LAN-only) |
| **lockbox** | Per-user directory sync, encrypted at rest | ciphertext everywhere |
| **curator** | P2P artifact registry — binaries, images (ADR-024) | signed, verifiable |
| **poolq** | P2P ordered-log queue, Kafka-style (ADR-029) | signed, replayable |

All six are rooted in one identity primitive — the [puddle](puddle-identity.md).

## How it stays trustworthy without a trusted server

The server **publishes, never arbitrates.** It aggregates what agents report (via traits) into read-only fleet views, but agents verify every signature and delegation chain **end-to-end** — they don't trust the server's word. A content subsystem's mesh aggregator (`gitbackmesh`, `curatormesh`, …) is a *view*, not an authority.

## Two-layer authorization

Both layers must say yes; default-deny on both:

1. **Envoy layer** — is the subsystem enabled on this envoy? (`server.yaml#swarm.*.enabled` pattern list vs hostname).
2. **User layer** — which Unix users may use it? (usercrate `puddle:` / `gitback:` / `curator:` flags).

The destructive lever is the **usercrate flag** — removing it scrubs the user's held data on the next reconcile. Pattern-list flips are gate-only; held data survives.

## Where this shows up

- [Puddle identity](puddle-identity.md) — the per-user anchor everything builds on.
- [Set up swarm](../howto/set-up-swarm.md), then [gitback](../howto/set-up-gitback.md) · [lockbox](../howto/set-up-lockbox.md) · [curator](../howto/set-up-curator.md) · [poolq](../howto/set-up-poolq.md) · [longdrawer](../howto/set-up-longdrawer.md).
- The `/swarm/*` UI pages and [`vigocli swarm`](../reference/vigocli/swarm.md).

---
*Confidential — Alexander4, LLC. Not for redistribution.*
