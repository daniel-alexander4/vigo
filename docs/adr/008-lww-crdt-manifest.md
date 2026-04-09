# ADR-008: LWW-CRDT for swarm manifest

**Status:** Accepted  
**Date:** 2026-04-01

## Context

The swarm subsystem distributes blobs peer-to-peer between agents. Each agent needs to know which blobs exist, their metadata, and whether any have been revoked. This manifest must be synchronized across all agents without requiring the server to be available for every update.

## Decision

Use a Last-Writer-Wins Element Map (LWW-Map) CRDT for the swarm manifest. Each entry is keyed by blob hash, with a timestamp determining the winning value. Manifests are gossiped between agents via multicast-triggered HTTPS sync and piggybacked on server check-ins.

## Consequences

- Agents can sync manifests peer-to-peer without server coordination
- Conflict resolution is automatic: latest timestamp wins, no merge logic needed
- Revocations are tombstones with a newer timestamp — they propagate the same way as additions
- Multicast discovery triggers HTTPS manifest sync — agents learn about peers on the local network and pull their manifest state
- Server acts as an authoritative seed but isn't required for peer-to-peer convergence
- Trade-off: LWW means a slow clock can lose updates. Acceptable because manifest writes are infrequent and come from the server (which has a reliable clock).
- Trade-off: tombstones accumulate. Seed retirement handles garbage collection.

## Alternatives considered

- **Server-authoritative manifest** — rejected because it creates a single point of failure for blob distribution; agents in a site should work even if the server is unreachable
- **Vector clocks** — rejected for complexity; LWW is sufficient when writes are rare and come from a single authority (the server)
- **Gossip-only without CRDT** — rejected because without a conflict resolution strategy, split-brain scenarios produce inconsistent manifests
