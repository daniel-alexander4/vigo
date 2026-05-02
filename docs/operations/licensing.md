
# Licensing

Vigo uses a built-in licensing system. All features are included at every tier — the license controls node count only, nothing else.

## Brackets

Pricing is graduated: each bracket's per-node rate applies only to the nodes within that bracket. The first 100 nodes are always free.

| Tier | Bracket | Per Node/Year | Support |
|------|---------|---------------|---------|
| Community | 1 – 100 | Free | None (AS IS) |
| Starter | 101 – 250 | $144 | Standard (2 business days) |
| Growth | 251 – 1,000 | $132 | Standard (2 business days) |
| Business | 1,001 – 5,000 | $120 | Priority (1 business day) |
| Enterprise | 5,001 – 25,000 | $108 | Dedicated (4 business hours) |
| Scale | 25,001 – 100,000 | $96 | Dedicated (4 business hours) |

Worked example: a 500-node fleet pays 0 (1–100) + 150 × $144 (101–250) + 250 × $132 (251–500) = **$54,600/year**. Your support level is set by the highest bracket your fleet straddles.

See `COMMERCIAL-TERMS.md` for full pricing details including loyalty discounts, multi-year commitments, and enterprise agreements.

## Community Tier — No Support, AS IS

The Community tier is provided free of charge with **no support obligation**. Alexander4, LLC does not guarantee any response to bug reports, questions, or requests. Community tier email is monitored on a voluntary, best-effort basis.

The Software is provided "AS IS" without warranty of any kind. Total aggregate liability to Community tier users is capped at $50. You assume all risk associated with your use of the Software. If you require guaranteed response times or configuration assistance, upgrade to a paid tier.

## Machine Fingerprint Binding

Paid licenses are bound to a specific server installation. The fingerprint is derived from machine-specific identifiers (machine ID, primary MAC address, TLS CA certificate), preventing license sharing across servers. If you migrate to a new machine, you will need a new license.

Generate your server's fingerprint:

```bash
vigocli license fingerprint
```

## Enforcement

Enrollment enforcement has two independent mechanisms:

### Hard Node-Count Gate

The primary enforcement mechanism. When your fleet reaches the node limit on the license (`MaxEnvoys`), the **next enrollment attempt is rejected immediately**, with no grace period. The rejection happens regardless of which enrollment path is used:

- gRPC `Register` (token-based enrollment)
- HTTP `POST /api/v1/bootstrap/register` (trusted-CIDR and token enrollment used by the curl one-liner)
- gRPC `AdminService.EnrollEnvoy` (hub→spoke internal enrollment)

All three paths funnel through a single chokepoint inside the registry that holds a serialization mutex across the live-count check, the DB insert, and the fleet-index update. Two concurrent registrations at the boundary cannot both pass the gate — one succeeds, the other receives `PermissionDenied` with `license violation: node limit reached (N/max)`.

`--force` replacement (revoke old envoy, enroll new one with the same hostname) is always allowed at the limit because the revoke runs before the gate, under the same mutex. Net-zero enrollments do not trip the limit.

Failed counter reads (internal errors inside the gate) fail closed — the enrollment is rejected rather than allowed through. This is tracked by the `vigo_license_enforcement_errors_total` Prometheus counter so operators can spot repeated internal errors that would otherwise masquerade as "license reached".

### Staged Enforcement (License Downgrade / Expiry / Fingerprint)

When a license changes state *without* operator action — license file expires, a new (smaller) license is installed on a fleet that already exceeds its cap, or the machine fingerprint no longer matches — the staged enforcement provides a 30-day runway to reconcile:

1. **Grace Period (days 1–15)** — Full functionality. Warning logged on every check-in. Grace clock resets immediately when returning to compliance.
2. **Enrollment Block (days 15–30)** — New envoy enrollments are rejected. Existing envoys continue to function normally and are never locked out.
3. **Service Stop (day 30+)** — Server shuts down. Agents continue enforcing their last-known policy indefinitely.

The staged enforcement does not fire for normal over-limit enrollment, because the hard gate blocks that before it can ever reach the stage machine. It is specifically a runway for license-side changes that leave the fleet retroactively over-limit.

### Capacity Warnings

Compliant fleets that are approaching the cap see advance warning:

- **Dashboard banner** — orange notice at ≥90% of cap, red notice at 100%. Rendered on every dashboard page load while the condition holds.
- **License page** — `Nodes Used` stat card shows `N / max` with a badge (`nearing cap` at ≥90%, `at capacity` at 100%).
- **Check-in log** — each check-in emits a warning in the server log when the fleet is at or near cap, so operators tailing `journalctl` see it even when not watching the web UI.
- **Bootstrap one-liner** — successful enrollments include `license_warning` text in the HTTP response payload, which the curl bootstrap prints to stderr if set. Gives operators a heads-up during the enrollment that pushes them over ≥90%.
- **Prometheus** — `vigo_license_envoys_used` / `vigo_license_envoys_max` gauges, plus `vigo_license_enrollments_blocked_total` counter for rejections.

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
