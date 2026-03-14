# License

Hobnob includes a built-in free license for up to 25 thralls. Beyond that, a license file is required. Enforcement is gradual — operations are never abruptly disrupted.

## Free Tier

Every Hobnob server starts with a built-in free license:
- **25 thralls** included
- No license file needed
- No expiration
- All features except [federation](federation.md) — hub/spoke mode requires a paid license

## Enforcement Stages

When the thrall count exceeds the license limit, enforcement proceeds through three stages over 30 days:

```
Day 0                Day 15               Day 30
│                    │                    │
│  ┌──────────────┐  │  ┌──────────────┐ │  ┌──────────────┐
│  │   GRACE      │  │  │  ENROLLMENT  │ │  │   SERVICE    │
│  │              │  │  │  BLOCK       │ │  │   STOP       │
│  │ Check-in     │  │  │              │ │  │              │
│  │ warning on   │  │  │ New thralls  │ │  │ Server shuts │
│  │ every check-in│  │  │ can't enroll │ │  │ down         │
│  │              │  │  │              │ │  │              │
│  │ Existing     │  │  │ Existing     │ │  │              │
│  │ thralls OK   │  │  │ thralls OK   │ │  │              │
│  └──────────────┘  │  └──────────────┘ │  └──────────────┘
```

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

## License Reload

Reload the license without restarting the server:

```bash
kill -HUP $(pidof hobd)
```

The server handles SIGHUP by re-reading the license file from the license directory.

## License File Location

| Variable | Default |
|----------|---------|
| `HOBNOB_LICENSE_DIR` | `/etc/hobnob-server/license` |

Place the license file at `$HOBNOB_LICENSE_DIR/license.json`.

## Related

- [Server Configuration](../getting-started/server-config.md) — Environment variables
- [Troubleshooting](../operations/troubleshooting.md)
