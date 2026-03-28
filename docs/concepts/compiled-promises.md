
# Compiled Promises

Compiled promises are signed policy bundles that allow agents to converge locally when the server is unreachable. Bundles are cryptographically signed and verified. Agents cache policy for offline operation.

## How It Works

![Compiled Promises Flow](img/compiled-promises.svg)

## Bundle Signing

The server generates a signing key at startup. Each policy bundle is signed before delivery to agents. The agent stores the server's public key on first successful check-in and uses it to verify all subsequent bundles.

## Bundle TTL

Bundles have a configurable maximum age:

```yaml
checkin:
  bundle_max_age: "24h"  # default
```

| Value | Behavior |
|-------|----------|
| `24h` (default) | Bundles expire after 24 hours. Agent proactively renews before expiry |
| `0` | Bundles never expire (not recommended for production) |
| `168h` | One week validity (for intermittently-connected envoys) |

When a bundle expires, the agent stops converging and waits for server contact.

## Offline Convergence

When the server is unreachable, the agent falls back to its cached policy bundle. If the bundle is still within its TTL, the agent continues to converge locally using cached policy and traits. Results are queued and drained when the server becomes reachable again.

## Security

- Bundles are signed, not encrypted — the policy content is visible to the agent (by design)
- Signature verification prevents tampering with cached bundles
- The server key is ephemeral (regenerated per server start) — all agents refresh on server restart
- Secret values in bundles are resolved at check-in time and transmitted over mTLS

## Configuration

Server-side:

```yaml
checkin:
  bundle_max_age: "24h"  # bundle validity period
```

Agent-side: No configuration needed. The agent automatically:
- Accepts and verifies signed bundles
- Caches them locally
- Falls back to cached bundles when offline
- Drains pending results on reconnect

To inspect the agent's cached bundle, pending results queue, and traits cache, run `vigo status` on the envoy. See [Agent CLI](../reference/cli/agent.md#status) for details.

## Related

- [Check-in Lifecycle](check-in-lifecycle.md) — Normal check-in flow
- [Architecture](architecture.md) — System overview
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
