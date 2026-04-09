
# Environment Isolation

How to prevent envoys in different environments (dev, staging, prod) from communicating with each other — including P2P swarm, gossip, and service discovery.

## CA-Per-Environment (Recommended)

The strongest isolation is cryptographic: **each environment gets its own Certificate Authority**. A dev envoy has a dev cert signed by the dev CA. A prod envoy has a prod cert signed by the prod CA. They cannot authenticate to each other because the TLS verifier on each side only trusts its own CA.

This applies to all communication channels:

| Channel | Isolation Mechanism |
|---------|-------------------|
| gRPC check-in (1530) | Agent cert must be signed by server's CA |
| Swarm P2P (1531) | Peer cert must be signed by the same CA |
| Multicast gossip (UDP 1531) | Gossip messages are unauthenticated discovery — but actual data exchange requires mTLS on 1531 |
| REST API (8443) | Server cert validated by CLI/browser |

To set up CA-per-environment, run `make certs` separately for each environment. Each environment gets its own `ca.crt`, `server.crt`, `server.key`. Agents enrolled against the dev server get dev certs; agents enrolled against the prod server get prod certs. No configuration needed — the isolation is inherent in the PKI.

```
dev:  ca-dev.crt  → dev server, dev agents
prod: ca-prod.crt → prod server, prod agents
```

A dev agent cannot:
- Check in with the prod server (cert not trusted)
- Download swarm blobs from prod peers (mTLS handshake fails)
- Gossip with prod envoys (data sync requires mTLS)

This is the same boundary that already exists for server communication. Extending it to P2P is automatic.

## Intra-Environment Isolation

For boundaries within the same environment — separating tiers (web, database, cache) or tenants within a shared infrastructure — there are lighter-weight options:

### Gossip Scoping

Different groups use different multicast addresses. Web servers gossip on one address, database servers on another. They never discover each other via multicast.

Configure via module:

```yaml
modules:
  swarm-web-tier:
    resources:
      - name: gossip-scope
        type: exec
        # Agent config sets the multicast group
```

The server can still mediate cross-tier communication selectively via the peer list in check-in responses.

### Swarm Peer Filtering

When the server builds the peer list for a check-in response, it can filter by `environment:`, `site:`, role, or any trait. An envoy only learns about peers the server explicitly includes. This is a server-side policy — the agent trusts whatever peer list the server provides.

### Network-Level Isolation

Firewall rules blocking port 1531 between tiers. The simplest and most auditable approach. Vigo modules can enforce these rules, making the isolation self-enforcing:

```yaml
modules:
  web-tier-firewall:
    resources:
      - name: block-db-swarm
        type: firewall
        rule: deny
        port: 1531
        source: "10.0.2.0/24"  # database subnet
```

## Choosing an Approach

| Scenario | Recommended Approach |
|----------|---------------------|
| Dev vs staging vs prod | Separate CAs (one server per environment) |
| Tiers within prod (web vs db) | Firewall rules on port 1531 + server peer filtering |
| Multi-tenant (MSP managing multiple clients) | Separate CAs per tenant (spanner hub routes to tenant spokes) |
| Compliance zones (PCI CDE vs non-CDE) | Separate CAs + firewall rules (defense in depth) |
| Geographic regions | Server peer filtering by `site:` + multicast scoping (multicast is LAN-only anyway) |

## Related

- [Architecture](architecture.md) — Network ports and data flows
- [Swarm Concepts](swarm.md) — P2P distribution and gossip
- [Server Configuration](../getting-started/server-config.md) — TLS certificate paths
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
