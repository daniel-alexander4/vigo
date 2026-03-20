
# iis_app_pool

Manages IIS application pools idempotently via the PowerShell WebAdministration module. Controls .NET runtime version, pipeline mode, process identity, idle timeout, and recycling schedule.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `app_pool` | Yes | -- | Application pool name. |
| `state` | No | `started` | `started`, `stopped`, or `absent`. |
| `runtime_version` | No | -- | .NET CLR version (e.g., `v4.0`, `v2.0`, empty string `""` for No Managed Code). |
| `pipeline_mode` | No | `Integrated` | `Integrated` or `Classic`. |
| `identity_type` | No | `ApplicationPoolIdentity` | Process identity. One of: `ApplicationPoolIdentity`, `LocalSystem`, `LocalService`, `NetworkService`, `SpecificUser`. |
| `username` | Conditional | -- | Username when `identity_type=SpecificUser`. Required with `SpecificUser`. |
| `password` | Conditional | -- | Password when `identity_type=SpecificUser`. Required with `SpecificUser`. Use `secret:` prefix. |
| `idle_timeout_mins` | No | `20` | Minutes before idle shutdown. `0` disables the timeout. |
| `recycle_interval_mins` | No | `1740` | Minutes between periodic recycling (1740 = 29 hours). `0` disables periodic recycling. |

## States

- **`started`** -- Ensure the pool exists and is running. Creates and configures it if missing.
- **`stopped`** -- Ensure the pool exists but is stopped. Creates and configures it if missing, then stops it.
- **`absent`** -- Stop and remove the pool entirely.

## Idempotency

The executor queries pool state via `Get-WebAppPoolState` before acting:

1. If the pool does not exist and state is `started` or `stopped`, it is created with `New-WebAppPool` and all properties are configured.
2. If the pool exists and is already in the desired state, no action is taken.
3. If the pool exists but is in the wrong state, `Start-WebAppPool` or `Stop-WebAppPool` is called.
4. If state is `absent` and the pool does not exist, no action is taken.

## Examples

### Basic app pool with .NET 4.0

```yaml
resources:
  - name: myapp-pool
    type: iis_app_pool
    app_pool: MyAppPool
    runtime_version: v4.0
```

### No Managed Code pool (for static content or reverse proxies)

```yaml
resources:
  - name: static-pool
    type: iis_app_pool
    app_pool: StaticContentPool
    runtime_version: ""
    idle_timeout_mins: "0"
```

### Pool with custom identity

```yaml
resources:
  - name: api-pool
    type: iis_app_pool
    app_pool: ApiPool
    runtime_version: v4.0
    identity_type: SpecificUser
    username: "DOMAIN\\svc-api"
    password: "secret:vigo/iis/api-pool-password"
    recycle_interval_mins: "0"
```

### Pool running as NetworkService with Classic pipeline

```yaml
resources:
  - name: legacy-pool
    type: iis_app_pool
    app_pool: LegacyPool
    runtime_version: v2.0
    pipeline_mode: Classic
    identity_type: NetworkService
```

### Remove a pool

```yaml
resources:
  - name: remove-old-pool
    type: iis_app_pool
    app_pool: OldPool
    state: absent
```

## Platform

Windows only. Requires the IIS-WebServer feature to be installed. Use the [`windows_feature`](windows_feature.md) executor with `depends_on` to ensure it.

## Notes

- The WebAdministration PowerShell module is imported automatically on each invocation.
- When `identity_type=SpecificUser`, both `username` and `password` are required. Use the `secret:` prefix to avoid plaintext passwords in config.
- The default `recycle_interval_mins` of 1740 minutes (29 hours) is the IIS default. This avoids recycling at the same time every day. Set to `0` for long-lived pools that should not recycle.
- For simple sites where you do not need fine-grained pool control, the [`iis_site`](iis_site.md) executor auto-creates a pool using the site name.
