# FreeBSD Support

Hobnob supports FreeBSD with native integration for rc.d services, the `pw` user/group utility, kernel module management, and `pkg` package management. The agent includes FreeBSD-specific executors alongside shared BSD executors for hostname, timezone, sysctl, and pf firewall management.

## Supported Versions

- **FreeBSD 13.x, 14.x** (amd64, arm64)

## Bootstrap

```sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

The bootstrap script detects FreeBSD via `uname -s`, downloads the agent binary to `/usr/local/sbin/hobnob`, and installs an rc.d service script.

## rc.d Service

The agent runs as an rc.d service at `/usr/local/etc/rc.d/hobnob_thrall`.

```sh
# Enable at boot
sysrc hobnob_thrall_enable=YES

# Service management
service hobnob_thrall start
service hobnob_thrall stop
service hobnob_thrall status
```

## Package Management

FreeBSD uses **pkg** (the binary package manager):

```yaml
- name: install-nginx
  type: package
  package: nginx

- name: install-python
  type: package
  package: python311
  version: "3.11.9"
```

The agent auto-detects `pkg` on FreeBSD. Package queries use `pkg info` for installed package checks.

## Available Executors

FreeBSD has access to all cross-platform executors, FreeBSD-specific executors, and shared BSD executors.

### Cross-Platform (work on all systems)

| Executor | Description |
|----------|-------------|
| `file` | File content, permissions, ownership |
| `directory` | Directory creation and permissions |
| `symlink` | Symbolic links |
| `exec` | Command execution with guards |
| `package` | Package install/remove via pkg |
| `cron` | Cron job management |
| `archive` | Download and extract archives |
| `repository` | Package repository configuration |
| `git` | Git repository cloning/updating |
| `sshkey` | SSH authorized_keys management |
| `host` | /etc/hosts entries |
| `mount` | Filesystem mount management |

### FreeBSD-Specific Executors

| Executor | Description |
|----------|-------------|
| `service` | Service management via rc.d (`service_freebsd`) |
| `user` | User management via `pw useradd`/`pw usermod`/`pw userdel` |
| `group` | Group management via `pw groupadd`/`pw groupmod`/`pw groupdel` |
| `kernel_module` | Kernel module management via `kldload`/`kldstat`/`kldunload` |

### BSD-Shared Executors

| Executor | Description |
|----------|-------------|
| `hostname` | Hostname via `/etc/rc.conf` |
| `timezone` | Timezone via `/etc/localtime` symlink |
| `swap` | Swap device management |
| `sysctl` | Sysctl parameter management |
| `firewall` | PF firewall rule management (`firewall_pf`) |

## Trait Collectors

| Collector | Description |
|-----------|-------------|
| `os` | FreeBSD version, architecture (`os_freebsd`) |
| `hardware` | CPU, memory via sysctl (`hardware_bsd`) |
| `network` | Network interfaces and addresses |
| `filesystem` | Mounted filesystems and usage |
| `swap` | Swap device information |
| `packages` | Installed packages via pkg (`packages_bsd`) |
| `identity` | Hostname, domain |
| `loadavg` | System load averages |
| `uptime` | System uptime |
| `processors` | CPU count and model |
| `locale` | System locale |
| `users` | Local user accounts |
| `virtual` | Virtualization detection — jails, bhyve (`virt_bsd`) |
| `dmi` | DMI/SMBIOS hardware data (`dmi_bsd`) |

## Example: FreeBSD Jail Host

```yaml
# modules/freebsd-jailhost.hnb
name: freebsd-jailhost

vars:
  pf_ext_if: vtnet0
  jail_net: "10.0.99.0/24"

resources:
  - name: install-jail-tools
    type: package
    package: ezjail

  - name: pf-config
    type: file
    targetpath: /etc/pf.conf
    content: |
      ext_if = "{{ .Vars.pf_ext_if }}"
      jail_net = "{{ .Vars.jail_net }}"

      set skip on lo0

      nat on $ext_if from $jail_net to any -> ($ext_if)

      block in all
      pass out all keep state
      pass in on $ext_if proto tcp to port { 22, 80, 443 }
    notify: [reload-pf]

  - name: enable-pf
    type: service
    service: pf
    state: running
    enabled: true

  - name: sysctl-forwarding
    type: sysctl
    path: net.inet.ip.forwarding
    value: "1"

  - name: sysctl-sendspace
    type: sysctl
    path: net.inet.tcp.sendspace
    value: "65536"

  - name: reload-pf
    type: exec
    command: pfctl -f /etc/pf.conf
    depends_on: [enable-pf]
```

## FreeBSD-Specific Notes

- **pkg vs ports**: Hobnob manages binary packages via `pkg`. For ports-based installations, use `exec` resources with appropriate guards.
- **Jails**: Jail creation and management is not handled by a dedicated executor. Use `exec` resources to invoke `jail`/`ezjail-admin` commands with idempotent guards (`creates:` or `unless:`).
- **ZFS**: No dedicated ZFS executor. Use `exec` resources for `zpool` and `zfs` commands. Ensure idempotency with `unless: "zfs list poolname"` or similar guards.
- **PF firewall**: The `firewall_pf` executor manages PF rules. For complex rulesets, use a `file` resource for `/etc/pf.conf` with a `notify` to reload via `exec`.
- **User/group management**: FreeBSD uses `pw(8)` instead of `useradd`/`groupadd`. The `user_freebsd` and `group_freebsd` executors handle this automatically.
- **Kernel modules**: The `kernel_module_freebsd` executor loads modules via `kldload` and checks status via `kldstat`. Persistent loading requires adding to `/boot/loader.conf` (use a `file` or `exec` resource).

## Related

- [Bootstrap](bootstrap.md) — Agent installation on all platforms
- [NetBSD Support](netbsd.md) — Similar BSD platform (shared executors)
- [OpenBSD Support](openbsd.md) — Similar BSD platform (shared executors)
- [Executors](../executors/_index.md) — Full executor reference
- [Traits](../traits/_index.md) — Full trait collector reference
