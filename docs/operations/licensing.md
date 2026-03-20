
# Licensing

Vigo uses a built-in licensing system with a free tier and paid tiers. Licenses govern maximum envoy counts and feature access.

## Tiers

| Tier | Max Envoys | Spanner | Support |
|------|-------------|---------|---------|
| Free | 25 | No | Community |
| Standard | 100 | No | Email |
| Growth | 500 | Yes | Priority |
| Enterprise | Unlimited | Yes | Dedicated |

## Machine Fingerprint Binding

Paid licenses are bound to a specific server installation. The fingerprint is derived from machine-specific identifiers, preventing license sharing across servers. If you migrate to a new machine, you will need a new license.

Generate your server's fingerprint:

```bash
vigogo license fingerprint
```

## Enforcement Stages

Licenses are machine-bound. Exceeding your node limit triggers a grace period followed by enforcement.

When a license violation is detected (e.g., envoy count exceeds the license limit, license expired, or fingerprint mismatch), enforcement proceeds through three stages:

1. **Grace Period** — Full functionality. Warning logged on every check-in. Grace clock resets immediately when returning to compliance.
2. **Enrollment Block** — New envoy enrollments are rejected. Existing envoys continue to function normally and are never locked out.
3. **Service Stop** — Server refuses to start.

## CLI Commands

```bash
vigogo license status          # View current license status
vigogo license install <file>  # Install a license file
vigogo license inspect [file]  # Inspect a license file without installing
vigogo license fingerprint     # Display machine fingerprint
```

## API

```
GET /api/v1/license
```

Returns current license status including stage, tier, envoy counts, and expiration. See [REST API](../reference/api/rest.md#license).
