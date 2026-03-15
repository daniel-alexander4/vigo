# License

Hobnob includes a built-in free license for up to 25 thralls. Beyond that, a license file is required. Enforcement is gradual — operations are never abruptly disrupted.

## Licensing Model

Hobnob uses a split-license model:

- **Agent** (`hobnob`): Source-available under the Elastic License 2.0. You can read, audit, and build the agent source code — critical since it runs as root on managed machines.
- **Server** (`hobd`) and **CLI** (`hobnobcli`): Proprietary. Distributed as compiled binaries and container images.

## Pricing

Pricing is per managed node, per year. Tiers are cumulative — each band prices only the nodes in that band.

| Tier        | Nodes            | Per Node / Year |
|-------------|------------------|-----------------|
| Community   | 1 – 25           | Free            |
| Starter     | 26 – 250         | $84             |
| Growth      | 251 – 1,000      | $74             |
| Business    | 1,001 – 5,000    | $64             |
| Enterprise  | 5,001 – 25,000   | $54             |
| Scale       | 25,001 – 100,000 | $41             |
| Unlimited   | 100,001+         | $31             |

All features are included at every tier — there are no enterprise-only features.

### Loyalty Discount

After year 5 of continuous renewal, per-node rates decrease automatically: 10% at year 6, increasing to a floor of 50% at year 11+. See [COMMERCIAL-TERMS.md](https://github.com/hobnob/hobnob/blob/main/COMMERCIAL-TERMS.md) for the full schedule.

## Free Tier

Every Hobnob server starts with a built-in free license:
- **25 thralls** included
- No license file needed
- No expiration
- All features except [federation](federation.md) — hub/spoke mode requires a paid license

## Enforcement Stages

When the thrall count exceeds the license limit, enforcement proceeds through three stages over 30 days:

![License Enforcement Timeline](img/license-enforcement.svg)

### Stage 1: Grace (days 0–15)

- Existing thralls continue to check in normally
- A warning is logged on every check-in
- New thralls can still enroll
- **Grace clock resets immediately** when returning to compliance (thrall count drops below limit)

### Stage 2: Enrollment Block (days 15–30)

- Existing thralls continue to check in normally
- New thralls **cannot enroll** — the Register RPC returns an error
- Warning continues on every check-in

### Stage 3: Service Stop (day 30)

- The server shuts down
- Agents fall back to offline convergence using cached bundles

## Checking License Status

### CLI

```bash
hobnobcli license status
```

Output:

```
License ID:    built-in-free-25
Max Thralls:   25
Current:       12
Stage:         compliant
Expires:       never
```

### Web UI

The [license page](../ui/license.md) at `/license` shows live status, node usage, and enforcement stage. A yellow warning banner appears on all pages when usage reaches 90% of the licensed maximum or when in an enforcement stage.

### Prometheus Metrics

Five license metrics are exposed at `GET /metrics`:

| Metric | Description |
|--------|-------------|
| `hobnob_license_thralls_used` | Current active (non-revoked) thralls |
| `hobnob_license_thralls_max` | Maximum allowed by the license |
| `hobnob_license_stage` | Enforcement stage (0–3) |
| `hobnob_license_days_remaining` | Days until expiry (negative if expired) |
| `hobnob_license_expiry_timestamp` | Unix timestamp of expiration |

Suggested alerts:
- `hobnob_license_stage > 0` — any enforcement violation
- `hobnob_license_thralls_used / hobnob_license_thralls_max > 0.9` — approaching node limit
- `hobnob_license_days_remaining < 30` — license expiring soon

## License Reload

Reload the license without restarting the server:

```bash
kill -HUP $(pidof hobd)
```

The server handles SIGHUP by re-reading the license file from the license directory.

## License File Location

Configure in `server.yaml`:

```yaml
paths:
  license_dir: "/srv/hobnob/license"
```

Default: `/srv/hobnob/license`. Place the license file at `<license_dir>/license.json`.

## Related

- [License UI Page](../ui/license.md) — Web dashboard license view
- [Prometheus Metrics](../operations/metrics.md) — License and server metrics
- [Server Configuration](../getting-started/server-config.md) — Environment variables
- [Troubleshooting](../operations/troubleshooting.md)
