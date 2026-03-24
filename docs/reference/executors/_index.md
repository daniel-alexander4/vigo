
# Executor Reference

Vigo ships 72 built-in resource types that manage system resources idempotently across seven supported platforms (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows) plus network devices (Cisco IOS/NX-OS/IOS-XR, Arista EOS, Juniper Junos). Each executor checks the current state of a resource before acting and only modifies the system when drift is detected.

## How Executors Work

Every resource in a `.vgo` config declares a `type:` field that maps to an executor. The agent calls the executor with the resource's parameters and a `dry_run` flag. The executor returns one of five actions:

| Action | Meaning |
|--------|---------|
| `None` | Resource is already in the desired state. No changes made. |
| `Created` | Resource did not exist and was created. |
| `Updated` | Resource existed but was out of date and was modified. |
| `Deleted` | Resource existed and was removed. |
| `Skipped` | Resource was skipped due to a guard condition. |

## Cross-Platform Executors (24)

These executors compile and run on all seven supported platforms (Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows).

| Type | Description | Doc |
|------|-------------|-----|
| `file` | Manage file content, mode, and ownership. Supports `stream_edit` transform pipelines. | [file](file.md) |
| `directory` | Create and remove directories | [directory](directory.md) |
| `symlink` | Manage symbolic and hard links | [symlink](symlink.md) |
| `exec` | Run arbitrary commands with guards | [exec](exec.md) |
| `process` | Manage running processes | [process](process.md) |
| `file_line` | Manage individual lines in files | [file_line](file_line.md) |
| `blockinfile` | Manage marked blocks in files | [blockinfile](blockinfile.md) |
| `replace` | Regex find-and-replace in files | [replace](replace.md) |
| `field_edit` | Edit fields in columnar/tabular files | [field_edit](field_edit.md) |
| `ini` | Manage INI file key-value pairs | [ini](ini.md) |
| `json_file` | Manage keys in JSON files | [json_file](json_file.md) |
| `host` | Manage /etc/hosts entries | [host](host.md) |
| `sshkey` | Manage SSH known host keys | [sshkey](sshkey.md) |
| `archive` | Download and extract archives | [archive](archive.md) |
| `repository` | Manage APT/DNF/YUM package repositories | [repository](repository.md) |
| `git` | Clone and update git repositories | [git](git.md) |
| `pip` | Manage Python packages via pip | [pip](pip.md) |
| `gem` | Manage Ruby gems | [gem](gem.md) |
| `npm` | Manage Node.js packages via npm | [npm](npm.md) |
| `docker_compose` | Manage Docker Compose stacks | [docker_compose](docker_compose.md) |
| `docker_container` | Manage Docker containers | [docker_container](docker_container.md) |
| `docker_image` | Manage Docker images | [docker_image](docker_image.md) |
| `reboot` | Trigger or cancel system reboots | [reboot](reboot.md) |
| `custom` | Run external scripts via JSON protocol | [custom](custom.md) |

## Unix Executors (12)

These executors manage OS-level resources across Unix platforms. Each type uses a platform-native backend so that `.vgo` configs are portable -- you write `type: service` and the agent dispatches to the correct implementation for the host OS.

| Type | Description | Linux | macOS | FreeBSD | OpenBSD | NetBSD | illumos | Doc |
|------|-------------|:-----:|:-----:|:-------:|:-------:|:------:|:-------:|-----|
| `service` | Manage system services | systemd | launchctl | rc.d | rcctl | -- | SMF | [service](service.md) |
| `package` | Install, upgrade, and remove OS packages | apt/dnf/yum | brew | pkg | pkg_add | pkgin | IPS | [package](package.md) |
| `user` | Manage local user accounts | useradd | dscl | pw | useradd | useradd | useradd | [user](user.md) |
| `group` | Manage local groups | groupadd | dscl | pw | groupadd | groupadd | groupadd | [group](group.md) |
| `cron` | Manage cron jobs | /etc/cron.d | crontab | crontab | crontab | crontab | crontab | [cron](cron.md) |
| `hostname` | Set the system hostname | hostnamectl | scutil | hostname | hostname | hostname | /etc/nodename | [hostname](hostname.md) |
| `timezone` | Set the system timezone | timedatectl | systemsetup | shared | shared | shared | /etc/default/init | [timezone](timezone.md) |
| `firewall` | Manage firewall rules | UFW | pf | pf | pf | pf | -- | [firewall](firewall.md) |
| `sysctl` | Manage kernel parameters | sysctl | sysctl | sysctl | sysctl | sysctl | -- | [sysctl](sysctl.md) |
| `mount` | Manage mount points and fstab | mount/fstab | mount/fstab | mount/fstab | mount/fstab | mount/fstab | -- | [mount](mount.md) |
| `swap` | Manage swap files/devices | swapon | shared | shared | shared | shared | shared | [swap](swap.md) |
| `kernel_module` | Load and unload kernel modules | modprobe | -- | kld | -- | -- | -- | [kernel_module](kernel_module.md) |

A "--" means the executor is not available on that platform.

## Linux-Only Executors (5)

These executors use Linux-specific tools with no equivalent on other platforms.

| Type | Description | Doc |
|------|-------------|-----|
| `tmpfile` | Manage systemd-tmpfiles configs | [tmpfile](tmpfile.md) |
| `logrotate` | Manage logrotate configs | [logrotate](logrotate.md) |
| `alternatives` | Manage update-alternatives entries | [alternatives](alternatives.md) |
| `selinux` | Manage SELinux file contexts | [selinux](selinux.md) |
| `systemd_dropin` | Manage systemd unit drop-in override files | [systemd_dropin](systemd_dropin.md) |

## Windows-Only Executors (22)

These executors replace their Unix counterparts on Windows or manage Windows-specific resources. Cross-platform types (`service`, `package`, etc.) are registered under the same `type:` name so configs are portable across platforms.

| Type | Description | Doc |
|------|-------------|-----|
| `service` (Windows) | Manage Windows services via sc.exe | [service_windows](service_windows.md) |
| `package` (Windows) | Manage packages via Chocolatey/winget/Scoop | [package_windows](package_windows.md) |
| `user` (Windows) | Manage local Windows users | [user_windows](user_windows.md) |
| `firewall` (Windows) | Manage Windows Firewall rules via netsh | [firewall_windows](firewall_windows.md) |
| `cron` (Windows) | Manage Windows scheduled tasks | [cron_windows](cron_windows.md) |
| `hostname` (Windows) | Set the Windows hostname | [hostname_windows](hostname_windows.md) |
| `timezone` (Windows) | Set the Windows timezone | [timezone_windows](timezone_windows.md) |
| `registry` | Manage Windows Registry keys and values | [registry_windows](registry_windows.md) |
| `acl` | Manage Windows file/directory ACLs via Get-Acl/Set-Acl | [acl_windows](acl_windows.md) |
| `windows_feature` | Manage Windows Features (IIS, Hyper-V, .NET) | [windows_feature](windows_feature.md) |
| `windows_env` | Manage persistent Windows environment variables | [windows_env](windows_env.md) |
| `reboot_windows` | Manage pending reboots and trigger reboots | [reboot_windows](reboot_windows.md) |
| `powershell` | Run PowerShell scripts with error handling and guards | [powershell](powershell.md) |
| `iis_site` | Manage IIS web sites, bindings, and SSL | [iis_site](iis_site.md) |
| `iis_app_pool` | Manage IIS application pools (runtime, identity, recycling) | [iis_app_pool](iis_app_pool.md) |
| `certificate_windows` | Manage certificates in Windows certificate stores | [certificate_windows](certificate_windows.md) |
| `domain_membership` | Manage Active Directory domain join/leave | [domain_membership](domain_membership.md) |
| `local_security_policy` | Manage local security policies via secedit | [local_security_policy](local_security_policy.md) |
| `windows_update` | Manage Windows Update / WSUS configuration | [windows_update](windows_update.md) |
| `windows_defender` | Manage Windows Defender exclusions, real-time protection, and scan schedule | [windows_defender](windows_defender.md) |
| `smb_share` | Manage SMB file shares with access control | [smb_share](smb_share.md) |
| `service_recovery_windows` | Manage per-service failure recovery actions | [service_recovery_windows](service_recovery_windows.md) |

## Network Device Executors (16)

These executors manage network devices (switches, routers) over SSH via the [gateway proxy](../../concepts/network-devices.md) pattern. They run on the gateway envoy and connect to the target device. SSH connection parameters are auto-injected. Supported device types: `cisco_ios`, `cisco_nxos`, `cisco_iosxr`, `arista_eos`, `juniper_junos`.

### Transport Executors (2)

| Type | Description | Doc |
|------|-------------|-----|
| `ssh_exec` | Run commands on remote devices over SSH | [ssh_exec](ssh_exec.md) |
| `network_config` | Declarative config-block management | [network_config](network_config.md) |

### Cisco Executors (3)

| Type | Description | Doc |
|------|-------------|-----|
| `cisco_vlan` | Manage VLANs on Cisco IOS/NX-OS | [cisco_vlan](cisco_vlan.md) |
| `cisco_interface` | Manage Cisco interface config | [cisco_interface](cisco_interface.md) |
| `cisco_acl` | Manage Cisco access control lists | [cisco_acl](cisco_acl.md) |

### Juniper Executors (1)

| Type | Description | Doc |
|------|-------------|-----|
| `junos_interface` | Manage Junos interface config | [junos_interface](junos_interface.md) |

### Cross-Platform Network Executors (10)

| Type | Description | Cisco IOS | NX-OS | IOS-XR | EOS | Junos | Doc |
|------|-------------|:---------:|:-----:|:------:|:---:|:-----:|-----|
| `network_ntp` | NTP server config | Yes | Yes | -- | -- | Yes | [network_ntp](network_ntp.md) |
| `network_syslog` | Syslog server config | Yes | Yes | -- | -- | Yes | [network_syslog](network_syslog.md) |
| `network_snmp` | SNMP community config | Yes | Yes | -- | -- | Yes | [network_snmp](network_snmp.md) |
| `network_user` | Local user management | Yes | Yes | -- | -- | Yes | [network_user](network_user.md) |
| `network_banner` | Login/MOTD banners | Yes | Yes | -- | -- | Yes | [network_banner](network_banner.md) |
| `network_interface` | Interface state and description | Yes | Yes | Yes | Yes | Yes | [network_interface](network_interface.md) |
| `network_vlan` | VLAN management | Yes | Yes | Yes | Yes | Yes | [network_vlan](network_vlan.md) |
| `network_acl` | Access control list entries | Yes | Yes | Yes | Yes | Yes | [network_acl](network_acl.md) |
| `network_static_route` | Static route management | Yes | Yes | Yes | Yes | Yes | [network_static_route](network_static_route.md) |
| `network_dns` | DNS server and domain config | Yes | Yes | Yes | Yes | Yes | [network_dns](network_dns.md) |

## Common Patterns

### State Parameter

Most executors accept a `state` parameter that defaults to `present`. The two universal states are:

- **`present`** -- Ensure the resource exists with the desired configuration.
- **`absent`** -- Ensure the resource does not exist.

Some executors support additional states (e.g., `running`, `stopped`, `mounted`, `latest`).

### Idempotency

Every executor follows the check-before-act pattern:

1. Read current system state.
2. Compare against desired state.
3. If they match, return `Action::None` with `changed: false`.
4. If they differ, apply the change and return the appropriate action.

### Dry Run

When `dry_run: true`, executors determine what *would* change but skip all mutating operations. The returned `action` and `changed` fields reflect the intended change.

### Resource Ordering

Use `depends_on` to create ordering dependencies between resources. Use `notify` and `subscribes` to trigger re-application when a dependency changes.

```yaml
resources:
  - name: /etc/nginx/nginx.conf
    type: file
    target_path: /etc/nginx/nginx.conf
    content: |
      worker_processes auto;
    notify: restart-nginx

  - name: restart-nginx
    type: service
    service: nginx
    state: restarted
    subscribes: /etc/nginx/nginx.conf
```

### Conditional Execution

Use `when:` expressions to conditionally apply resources:

```yaml
resources:
  - name: Install apt-transport-https
    type: package
    package: apt-transport-https
    when: "os_family('debian')"
```
