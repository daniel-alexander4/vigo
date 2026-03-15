# Windows Support

Hobnob fully supports Windows as a managed platform. The Rust agent runs as a native Windows Service with 7 Windows-specific executors, 13 trait collectors, and Windows-native `when:` builtins.

## Bootstrap

Install the agent on a Windows machine with a one-line PowerShell command:

```powershell
irm http://<server>:8181/bootstrap?os=windows | iex
```

This downloads the agent binary, generates keys, enrolls with the server, and registers a Windows Service that starts automatically on boot. The service is configured with failure recovery (restart after 60 seconds, up to 3 retries).

**Manual installation:**

```powershell
# Download agent binary
Invoke-WebRequest -Uri "http://<server>:8181/api/v1/agent/download/windows/amd64" -OutFile "C:\Program Files\hobnob\hobnob.exe"

# Register as Windows Service
sc.exe create hobnob-thrall binPath= "\"C:\Program Files\hobnob\hobnob.exe\" --server <server>:1530 --data-dir C:\ProgramData\hobnob" start= auto
sc.exe failure hobnob-thrall reset= 86400 actions= restart/60000/restart/60000/restart/60000
sc.exe start hobnob-thrall
```

Both amd64 and arm64 architectures are supported.

## Cross-Platform Configs

Hobnob configs are portable — use the standard resource types and the agent dispatches to the correct Windows implementation automatically:

```yaml
resources:
  - name: install-nginx
    type: package        # → chocolatey/winget/scoop on Windows, apt/yum on Linux
    package: nginx

  - name: start-nginx
    type: service        # → sc.exe on Windows, systemctl on Linux
    service: nginx
    state: running
    enabled: true

  - name: deploy-user
    type: user           # → net user on Windows, useradd on Linux
    username: deploy
    groups: Administrators
```

No `when:` guards needed for basic resources — the agent handles the platform difference. Use `when:` only when the resource is truly platform-specific:

```yaml
resources:
  # Only on Windows
  - name: enable-rdp
    type: registry
    path: "HKLM:\\SYSTEM\\CurrentControlSet\\Control\\Terminal Server"
    name: fDenyTSConnections
    value: "0"
    type: dword
    when: "os_family('windows')"

  # Only on Linux
  - name: enable-sshd
    type: service
    service: sshd
    state: running
    when: "os_family('debian') || os_family('redhat')"
```

## Windows-Specific Executors

These executors are only available on Windows. Using them on Linux results in a skip.

### registry

Manage Windows Registry keys and values.

```yaml
- name: disable-telemetry
  type: registry
  path: "HKLM:\\SOFTWARE\\Policies\\Microsoft\\Windows\\DataCollection"
  name: AllowTelemetry
  value: "0"
  type: dword

- name: set-wallpaper
  type: registry
  path: "HKCU:\\Control Panel\\Desktop"
  name: Wallpaper
  value: "C:\\Windows\\Web\\Wallpaper\\corporate.jpg"

- name: create-app-key
  type: registry
  path: "HKLM:\\SOFTWARE\\MyApp"
  state: present   # creates the key if missing, no value

- name: remove-legacy-key
  type: registry
  path: "HKLM:\\SOFTWARE\\LegacyApp"
  state: absent
```

**Supported types:** `string`, `dword`, `qword`, `binary`, `multi_string`, `expand_string`

**Supported hives:** `HKLM:\`, `HKCU:\`, `HKCR:\`, `HKU:\`, `HKCC:\`

See [registry executor](../executors/registry_windows.md) for full reference.

### firewall (Windows)

Manage Windows Firewall rules via `netsh advfirewall`.

```yaml
- name: allow-http
  type: firewall
  port: 80
  protocol: tcp
  action: allow
  direction: in
  # auto-generated rule name: hobnob-80-tcp-in

- name: allow-custom-app
  type: firewall
  name: "MyApp Inbound"
  port: 8443
  protocol: tcp

- name: block-telnet
  type: firewall
  name: "Block Telnet"
  port: 23
  action: block
  direction: in

- name: remove-old-rule
  type: firewall
  name: "Legacy Rule"
  state: absent
```

See [firewall_windows executor](../executors/firewall_windows.md) for full reference.

### cron (Windows)

Manage scheduled tasks via `schtasks.exe`. Accepts standard cron syntax — the agent translates it to Windows Task Scheduler equivalents.

```yaml
- name: daily-backup
  type: cron
  name: "Hobnob Daily Backup"
  command: "C:\\Scripts\\backup.ps1"
  schedule: "0 2 * * *"    # 2 AM daily → DAILY /ST 02:00

- name: hourly-check
  type: cron
  name: "Health Check"
  command: "C:\\Scripts\\health.bat"
  schedule: "@hourly"       # → HOURLY

- name: every-five-min
  type: cron
  name: "Metrics Push"
  command: "C:\\Scripts\\push-metrics.ps1"
  schedule: "*/5 * * * *"  # → MINUTE /MO 5
```

**Cron → schtasks translation:**

| Cron | schtasks equivalent |
|------|-------------------|
| `@hourly` | `/SC HOURLY` |
| `@daily` | `/SC DAILY /ST 00:00` |
| `@weekly` | `/SC WEEKLY /D SUN /ST 00:00` |
| `@monthly` | `/SC MONTHLY /D 1 /ST 00:00` |
| `@reboot` | `/SC ONSTART` |
| `*/N * * * *` | `/SC MINUTE /MO N` |
| `0 */N * * *` | `/SC HOURLY /MO N /ST 00:00` |
| `0 H * * *` | `/SC DAILY /ST HH:00` |
| `0 H * * D` | `/SC WEEKLY /D D /ST HH:00` |
| `0 H D * *` | `/SC MONTHLY /D D /ST HH:00` |

See [cron_windows executor](../executors/cron_windows.md) for full reference.

### hostname (Windows)

Set the computer name via PowerShell.

```yaml
- name: set-hostname
  type: hostname
  hostname: WEB-PROD-01
```

Requires a reboot to take effect. The agent detects the pending rename and skips re-application until rebooted.

### timezone (Windows)

Set the system timezone. Uses Windows timezone names (not IANA).

```yaml
- name: set-timezone
  type: timezone
  timezone: "Pacific Standard Time"
```

Common Windows timezone names: `Eastern Standard Time`, `Central Standard Time`, `Mountain Standard Time`, `Pacific Standard Time`, `UTC`, `GMT Standard Time`, `Tokyo Standard Time`, `AUS Eastern Standard Time`.

## Windows Trait Collectors

The agent automatically collects 13 Windows-specific traits:

| Collector | Example traits |
|-----------|---------------|
| `os` | `os.family: windows`, `os.version: 10.0.22631`, `os.distribution: Windows 11 Pro` |
| `hardware` | `hardware.cpu: Intel Core i7-12700K`, `hardware.memory_mb: 32768` |
| `memory` | `memory.total_mb`, `memory.available_mb`, `memory.used_pct` |
| `swap` | `swap.total_mb`, `swap.used_mb` |
| `network` | `network.interfaces[].name`, `network.interfaces[].ipv4` |
| `filesystem` | `filesystem.mounts[].device`, `filesystem.mounts[].size_gb` |
| `mountpoints` | `mountpoints[].path`, `mountpoints[].fstype` |
| `packages` | `packages[].name`, `packages[].version` |
| `processors` | `processors.count`, `processors.model` |
| `identity` | `identity.hostname`, `identity.domain` |
| `timezone` | `timezone.name`, `timezone.offset` |
| `users` | `users[].name`, `users[].enabled` |
| `uptime` | `uptime.seconds`, `uptime.days` |

Use traits in `when:` expressions and templates:

```yaml
- name: high-memory-config
  type: file
  targetpath: "C:\\ProgramData\\MyApp\\config.ini"
  content: |
    max_memory={{ if ge .Traits.hardware.memory_mb 16384 }}8192{{ else }}2048{{ end }}
```

## Windows when: Builtins

All cross-platform builtins work on Windows, plus Windows-specific behavior:

| Builtin | Windows behavior |
|---------|-----------------|
| `os_family('windows')` | true on Windows |
| `process_running('nginx')` | Uses `tasklist /FI` (auto-appends `.exe`) |
| `in_group('Administrators')` | Uses `net localgroup` |
| `command_succeeds('...')` | Runs via `cmd /C` |
| `file_exists('C:\\path')` | Standard path check |
| `port_listening(80)` | Uses `netstat` |
| `hour_range(9,17)` | UTC-based on Windows |

## Windows Path Conventions

Windows configs use backslash paths. In YAML, either escape or quote:

```yaml
# Double backslash (escaped)
targetpath: C:\\ProgramData\\MyApp\\config.ini

# Single backslash in quoted string
targetpath: "C:\\ProgramData\\MyApp\\config.ini"

# Forward slashes also work (Go/Rust normalize)
targetpath: C:/ProgramData/MyApp/config.ini
```

## Example: Complete Windows Web Server Module

```yaml
name: iis-webserver

vars:
  site_name: Default Web Site
  site_port: 80
  app_pool: DefaultAppPool

resources:
  - name: install-iis
    type: package
    package: IIS-WebServer
    when: "os_family('windows')"

  - name: iis-service
    type: service
    service: W3SVC
    state: running
    enabled: true
    depends_on: [install-iis]

  - name: firewall-http
    type: firewall
    port: "{{ .Vars.site_port }}"
    protocol: tcp
    action: allow

  - name: app-directory
    type: directory
    targetpath: "C:\\inetpub\\wwwroot\\myapp"
    owner: IIS_IUSRS

  - name: web-config
    type: file
    targetpath: "C:\\inetpub\\wwwroot\\myapp\\web.config"
    content: |
      <?xml version="1.0" encoding="utf-8"?>
      <configuration>
        <system.webServer>
          <defaultDocument>
            <files>
              <add value="index.html" />
            </files>
          </defaultDocument>
        </system.webServer>
      </configuration>
    notify: [restart-iis]

  - name: restart-iis
    type: exec
    command: "iisreset /restart"
    refreshonly: true

  - name: disable-telemetry
    type: registry
    path: "HKLM:\\SOFTWARE\\Policies\\Microsoft\\Windows\\DataCollection"
    name: AllowTelemetry
    value: "0"
    type: dword

  - name: nightly-backup
    type: cron
    name: "IIS Backup"
    command: "C:\\Scripts\\backup-iis.ps1"
    schedule: "0 2 * * *"
```

## Related

- [Bootstrap](bootstrap.md) — Agent installation on all platforms
- [Executors index](../executors/_index.md) — All executor reference docs
- [Traits index](../traits/_index.md) — All trait collector reference docs
- [When Expressions](../concepts/when-expressions.md) — Conditional logic
