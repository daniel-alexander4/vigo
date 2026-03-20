
# windows_feature

Manages Windows Features (IIS, Hyper-V, .NET Framework, RSAT, etc.) idempotently via PowerShell. Automatically detects whether the host is a Server or client/workstation SKU and uses the appropriate cmdlets.

## Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `feature` | Yes | -- | Feature name (e.g., `IIS-WebServer`, `NET-Framework-45-Core`, `Hyper-V`). |
| `state` | No | `present` | `present` to install the feature, `absent` to remove it. |
| `include_management_tools` | No | `false` | Set to `true` to include management tools (Server SKU only, maps to `-IncludeManagementTools`). |

## States

- **`present`** -- Install the feature if not already installed.
- **`absent`** -- Remove the feature if currently installed.

## Idempotency

The executor queries feature state before acting:

- **Server SKU:** Uses `Get-WindowsFeature` to check, `Install-WindowsFeature`/`Remove-WindowsFeature` to change.
- **Client/Workstation SKU:** Falls back to `Get-WindowsOptionalFeature -Online` to check, `Enable-WindowsOptionalFeature`/`Disable-WindowsOptionalFeature` to change.

If the feature is already in the desired state, no action is taken.

## Examples

### Install IIS with management tools

```yaml
resources:
  - name: install-iis
    type: windows_feature
    feature: IIS-WebServer
    include_management_tools: "true"
```

### Install .NET Framework 4.5

```yaml
resources:
  - name: install-dotnet45
    type: windows_feature
    feature: NET-Framework-45-Core
```

### Install Hyper-V

```yaml
resources:
  - name: install-hyperv
    type: windows_feature
    feature: Microsoft-Hyper-V-All
```

### Remove a feature

```yaml
resources:
  - name: remove-telnet
    type: windows_feature
    feature: TelnetClient
    state: absent
```

### Web server role with dependencies

```yaml
resources:
  - name: install-iis
    type: windows_feature
    feature: IIS-WebServer
    include_management_tools: "true"

  - name: install-asp-net
    type: windows_feature
    feature: IIS-ASPNET45
    depends_on: [install-iis]

  - name: iis-service
    type: service
    service: W3SVC
    state: running
    enabled: true
    depends_on: [install-iis]
```

## Platform

Windows only.

## Notes

- Feature names are case-insensitive but must match the Windows feature catalog exactly. Use `Get-WindowsFeature` (Server) or `Get-WindowsOptionalFeature -Online` (client) to list available features.
- On client SKUs, `include_management_tools` is ignored since `Enable-WindowsOptionalFeature` does not support that flag.
- Installing or removing features may require a reboot. Use the [`reboot_windows`](reboot_windows.md) executor with `when_pending: "true"` to handle this.
- The `-NoRestart` flag is passed on client SKUs to prevent automatic reboots during convergence.
