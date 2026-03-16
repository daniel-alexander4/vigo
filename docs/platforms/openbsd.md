# OpenBSD Support

Hobnob supports OpenBSD with native integration for rcctl service management and pkg_add package management. The agent shares BSD-common executors with FreeBSD and NetBSD for hostname, timezone, sysctl, and pf firewall management.

## Supported Versions

- **OpenBSD 7.x** (amd64)

## Bootstrap

```sh
curl -sSf https://<server>:8443/bootstrap | sudo sh
```

If `curl` is not installed (it is not in the OpenBSD base system), use `ftp` instead:

```sh
ftp -o - https://<server>:8443/bootstrap | sudo sh
```

The bootstrap script detects OpenBSD via `uname -s`, downloads the agent binary to `/usr/local/sbin/hobnob`, and registers the service via `rcctl`.

## rcctl Service

The agent runs as a daemon managed by `rcctl`.

```sh
# Enable at boot
rcctl enable hobnob_thrall

# Service management
rcctl start hobnob_thrall
rcctl stop hobnob_thrall
rcctl check hobnob_thrall
```

## Package Management

OpenBSD uses **pkg_add** (the base system package manager):

```yaml
- name: install-nginx
  type: package
  package: nginx

- name: install-python
  type: package
  package: python%3.11
```

The agent auto-detects `pkg_add` on OpenBSD. Package queries use `pkg_info` for installed package checks. OpenBSD package names sometimes use `%` for version flavors (e.g., `python%3.11`).

## Available Executors

OpenBSD has access to all cross-platform executors, the OpenBSD-specific service executor, and shared BSD executors.

### Cross-Platform (work on all systems)

| Executor | Description |
|----------|-------------|
| `file` | File content, permissions, ownership |
| `directory` | Directory creation and permissions |
| `symlink` | Symbolic links |
| `exec` | Command execution with guards |
| `package` | Package install/remove via pkg_add |
| `user` | User account management |
| `group` | Group management |
| `cron` | Cron job management |
| `archive` | Download and extract archives |
| `repository` | Package repository configuration |
| `git` | Git repository cloning/updating |
| `sshkey` | SSH authorized_keys management |
| `host` | /etc/hosts entries |
| `mount` | Filesystem mount management |

### OpenBSD-Specific Executors

| Executor | Description |
|----------|-------------|
| `service` | Service management via `rcctl` (`service_openbsd`) |

### BSD-Shared Executors

| Executor | Description |
|----------|-------------|
| `hostname` | Hostname via `/etc/myname` |
| `timezone` | Timezone via `/etc/localtime` symlink |
| `swap` | Swap device management |
| `sysctl` | Sysctl parameter management |
| `firewall` | PF firewall rule management (`firewall_pf`) |

## Trait Collectors

| Collector | Description |
|-----------|-------------|
| `os` | OpenBSD version, architecture (`os_openbsd`) |
| `hardware` | CPU, memory via sysctl (`hardware_bsd`) |
| `network` | Network interfaces and addresses |
| `filesystem` | Mounted filesystems and usage |
| `swap` | Swap device information |
| `packages` | Installed packages via pkg_info (`packages_bsd`) |
| `identity` | Hostname, domain |
| `loadavg` | System load averages |
| `uptime` | System uptime |
| `processors` | CPU count and model |
| `locale` | System locale |
| `users` | Local user accounts |
| `virtual` | Virtualization detection (`virt_bsd`) |

## Example: OpenBSD Router/Firewall

```yaml
# modules/openbsd-router.hnb
name: openbsd-router

vars:
  ext_if: vio0
  int_if: vio1
  int_net: "192.168.1.0/24"

resources:
  - name: set-hostname
    type: hostname
    hostname: fw01.example.com

  - name: enable-forwarding
    type: sysctl
    path: net.inet.ip.forwarding
    value: "1"

  - name: enable-forwarding6
    type: sysctl
    path: net.inet6.ip6.forwarding
    value: "1"

  - name: pf-config
    type: file
    targetpath: /etc/pf.conf
    content: |
      ext_if = "{{ .Vars.ext_if }}"
      int_if = "{{ .Vars.int_if }}"
      int_net = "{{ .Vars.int_net }}"

      set skip on lo0
      set block-policy drop
      set loginterface $ext_if

      match out on $ext_if from $int_net nat-to ($ext_if)

      block all
      pass out quick
      pass in on $int_if from $int_net
      pass in on $ext_if proto tcp to port { 22 }
    notify: [reload-pf]

  - name: enable-pf
    type: service
    service: pf
    state: running
    enabled: true

  - name: reload-pf
    type: exec
    command: pfctl -f /etc/pf.conf
    depends_on: [enable-pf]

  - name: install-monitoring
    type: package
    package: node_exporter
```

## OpenBSD-Specific Notes

- **pledge/unveil**: OpenBSD's pledge and unveil security mechanisms may restrict what the agent can do. The agent binary itself does not call `pledge()` or `unveil()`, so it inherits the default unrestricted process permissions. However, child processes spawned by `exec` resources may be subject to application-level pledge restrictions.
- **PF firewall**: PF is the native and only firewall on OpenBSD. The `firewall_pf` executor manages rules directly. For full ruleset management, use a `file` resource for `/etc/pf.conf` with a `notify` to reload via `pfctl -f`.
- **pkg_add quirks**: OpenBSD's `pkg_add` requires `PKG_PATH` or `installpath` in `/etc/installurl` to be set. The bootstrap script does not configure this -- ensure your mirror is set before using package resources. Package names may differ from other BSDs (e.g., `python%3.11` instead of `python311`).
- **doas vs sudo**: OpenBSD ships `doas` instead of `sudo`. The bootstrap command examples use `sudo` for consistency, but substitute `doas` if sudo is not installed. The agent runs as root and does not use either for its own operations.
- **User/group management**: OpenBSD uses standard `useradd`/`usermod`/`userdel` and `groupadd`/`groupmod`/`groupdel` (shared with Linux). No FreeBSD-style `pw` commands.
- **No kernel module executor**: Unlike FreeBSD, OpenBSD does not support loadable kernel modules in the same way. There is no `kernel_module` executor for OpenBSD.

## Related

- [Bootstrap](bootstrap.md) — Agent installation on all platforms
- [FreeBSD Support](freebsd.md) — Similar BSD platform (shared executors)
- [NetBSD Support](netbsd.md) — Similar BSD platform (shared executors)
- [Executors](../executors/_index.md) — Full executor reference
- [Traits](../traits/_index.md) — Full trait collector reference
