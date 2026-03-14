# Executor Reference

Hobnob ships 45 built-in executors that manage system resources idempotently across six supported platforms: Linux, macOS, FreeBSD, OpenBSD, NetBSD, and Windows. Each executor checks the current state of a resource before acting and only modifies the system when drift is detected.

## How Executors Work

Every resource in a `.hnb` config declares a `type:` field that maps to an executor. The agent calls the executor with the resource's parameters and a `dry_run` flag. The executor returns one of five actions:

| Action | Meaning |
|--------|---------|
| `None` | Resource is already in the desired state. No changes made. |
| `Created` | Resource did not exist and was created. |
| `Updated` | Resource existed but was out of date and was modified. |
| `Deleted` | Resource existed and was removed. |
| `Skipped` | Resource was skipped due to a guard condition. |

## Cross-Platform Executors (23)

These executors compile and run on all six supported platforms (Linux, macOS, FreeBSD, OpenBSD, NetBSD, Windows).

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
| `docker_container` | Manage Docker containers | [docker_container](docker_container.md) |
| `docker_image` | Manage Docker images | [docker_image](docker_image.md) |
| `reboot` | Trigger or cancel system reboots | [reboot](reboot.md) |
| `custom` | Run external scripts via JSON protocol | [custom](custom.md) |

## Unix Executors (12)

These executors manage OS-level resources across Unix platforms. Each type uses a platform-native backend so that `.hnb` configs are portable -- you write `type: service` and the agent dispatches to the correct implementation for the host OS.

| Type | Description | Linux | macOS | FreeBSD | OpenBSD | NetBSD | Doc |
|------|-------------|:-----:|:-----:|:-------:|:-------:|:------:|-----|
| `service` | Manage system services | systemd | launchctl | rc.d | rcctl | -- | [service](service.md) |
| `package` | Install, upgrade, and remove OS packages | apt/dnf/yum | brew | pkg | pkg_add | pkgin | [package](package.md) |
| `user` | Manage local user accounts | useradd | dscl | pw | useradd | useradd | [user](user.md) |
| `group` | Manage local groups | groupadd | dscl | pw | groupadd | groupadd | [group](group.md) |
| `cron` | Manage cron jobs | /etc/cron.d | crontab | crontab | crontab | crontab | [cron](cron.md) |
| `hostname` | Set the system hostname | hostnamectl | scutil | hostname | hostname | hostname | [hostname](hostname.md) |
| `timezone` | Set the system timezone | timedatectl | systemsetup | shared | shared | shared | [timezone](timezone.md) |
| `firewall` | Manage firewall rules | UFW | pf | pf | pf | pf | [firewall](firewall.md) |
| `sysctl` | Manage kernel parameters | sysctl | sysctl | sysctl | sysctl | sysctl | [sysctl](sysctl.md) |
| `mount` | Manage mount points and fstab | mount/fstab | mount/fstab | mount/fstab | mount/fstab | mount/fstab | [mount](mount.md) |
| `swap` | Manage swap files/devices | swapon | shared | shared | shared | shared | [swap](swap.md) |
| `kernel_module` | Load and unload kernel modules | modprobe | -- | kld | -- | -- | [kernel_module](kernel_module.md) |

A "--" means the executor is not available on that platform.

## Linux-Only Executors (3)

These executors use Linux-specific tools with no equivalent on other platforms.

| Type | Description | Doc |
|------|-------------|-----|
| `tmpfile` | Manage systemd-tmpfiles configs | [tmpfile](tmpfile.md) |
| `logrotate` | Manage logrotate configs | [logrotate](logrotate.md) |
| `alternatives` | Manage update-alternatives entries | [alternatives](alternatives.md) |

## Windows-Only Executors (7)

These executors replace their Unix counterparts on Windows. They are registered under the same `type:` name (e.g., `service`, `package`) so configs are portable across platforms.

| Type | Description | Doc |
|------|-------------|-----|
| `service` (Windows) | Manage Windows services via sc.exe | [service_windows](service_windows.md) |
| `package` (Windows) | Manage packages via Chocolatey/winget/Scoop | [package_windows](package_windows.md) |
| `user` (Windows) | Manage local Windows users | [user_windows](user_windows.md) |
| `firewall` (Windows) | Manage Windows Firewall rules via netsh | [firewall_windows](firewall_windows.md) |
| `cron` (Windows) | Manage Windows scheduled tasks | [cron_windows](cron_windows.md) |
| `hostname` (Windows) | Set the Windows hostname | [hostname_windows](hostname_windows.md) |
| `timezone` (Windows) | Set the Windows timezone | [timezone_windows](timezone_windows.md) |

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
    targetpath: /etc/nginx/nginx.conf
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
