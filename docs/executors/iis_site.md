# iis_site (Windows)

Manages IIS web sites idempotently via the PowerShell WebAdministration module. Creates, removes, starts, and stops sites with application pool management.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `site` | Yes | -- | IIS site name (e.g., `Default Web Site`, `MyApp`). |
| `physical_path` | Yes (for create) | -- | Filesystem path to the site content directory. |
| `bindings` | No | `http:80:` | Comma-separated bindings in `protocol:port:hostname` format. |
| `app_pool` | No | -- | Application pool name. Created automatically if it doesn't exist. |
| `state` | No | `started` | `started`, `stopped`, or `absent`. |

## States

- **`started`** -- Ensure the site exists and is running. Creates the site if it doesn't exist.
- **`stopped`** -- Ensure the site exists but is stopped.
- **`absent`** -- Remove the site entirely.

## Prerequisites

The IIS-WebServer Windows Feature must be installed. Use the `windows_feature` executor with `depends_on`:

```yaml
resources:
  - name: install-iis
    type: windows_feature
    feature: IIS-WebServer
    include_management_tools: "true"

  - name: my-website
    type: iis_site
    site: MyApp
    physical_path: "C:\\inetpub\\wwwroot\\myapp"
    app_pool: MyAppPool
    depends_on: [install-iis]
```

## Examples

### Basic site

```yaml
resources:
  - name: company-site
    type: iis_site
    site: CompanyIntranet
    physical_path: "C:\\inetpub\\wwwroot\\intranet"
```

### Site with HTTPS binding and app pool

```yaml
resources:
  - name: web-app
    type: iis_site
    site: WebApp
    physical_path: "C:\\WebApps\\MyApp"
    bindings: "https:443:www.example.com"
    app_pool: WebAppPool
```

### Stop a site

```yaml
resources:
  - name: maintenance-mode
    type: iis_site
    site: WebApp
    state: stopped
```

### Remove a site

```yaml
resources:
  - name: decommission-old-site
    type: iis_site
    site: OldApp
    state: absent
```

### Full IIS stack with permissions

```yaml
resources:
  - name: install-iis
    type: windows_feature
    feature: IIS-WebServer
    include_management_tools: "true"

  - name: site-directory
    type: directory
    path: "C:\\WebApps\\MyApp"
    state: present
    depends_on: [install-iis]

  - name: site-acl
    type: acl
    path: "C:\\WebApps\\MyApp"
    identity: IIS_IUSRS
    rights: ReadAndExecute
    depends_on: [site-directory]

  - name: my-site
    type: iis_site
    site: MyApp
    physical_path: "C:\\WebApps\\MyApp"
    bindings: "http:80:,https:443:"
    app_pool: MyAppPool
    depends_on: [site-acl]
```

## Platform

Windows only. Requires IIS-WebServer feature and the WebAdministration PowerShell module.

## Notes

- The first binding in the `bindings` list is used when creating the site. Additional bindings can be added via PowerShell after creation.
- Application pools are created with default settings. For custom pool configuration, use a `powershell` resource.
- The executor checks site state via `Get-WebItemState` and creates via `New-WebSite`.
