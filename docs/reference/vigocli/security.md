
# vigocli security

Umbrella command for security-posture operations: CVE search, risk scoring, and audit trail. Subcommands are registered from their own CLI configcrates:

| Subcommand | Purpose | Full reference |
|---|---|---|
| `vigocli security cve` | Search the fleet for a specific CVE across installed packages | [cve](cve.md) |
| `vigocli security risk` | Per-envoy and fleet-wide risk scoring | [risk](risk.md) |
| `vigocli security audit` | Tail / export the tamper-evident audit log | [audit](audit.md) |

Run `vigocli security --help` on a live server for the current set of subcommands.

## See also

- [Risk Scoring](../compliance-matrix.md) — how scores are computed
- [Tamper-Evident Audit](../compliance-matrix.md) — hash-chained audit trail
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
