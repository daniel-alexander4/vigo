# License

The license page at `/license` shows the current license status, node usage, and enforcement stage. It is accessible from the sidebar navigation on all pages.

## Stat Cards

Four stat cards at the top:

| Card | Description |
|------|-------------|
| Status | Current enforcement stage (Compliant, Grace Period, Enrollment Block, Service Stop). Green when compliant, yellow for grace, red for block/stop. |
| Nodes Used | Active envoys vs. licensed maximum (e.g., `12 / 25`). |
| Usage | Percentage of licensed nodes in use. Green under 75%, yellow 75–90%, red above 90%. |
| Days Remaining | Days until license expiry. Yellow when under 30 days. |

## License Details Table

A details table showing:

- **Customer** — Customer name (omitted for free tier)
- **License ID** — Unique license identifier
- **Tier** — License tier with display name (e.g., "Free (25 nodes)")
- **Max Nodes** — Maximum envoys allowed
- **Active Nodes** — Current active (non-revoked) envoy count
- **Expires** — License expiration date
- **Days Remaining** — Days until expiry
- **Enforcement Stage** — Current stage with color-coded badge
- **Violation** — Violation reason (only shown when in violation)

## Upgrade Section

When running on the built-in free license, an upgrade section shows how to install a paid license:

```
vigogo license install --file=license.lic
```

## Warning Banner

A yellow warning banner appears at the top of **every page** (not just the license page) when:

- Node usage reaches **90% or more** of the licensed maximum
- The license is in any enforcement stage above compliant (grace period, enrollment block)

The banner includes the warning message and a "View license" link to the license page.

## Related

- [License Concepts](../concepts/license.md) — Enforcement stages and grace periods
- [Prometheus Metrics](../operations/metrics.md) — License metrics for alerting
- [Server Configuration](../getting-started/server-config.md) — License directory path
