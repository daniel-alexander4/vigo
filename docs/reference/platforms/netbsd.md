
# NetBSD Support

Vigo supports NetBSD with native integration for rc.d services and pkgin package management. The agent shares BSD-common executors with FreeBSD and OpenBSD.

## Supported Versions

- **NetBSD 9.x, 10.x** (amd64)

## Bootstrap

```sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

The bootstrap script detects NetBSD via `uname -s`, downloads the agent binary to `/usr/local/sbin/vigo`, and installs an rc.d service script.

## rc.d Service

The agent runs as an rc.d service at `/usr/local/etc/rc.d/vigo_envoy`.

```sh
# Enable at boot
echo 'vigo_envoy="YES"' >> /etc/rc.conf

# Service management
/usr/local/etc/rc.d/vigo_envoy start
/usr/local/etc/rc.d/vigo_envoy stop
/usr/local/etc/rc.d/vigo_envoy status
```

## Package Management

NetBSD uses **pkgin** (the binary package manager for pkgsrc):

```yaml
- name: install-nginx
  type: package
  package: nginx

- name: install-python
  type: package
  package: python311
  version: "3.11.6"
```

The agent auto-detects pkgin on NetBSD. Package queries use `pkg_info` for installed package checks.

## Available Executors

NetBSD has access to all cross-platform executors plus shared BSD executors:

### Cross-Platform (work on all systems)

| Executor | Description |
|----------|-------------|
| `file` | File content, permissions, ownership |
| `directory` | Directory creation and permissions |
| `symlink` | Symbolic links |
| `exec` | Command execution with guards |
| `package` | Package install/remove via pkgin |
| `service` | Service management via rc.d |
| `user` | User account management |
| `group` | Group management |
| `cron` | Cron job management |
| `archive` | Download and extract archives |
| `repository` | Package repository configuration |
| `git` | Git repository cloning/updating |
| `sshkey` | SSH authorized_keys management |
| `host` | /etc/hosts entries |
| `mount` | Filesystem mount management |

### BSD-Shared Executors

| Executor | Description |
|----------|-------------|
| `hostname` | Hostname via `/etc/myname` |
| `timezone` | Timezone via `/etc/localtime` symlink |
| `swap` | Swap device management |
| `sysctl` | Sysctl parameter management |
| `mount` | BSD-style mount management |

## Trait Collectors

| Collector | Description |
|-----------|-------------|
| `os` | NetBSD version, architecture (`os_netbsd`) |
| `hardware` | CPU, memory via sysctl (`hardware_bsd`) |
| `network` | Network interfaces and addresses |
| `filesystem` | Mounted filesystems and usage |
| `swap` | Swap device information |
| `packages` | Installed pkgsrc packages (`packages_bsd`) |
| `identity` | Hostname, domain |
| `loadavg` | System load averages |
| `uptime` | System uptime |
| `processors` | CPU count and model |
| `locale` | System locale |
| `users` | Local user accounts |
| `virtual` | Virtualization detection (`virt_bsd`) |

## Example: NetBSD Web Server

```yaml
# modules/netbsd-web.vgo
name: netbsd-web

vars:
  http_port: 80

resources:
  - name: install-nginx
    type: package
    package: nginx

  - name: nginx-config
    type: file
    target_path: /usr/pkg/etc/nginx/nginx.conf
    content: |
      worker_processes auto;
      events { worker_connections 1024; }
      http {
          include /usr/pkg/etc/nginx/mime.types;
          server {
              listen {{ .Vars.http_port }};
              root /var/www;
          }
      }
    notify: [restart-nginx]
    depends_on: [install-nginx]

  - name: webroot
    type: directory
    target_path: /var/www
    owner: www
    group: www

  - name: restart-nginx
    type: service
    service: nginx
    state: running
    enabled: true
    depends_on: [install-nginx]
```

## NetBSD-Specific Notes

- **Package paths**: pkgsrc installs to `/usr/pkg/` (not `/usr/local/`). Config files are typically at `/usr/pkg/etc/`.
- **Init system**: rc.d with `/etc/rc.conf` for service enablement. No systemd.
- **User management**: Standard BSD `useradd`/`usermod`/`userdel`.
- **Firewall**: NPF (NetBSD Packet Filter) is the native firewall. Use `exec` resources to manage NPF rules — no dedicated executor yet.
- **pkgsrc quarterly**: NetBSD uses pkgsrc quarterly branches. Version pinning with pkgin works via `package=version` syntax.

## Related

- [Bootstrap](../../getting-started/bootstrap.md) — Agent installation on all platforms
- [FreeBSD Support](freebsd.md) — Similar BSD platform (shared executors)
- [OpenBSD Support](openbsd.md) — Similar BSD platform (shared executors)
- [Executors](../executors/_index.md) — Full executor reference
- [Traits](../traits/_index.md) — Full trait collector reference
