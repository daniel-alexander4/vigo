# Spanner federation

**What happens when one server isn't enough?** You federate. **Spanner** (ADR-026) lets several servers act as one control plane — not a primary with replicas, but **write-equal peers**, each owning a slice of the fleet.

## The short answer

In `standalone` mode, one `vigosrv` owns everything. In `spanner` mode, each server is a **bolt** — a write-equal peer that owns a hostname-pattern partition of envoys and holds the **full admissions roster** (CRDT-replicated). There is no hub: admin reads, enrollment routing, force-push, reassignment, and drain are all peer-equal, driven off a gossip-backed observability cache and direct peer-to-peer gRPC.

This is how Vigo's tight-cadence scale goes from "tens of thousands of envoys per server" to **uncapped** — add bolts.

## How peers stay consistent without a leader

Two tiers of gossip:

- **Tier 1** — multicast counter gossip (cheap, frequent) for liveness + observability.
- **Tier 2** — REST roster-snapshot pull for the full admissions state.

Each bolt has its own Ed25519 identity; it signs its admission row and every gossiped roster snapshot, and peers pin each bolt's pubkey at admission. A `consumed_tokens` G-Set (grow-only CRDT) enforces one-shot membership tokens — a join token can't be replayed.

## Spanner is not HA

Don't confuse the two:

- **[High availability](high-availability.md)** (`server/peer/`) replicates *the same* fleet's state (config, secrets, TLS) to standby replicas for failover.
- **Spanner** partitions *different* fleet slices across peers and does **not** replicate secrets or TLS material between them.

You can run both: HA within a partition, spanner across partitions.

## Membership

`init` founds the federation; `invite` mints a one-shot join token; `join` claims it. Retired hub-spoke vocabulary (`role`, `hub_addr`, …) hard-errors with a migration pointer.

## Where this shows up

- [Set up spanner](../howto/operate/set-up-spanner.md) — found, invite, join.
- The `/spanner` UI + [`vigocli spanner`](../reference/vigocli/spanner.md).
- [High availability](high-availability.md) — the other axis of scale.

---
*Confidential — Alexander4, LLC. Not for redistribution.*
