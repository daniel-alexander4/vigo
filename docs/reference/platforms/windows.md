
# Windows Support

Vigo fully supports Windows as a managed platform. The Rust agent runs as a native Windows Service with 22 Windows-specific executors, 13 trait collectors, and Windows-native `when:` builtins.

## Bootstrap

Install the agent on a Windows machine with a one-line PowerShell command:

```powershell
irm https://<server>:8443/bootstrap?os=windows | iex
```

This downloads the agent binary, generates keys, enrolls with the server, and registers a Windows Service that starts automatically on boot. The service is configured with failure recovery (restart after 60 seconds, up to 3 retries).

**Manual installation:**

```powershell
# Download agent binary
Invoke-WebRequest -Uri "https://<server>:8443/api/v1/agent/download/windows/amd64" -OutFile "C:\Program Files\vigo\vigo.exe"

# Register as Windows Service
sc.exe create vigo-envoy binPath= "\"C:\Program Files\vigo\vigo.exe\" --server <server>:1530 --data-dir C:\ProgramData\vigo" start= auto
sc.exe failure vigo-envoy reset= 86400 actions= restart/60000/restart/60000/restart/60000
sc.exe start vigo-envoy
```

Both amd64 and arm64 architectures are supported.

## Cross-Platform Configs

Vigo configs are portable — use the standard resource types and the agent dispatches to the correct Windows implementation automatically:

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

See [registry executor](../reference/executors/registry_windows.md) for full reference.

### firewall (Windows)

Manage Windows Firewall rules via `netsh advfirewall`.

```yaml
- name: allow-http
  type: firewall
  port: 80
  protocol: tcp
  action: allow
  direction: in
  # auto-generated rule name: vigo-80-tcp-in

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

See [firewall_windows executor](../reference/executors/firewall_windows.md) for full reference.

### cron (Windows)

Manage scheduled tasks via `schtasks.exe`. Accepts standard cron syntax — the agent translates it to Windows Task Scheduler equivalents.

```yaml
- name: daily-backup
  type: cron
  name: "Vigo Daily Backup"
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

See [cron_windows executor](../reference/executors/cron_windows.md) for full reference.

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

### windows_feature

Manage Windows Features (IIS, Hyper-V, .NET Framework). Automatically uses the correct cmdlets for Server and client SKUs.

```yaml
- name: install-iis
  type: windows_feature
  feature: IIS-WebServer
  include_management_tools: "true"

- name: install-dotnet45
  type: windows_feature
  feature: NET-Framework-45-Core

- name: install-hyperv
  type: windows_feature
  feature: Microsoft-Hyper-V-All

- name: remove-telnet
  type: windows_feature
  feature: TelnetClient
  state: absent
```

See [windows_feature executor](../reference/executors/windows_feature.md) for full reference.

### acl

Manage NTFS ACL permissions on files and directories via PowerShell `Get-Acl`/`Set-Acl`.

```yaml
- name: admin-full-control
  type: acl
  path: "C:\\AppData\\MyApp"
  identity: "BUILTIN\\Administrators"
  rights: FullControl

- name: web-read-execute
  type: acl
  path: "C:\\inetpub\\wwwroot\\mysite"
  identity: IIS_IUSRS
  rights: ReadAndExecute

- name: deny-guest-write
  type: acl
  path: "C:\\Shares\\Public"
  identity: "BUILTIN\\Guests"
  rights: Write
  type: Deny

- name: remove-old-access
  type: acl
  path: "C:\\Data\\Archive"
  identity: "DOMAIN\\OldTeam"
  rights: Modify
  state: absent
```

See [acl_windows executor](../reference/executors/acl_windows.md) for full reference.

### windows_env

Manage persistent environment variables via the .NET `[Environment]::SetEnvironmentVariable()` API.

```yaml
- name: set-java-home
  type: windows_env
  variable: JAVA_HOME
  value: "C:\\Program Files\\Java\\jdk-17"

- name: set-go-root
  type: windows_env
  variable: GOROOT
  value: "C:\\Go"

- name: set-node-env
  type: windows_env
  variable: NODE_ENV
  value: production

- name: remove-legacy-var
  type: windows_env
  variable: OLD_APP_HOME
  state: absent
```

**Scopes:** `Machine` (default, system-wide) or `User` (current user). Machine scope requires administrator privileges.

See [windows_env executor](../reference/executors/windows_env.md) for full reference.

### reboot_windows

Manage pending reboots. Checks well-known registry keys for pending reboot state before triggering a restart.

```yaml
# Reboot only if a reboot is pending (default)
- name: pending-reboot
  type: reboot_windows

# Reboot after feature install
- name: reboot-after-hyperv
  type: reboot_windows
  message: "Rebooting to complete Hyper-V installation"
  timeout: "120"
  depends_on: [install-hyperv]

# Forced reboot (always reboots)
- name: maintenance-reboot
  type: reboot_windows
  when_pending: "false"
  message: "Scheduled maintenance reboot"
  timeout: "30"
```

The executor checks `RebootPending`, `RebootRequired`, and `PendingFileRenameOperations` registry keys. If none are set, the resource is skipped.

See [reboot_windows executor](../reference/executors/reboot_windows.md) for full reference.

### powershell

Run PowerShell scripts with `$ErrorActionPreference = 'Stop'` and idempotency guards (`creates`, `unless`, `onlyif`).

```yaml
- name: init-database
  type: powershell
  command: "& C:\\Scripts\\init-db.ps1 -Server localhost -Database MyApp"
  creates: "C:\\ProgramData\\MyApp\\db-initialized.flag"

- name: import-cert
  type: powershell
  command: |
    Import-PfxCertificate -FilePath C:\certs\app.pfx `
      -CertStoreLocation Cert:\LocalMachine\My `
      -Password (ConvertTo-SecureString 'changeit' -AsPlainText -Force)
  unless: "Get-ChildItem Cert:\\LocalMachine\\My | Where-Object { $_.Subject -match 'CN=myapp' }"

- name: deploy-app
  type: powershell
  command: "& C:\\Scripts\\deploy.ps1"
  cwd: "C:\\Deployments\\myapp"
  environment: "APP_ENV=production,DB_HOST=sql01.internal"
  creates: "C:\\Deployments\\myapp\\deployed.flag"
```

Always use at least one guard (`creates`, `unless`, `onlyif`) to maintain idempotency.

See [powershell executor](../reference/executors/powershell.md) for full reference.

### iis_site

Manage IIS websites with bindings, application pools, and SSL certificates.

```yaml
- name: myapp-site
  type: iis_site
  site: MyApp
  physical_path: "C:\\inetpub\\wwwroot\\myapp"
  port: "8080"
  depends_on: [install-iis]

- name: secure-site
  type: iis_site
  site: SecureApp
  physical_path: "C:\\inetpub\\wwwroot\\secureapp"
  port: "443"
  protocol: https
  ssl_cert_thumbprint: "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2"
  host_header: app.example.com

- name: remove-old-site
  type: iis_site
  site: OldApp
  state: absent
```

Requires the IIS-WebServer feature. Application pools are auto-created using the site name (or `app_pool` if specified).

See [iis_site executor](../reference/executors/iis_site.md) for full reference.

### iis_app_pool

Manage IIS application pools with fine-grained control over .NET runtime, pipeline mode, process identity, idle timeout, and recycling.

```yaml
- name: api-pool
  type: iis_app_pool
  app_pool: ApiPool
  runtime_version: v4.0
  pipeline_mode: Integrated
  identity_type: NetworkService
  idle_timeout: "0"

- name: static-pool
  type: iis_app_pool
  app_pool: StaticPool
  runtime_version: ""
  recycle_interval: "0"

- name: custom-identity-pool
  type: iis_app_pool
  app_pool: AppPool
  identity_type: SpecificUser
  username: "DOMAIN\\svc-app"
  password: "secret:vigo/iis/pool-password"
```

**Identity types:** `ApplicationPoolIdentity` (default), `LocalSystem`, `LocalService`, `NetworkService`, `SpecificUser`.

See [iis_app_pool executor](../reference/executors/iis_app_pool.md) for full reference.

### certificate_windows

Manage certificates in Windows certificate stores. Import PFX/CER/CRT files and remove certificates by thumbprint.

```yaml
- name: import-ssl-cert
  type: certificate_windows
  source: "C:\\certs\\app.pfx"
  password: "secret:vigo/certs/app-password"
  thumbprint: "A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4E5F6A1B2"
  exportable: "true"

- name: import-ca
  type: certificate_windows
  source: "C:\\certs\\corporate-ca.cer"
  store: "Cert:\\LocalMachine\\Root"

- name: remove-expired
  type: certificate_windows
  thumbprint: "DEADBEEF1234567890DEADBEEF1234567890DEAD"
  state: absent
```

**Common stores:** `Cert:\LocalMachine\My` (default, personal), `Cert:\LocalMachine\Root` (trusted root CAs), `Cert:\LocalMachine\CA` (intermediate CAs).

See [certificate_windows executor](../reference/executors/certificate_windows.md) for full reference.

### domain_membership

Manage Active Directory domain join and leave operations.

```yaml
- name: join-domain
  type: domain_membership
  domain: corp.example.com
  username: "admin@corp.example.com"
  password: "secret:vigo/ad/join-password"
  ou_path: "OU=WebServers,DC=corp,DC=example,DC=com"

- name: leave-domain
  type: domain_membership
  domain: corp.example.com
  state: absent
  username: "admin@corp.example.com"
  password: "secret:vigo/ad/leave-password"
```

By default, the machine restarts after join/leave. Set `restart: "false"` to defer the reboot and use `reboot_windows` with `depends_on` for controlled restarts.

See [domain_membership executor](../reference/executors/domain_membership.md) for full reference.

### local_security_policy

Manage local security policies (passwords, lockout, auditing) via `secedit`.

```yaml
- name: min-password-length
  type: local_security_policy
  policy: MinimumPasswordLength
  value: "14"

- name: lockout-threshold
  type: local_security_policy
  policy: LockoutBadCount
  value: "5"

- name: audit-logon
  type: local_security_policy
  policy: AuditLogonEvents
  value: "3"
```

**Password policies:** `MinimumPasswordLength`, `MaximumPasswordAge`, `MinimumPasswordAge`, `PasswordComplexity`, `PasswordHistorySize`.
**Lockout policies:** `LockoutBadCount`, `LockoutDuration`, `ResetLockoutCount`.
**Audit policies:** `AuditLogonEvents`, `AuditObjectAccess`, `AuditPrivilegeUse`, `AuditPolicyChange`, `AuditAccountManage`.

See [local_security_policy executor](../reference/executors/local_security_policy.md) for full reference.

### windows_update

Manage Windows Update and WSUS configuration via registry policies.

```yaml
- name: configure-wsus
  type: windows_update
  wsus_server: "https://wsus.corp.example.com:8531"
  auto_update: enabled

- name: scheduled-updates
  type: windows_update
  auto_update: scheduled
  scheduled_day: "1"
  scheduled_time: "3"

- name: disable-auto-update
  type: windows_update
  auto_update: disabled

- name: remove-wsus-policy
  type: windows_update
  state: absent
```

**Auto-update modes:** `enabled` (auto-install), `disabled`, `notify` (notify before download), `download` (download only), `scheduled` (install on schedule).

See [windows_update executor](../reference/executors/windows_update.md) for full reference.

### windows_defender

Manage Windows Defender exclusions, real-time protection, and scheduled scan settings.

```yaml
- name: defender-exclusions
  type: windows_defender
  exclusion_path: "C:\\App,D:\\Database\\Data"
  exclusion_process: "sqlservr.exe"
  exclusion_extension: ".mdf,.ldf"

- name: defender-realtime
  type: windows_defender
  real_time_protection: "true"

- name: defender-scan
  type: windows_defender
  scan_schedule_day: "0"
  scan_schedule_time: "02:00"

- name: remove-exclusions
  type: windows_defender
  exclusion_path: "C:\\OldApp"
  state: absent
```

**Scan schedule days:** `0` = daily, `1` = Sunday, `2` = Monday, ..., `7` = Saturday.

See [windows_defender executor](../reference/executors/windows_defender.md) for full reference.

### smb_share

Manage SMB file shares with granular access control for users and groups.

```yaml
- name: data-share
  type: smb_share
  share: Data
  path: "D:\\Shares\\Data"
  description: "Shared data directory"
  full_access: "BUILTIN\\Administrators"
  read_access: "DOMAIN\\DataReaders"
  change_access: "DOMAIN\\DataWriters"

- name: public-share
  type: smb_share
  share: Public
  path: "D:\\Shares\\Public"
  read_access: Everyone
  no_access: "BUILTIN\\Guests"

- name: remove-share
  type: smb_share
  share: OldData
  state: absent
```

**Access levels:** `full_access` (full control), `change_access` (read + write), `read_access` (read only), `no_access` (deny). All accept comma-separated users/groups.

See [smb_share executor](../reference/executors/smb_share.md) for full reference.

### service_recovery_windows

Manage per-service failure recovery actions -- what happens when a Windows service crashes.

```yaml
- name: myapp-recovery
  type: service_recovery_windows
  service: MyAppService
  first_failure: restart
  restart_delay: "30000"

- name: critical-recovery
  type: service_recovery_windows
  service: CriticalService
  first_failure: restart
  second_failure: restart
  subsequent_failure: reboot
  reset_period: "3600"

- name: diag-on-failure
  type: service_recovery_windows
  service: SQLServer
  first_failure: run
  command: "C:\\Scripts\\diagnose-db.bat"
  second_failure: restart
```

**Failure actions:** `restart` (restart the service), `run` (execute a command), `reboot` (reboot the machine), `none` (take no action). The `run` action requires the `command` parameter.

See [service_recovery_windows executor](../reference/executors/service_recovery_windows.md) for full reference.

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
  target_path: "C:\\ProgramData\\MyApp\\config.ini"
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
target_path: C:\\ProgramData\\MyApp\\config.ini

# Single backslash in quoted string
target_path: "C:\\ProgramData\\MyApp\\config.ini"

# Forward slashes also work (Go/Rust normalize)
target_path: C:/ProgramData/MyApp/config.ini
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
    target_path: "C:\\inetpub\\wwwroot\\myapp"
    owner: IIS_IUSRS

  - name: web-config
    type: file
    target_path: "C:\\inetpub\\wwwroot\\myapp\\web.config"
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

- [Bootstrap](../getting-started/bootstrap.md) — Agent installation on all platforms
- [Executors index](../reference/executors/_index.md) — All executor reference docs
- [Traits index](../reference/traits/_index.md) — All trait collector reference docs
- [When Expressions](../concepts/when-expressions.md) — Conditional logic
