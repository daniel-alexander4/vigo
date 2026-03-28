
# Troubleshooting

## Quick Diagnostics

Check last results:

```bash
connwaer status
```

Run a single capability interactively with debug logging:

```bash
connwaer --log-level debug verify time_sync
```

Run all capabilities once with trace logging:

```bash
connwaer --log-level trace once
```

## Common Errors

### "schedule is required"

Every capability entry must have a `schedule` field. An empty or missing schedule causes a startup error.

```yaml
capabilities:
  time_sync:
    enabled: true
    schedule: "@daily"    # required
```

### "depends_on 'X' not found in capabilities"

A capability declares a dependency on another capability that is not present in the config. Add the dependency or remove the `depends_on` entry.

### "dependency cycle detected"

Two or more capabilities form a circular dependency chain. Break the cycle by removing one `depends_on` reference.

### "unknown capability: X"

The `verify` or `run` command encountered a capability name not in the built-in list. Check spelling against the [Capability Index](capabilities/_index.md).

## Capability Returns "skipped"

A capability returns `skipped` when:

- It is not supported on the current platform (e.g., `app_allowlisting` on a platform without fapolicyd or AppLocker)
- A required parameter is not configured (e.g., `hsm_lifecycle` without `pkcs11_library`)
- A prerequisite tool is not installed

The `summary` field in the result explains why it was skipped.

## License Warnings


---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*

---
Connwaer warns but does not block on license issues:

- **"license expired"** -- the license file has passed its expiration date. Connwaer runs in evaluation mode.
- **"does not include the 'connwaer' feature"** -- the license file is valid but does not authorize Connwaer. Connwaer runs in evaluation mode.
- **"no valid license found"** -- no license file in the configured directory. Connwaer runs in evaluation mode.

In all cases, capabilities still execute. Licensing enforcement is for commercial compliance, not functional gating.

## Result Files

Results are written atomically (temp file + rename) to `<results_dir>/<capability>.json`. If a result file is missing:

- Check that `results_dir` exists and is writable by the Connwaer process
- Check the Connwaer logs for write errors
- Verify the capability actually ran (check `connwaer status` or journal logs)

## Trends Database

If the trends database cannot be opened, Connwaer logs a warning but continues. Capabilities run normally -- only trend recording is affected.

To inspect the database manually:

```bash
sqlite3 /var/lib/connwaer/trends.db ".tables"
sqlite3 /var/lib/connwaer/trends.db "SELECT * FROM runs ORDER BY timestamp DESC LIMIT 10"
```

## Platform-Specific Issues

### Linux: NTP Service Detection

The `time_sync` capability auto-detects the active NTP service by checking `chronyd`, `ntpd`, and `systemd-timesyncd` via systemctl. If none are found, it returns `fail`. Ensure at least one NTP service is installed and running.

### Linux: AIDE Database

The `integrity_monitoring` capability checks for AIDE databases at `/var/lib/aide/aide.db`, `/var/lib/aide/aide.db.gz`, and `/var/lib/aide.db`. If AIDE is installed but the database has not been initialized, run `aide --init` to create the baseline.

### Linux: fapolicyd

The `app_allowlisting` capability requires fapolicyd to be installed and running. If fapolicyd is not present, the capability returns `skipped`.

### Windows: Service Permissions

Some capabilities query Windows services via `sc.exe` or WMI. Ensure the Connwaer service runs with sufficient privileges (Local System by default from `install-service`).
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
