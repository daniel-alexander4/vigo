
# Licensing

Vigo uses a built-in licensing system. All features are included at every tier — the license controls node count only, nothing else.

## Tiers

| Tier | Nodes | Per Node/Year | Support |
|------|-------|---------------|---------|
| Community | 1 – 25 | Free | None (AS IS) |
| Starter | 26 – 250 | $144 | Standard (2 business days) |
| Growth | 251 – 1,000 | $132 | Standard (2 business days) |
| Business | 1,001 – 5,000 | $120 | Priority (1 business day) |
| Enterprise | 5,001 – 25,000 | $108 | Dedicated (4 business hours) |
| Scale | 25,001 – 100,000 | $84 | Dedicated (4 business hours) |

See `COMMERCIAL-TERMS.md` for full pricing details including loyalty discounts, multi-year commitments, and enterprise agreements.

## Community Tier — No Support, AS IS

The Community tier is provided free of charge with **no support obligation**. Alexander4, LLC does not guarantee any response to bug reports, questions, or requests. GitHub Issues are monitored on a voluntary, best-effort basis.

The Software is provided "AS IS" without warranty of any kind. Total aggregate liability to Community tier users is capped at $50. You assume all risk associated with your use of the Software. If you require guaranteed response times or configuration assistance, upgrade to a paid tier.

## Machine Fingerprint Binding

Paid licenses are bound to a specific server installation. The fingerprint is derived from machine-specific identifiers (machine ID, primary MAC address, TLS CA certificate), preventing license sharing across servers. If you migrate to a new machine, you will need a new license.

Generate your server's fingerprint:

```bash
vigocli license fingerprint
```

## Enforcement Stages

When a license violation is detected (envoy count exceeds limit, license expired, or fingerprint mismatch), enforcement proceeds through three stages:

1. **Grace Period (days 1–15)** — Full functionality. Warning logged on every check-in. Grace clock resets immediately when returning to compliance.
2. **Enrollment Block (days 15–30)** — New envoy enrollments are rejected. Existing envoys continue to function normally and are never locked out.
3. **Service Stop (day 30+)** — Server shuts down. Agents continue enforcing their last-known policy indefinitely.

## CLI Commands

```bash
vigocli license status          # View current license status
vigocli license install <file>  # Install a license file
vigocli license inspect [file]  # Inspect a license file without installing
vigocli license fingerprint     # Display machine fingerprint
```

## API

```
GET /api/v1/license
```

Returns current license status including stage, tier, envoy counts, and expiration. See [REST API](../reference/api/rest.md#license).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
