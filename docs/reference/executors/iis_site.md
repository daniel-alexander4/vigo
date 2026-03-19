# iis_site

Manages IIS websites idempotently via the PowerShell WebAdministration module. Creates sites with bindings and application pools, starts/stops existing sites, and removes sites that should be absent.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `site` | Yes | -- | Site name (e.g., `Default Web Site`, `MyApp`). |
| `state` | No | `started` | `started`, `stopped`, or `absent`. |
| `physical_path` | Conditional | -- | Root directory (e.g., `C:\inetpub\wwwroot\myapp`). Required when creating a new site. |
| `port` | No | `80` | Binding port number. |
| `protocol` | No | `http` | `http` or `https`. |
| `host_header` | No | -- | Host header for the binding (e.g., `app.example.com`). |
| `app_pool` | No | site name | Application pool to assign. Defaults to the site name. Auto-created if it does not exist. |
| `ssl_cert_thumbprint` | Conditional | -- | Certificate thumbprint for HTTPS bindings. Required when `protocol=https`. |

## States

- **`started`** -- Ensure the site exists and is running. Creates the site (and its app pool) if missing.
- **`stopped`** -- Ensure the site exists but is stopped. Creates the site if missing, then stops it.
- **`absent`** -- Remove the site entirely.

## Idempotency

The executor queries the site state via `Get-Website` before acting:

1. If the site does not exist and state is `started` or `stopped`, the site (and app pool) are created with `New-Website`/`New-WebAppPool`. HTTPS bindings are added when `protocol=https`.
2. If the site exists and is already in the desired state, no action is taken.
3. If the site exists but is in the wrong state, `Start-Website` or `Stop-Website` is called.
4. If state is `absent` and the site does not exist, no action is taken.

## Examples

### Basic HTTP site

```yaml
resources:
  - name: myapp-site
    type: iis_site
    site: MyApp
    physical_path: "C:\\inetpub\\wwwroot\\myapp"
    port: "8080"
```

### HTTPS site with certificate

```yaml
resources:
  - name: secure-site
    type: iis_site
    site: SecureApp
    physical_path: "C:\\inetpub\\wwwroot\\secureapp"
    port: "443"
    protocol: https
    ssl_cert_thumbprint: "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2"
    host_header: app.example.com
```

### Site with custom app pool

```yaml
resources:
  - name: api-site
    type: iis_site
    site: ApiSite
    physical_path: "C:\\inetpub\\wwwroot\\api"
    app_pool: ApiPool
    depends_on: [install-iis]
```

### Stop a site for maintenance

```yaml
resources:
  - name: stop-legacy
    type: iis_site
    site: LegacySite
    state: stopped
```

### Remove a decommissioned site

```yaml
resources:
  - name: remove-old-site
    type: iis_site
    site: OldApp
    state: absent
```

## Platform

Windows only. Requires the IIS-WebServer feature to be installed. Use the [`windows_feature`](windows_feature.md) executor with `depends_on` to ensure it:

```yaml
resources:
  - name: install-iis
    type: windows_feature
    feature: IIS-WebServer
    include_management_tools: "true"

  - name: myapp-site
    type: iis_site
    site: MyApp
    physical_path: "C:\\inetpub\\wwwroot\\myapp"
    depends_on: [install-iis]
```

## Notes

- The WebAdministration PowerShell module is imported automatically on each invocation.
- When creating a site, the application pool is auto-created if it does not already exist.
- For fine-grained app pool configuration (runtime version, pipeline mode, identity), use the [`iis_app_pool`](iis_app_pool.md) executor.
- HTTPS bindings are added to `0.0.0.0` (all interfaces) by default. The certificate must exist in `Cert:\LocalMachine\My`.
