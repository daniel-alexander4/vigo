
---
title: Linux Agent Support
weight: 10
---

# Linux Agent Support

Linux is the primary platform for the Vigo agent. The agent ships as a statically linked musl binary that runs on any Linux distribution without external dependencies.

## Supported Distributions

- **Debian / Ubuntu** (including Mint, Pop!_OS)
- **RHEL / CentOS / Rocky / AlmaLinux**
- **Fedora**
- **SUSE / openSUSE**
- **Arch Linux**
- **Alpine Linux**
- **Amazon Linux**

Architectures: **amd64** and **arm64**.

The musl static binary means no glibc dependency -- it works on any Linux kernel 3.2+ regardless of distribution or C library.

## Bootstrap

```sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

The bootstrap script detects the architecture, downloads the agent binary to `/usr/local/sbin/vigo`, generates ED25519 keys, enrolls with the server using the one-time token, and installs a systemd service unit.

**Manual installation:**

```sh
# Download agent binary
curl -o /usr/local/sbin/vigo https://<server>:8443/api/v1/agent/download/linux/amd64
chmod +x /usr/local/sbin/vigo

# Enroll and start
/usr/local/sbin/vigo --server <server>:1530 --enroll --token <token>
systemctl enable --now vigo-envoy
```

## systemd Service

The agent runs as a systemd service at `vigo-envoy.service`.

```sh
# Check status
systemctl status vigo-envoy

# Restart
systemctl restart vigo-envoy

# View logs
journalctl -u vigo-envoy -f
```

The service is configured with automatic restart on failure and `WatchdogSec` for health monitoring.


## Package Managers

The agent auto-detects the system's package manager based on the OS family trait:

| Distribution | Package Manager | Detection |
|-------------|----------------|-----------|
| Debian / Ubuntu | `apt` | `os_family = debian` |
| RHEL / Fedora | `dnf` (preferred) or `yum` | `os_family = redhat` |
| SUSE / openSUSE | `zypper` | `os_family = suse` |
| Alpine | `apk` | `os_family = alpine` |
| Arch | `pacman` | `os_family = arch` |

```yaml
# Works on any distro -- the agent picks the right package manager
- name: install-nginx
  type: package
  package: nginx
```

Package names may differ across distributions. Use `when:` guards when they do:

```yaml
- name: install-apache-debian
  type: package
  package: apache2
  when: "os_family('debian')"

- name: install-apache-redhat
  type: package
  package: httpd
  when: "os_family('redhat')"
```

## Available Executors

Linux has access to all cross-platform executors plus Linux-specific executors.

### Cross-Platform (work on all systems)

| Executor | Description |
|----------|-------------|
| `file` | File content, permissions, ownership |
| `directory` | Directory creation and permissions |
| `symlink` | Symbolic links |
| `exec` | Command execution with guards (`creates`, `unless`, `onlyif`) |
| `package` | Package install/remove via auto-detected manager |
| `service` | Service management via systemctl |
| `user` | User account management via useradd/usermod/userdel |
| `group` | Group management via groupadd/groupmod/groupdel |
| `cron` | Cron job management |
| `archive` | Download and extract archives |
| `repository` | Package repository configuration |
| `git` | Git repository cloning/updating |
| `sshkey` | SSH authorized_keys management |
| `host` | /etc/hosts entries |
| `mount` | Filesystem mount management |
| `ini` | INI file editing |
| `json_file` | JSON file editing |
| `file_line` | Line-based file editing |

| `blockinfile` | Managed block insertion in files |
| `replace` | Regex-based file content replacement |
| `field_edit` | Structured field editing |
| `pip` | Python pip package management |
| `npm` | Node.js npm package management |
| `gem` | Ruby gem package management |
| `docker_container` | Docker container management |
| `docker_image` | Docker image management |
| `docker_compose` | Docker Compose stack management |
| `process` | Process management (kill, signal) |
| `reboot` | System reboot management |
| `region` | Cloud region/datacenter tagging |
| `atomic` | Atomic file writes |
| `ssh_exec` | Remote command execution over SSH |

### Linux-Specific Executors

| Executor | Description |
|----------|-------------|
| `systemd_dropin` | Systemd unit drop-in overrides (`/etc/systemd/system/<unit>.d/`) |
| `selinux` | SELinux boolean and context management |
| `logrotate` | Logrotate configuration management |
| `alternatives` | Update-alternatives management (Debian/RHEL) |
| `firewall` | Firewall rules via ufw or iptables (auto-detected) |
| `kernel_module` | Kernel module loading/unloading via modprobe |
| `hostname` | Hostname via `hostnamectl` |
| `timezone` | Timezone via `timedatectl` |
| `sysctl` | Sysctl parameter management (`/etc/sysctl.d/`) |
| `swap` | Swap device management |
| `tmpfile` | systemd-tmpfiles configuration |

### systemd_dropin

Manage systemd unit drop-in overrides without replacing the original unit file:

```yaml
- name: increase-nginx-limits
  type: systemd_dropin
  unit: nginx.service
  dropin: limits.conf
  content: |
    [Service]
    LimitNOFILE=65536
    LimitNPROC=4096
  notify: [reload-systemd]

- name: reload-systemd
  type: exec
  command: systemctl daemon-reload
  when: "changed"
```

### selinux

Manage SELinux booleans and file contexts:

```yaml
- name: allow-httpd-network
  type: selinux
  boolean: httpd_can_network_connect
  state: "on"

- name: webapp-context
  type: selinux
  context: httpd_sys_content_t
  path: /srv/webapp
```

### firewall

Manage firewall rules via ufw (Debian/Ubuntu) or iptables (RHEL/others):

```yaml
- name: allow-http
  type: firewall
  port: 80
  protocol: tcp
  action: allow

- name: allow-https
  type: firewall
  port: 443
  protocol: tcp
  action: allow
```

## Trait Collectors

Linux runs all 28 built-in trait collectors:

| Collector | Data Source |
|-----------|------------|
| `os` | `/etc/os-release`, `uname` |
| `hardware` | `/proc/cpuinfo`, `/sys/`, `lscpu` |
| `memory` | `/proc/meminfo` |
| `network` | `/sys/class/net/`, `ip addr` |
| `filesystem` | `df`, `/proc/mounts` |
| `mountpoints` | `/proc/mounts` |
| `blockdevices` | `lsblk`, `/sys/block/` |
| `packages` | dpkg/rpm/apk/pacman query |
| `identity` | `hostname`, `/etc/hostname` |
| `kernel` | `uname -r`, `/proc/version` |
| `processors` | `/proc/cpuinfo` |
| `users` | `/etc/passwd` |
| `uptime` | `/proc/uptime` |
| `loadavg` | `/proc/loadavg` |
| `swap` | `/proc/swaps` |
| `timezone` | `timedatectl`, `/etc/timezone` |
| `locale` | `locale` |
| `dmi` | `/sys/class/dmi/id/` |
| `gpu` | `lspci` |
| `virt` | `systemd-detect-virt`, `/proc/1/cgroup` |
| `systemd` | `systemctl` (unit states, boot time) |
| `selinux` | `getenforce`, `/etc/selinux/config` |
| `docker` | Docker socket (`/var/run/docker.sock`) |
| `cloud` | Instance metadata (AWS, GCP, Azure) |
| `time` | System clock |
| `cert_expiry` | TLS certificate expiration checks |
| `ports` | `/proc/net/tcp`, `/proc/net/tcp6` |


Use traits in `when:` expressions and templates:

```yaml
- name: high-memory-config
  type: file
  target_path: /etc/myapp/config.yaml
  content: |
    max_memory: {{ if ge .Traits.memory.total_mb 16384 }}8192{{ else }}2048{{ end }}
    workers: {{ .Traits.processors.count }}
```

## Example: LAMP Stack Module

```yaml
# modules/lamp.vgo
name: lamp-stack

vars:
  http_port: 80
  mysql_root_password: "secret:vigo/mysql/root-password"
  php_version: "8.2"
  document_root: /var/www/html

resources:
  # Apache
  - name: install-apache-debian
    type: package
    package: apache2
    when: "os_family('debian')"

  - name: install-apache-redhat
    type: package
    package: httpd
    when: "os_family('redhat')"

  - name: apache-service
    type: service
    service: "{{ if eq .Traits.os.family \"debian\" }}apache2{{ else }}httpd{{ end }}"
    state: running
    enabled: true

  - name: firewall-http
    type: firewall
    port: 80
    protocol: tcp
    action: allow

  - name: firewall-https
    type: firewall
    port: 443
    protocol: tcp
    action: allow

  # MySQL
  - name: install-mysql-debian
    type: package
    package: mysql-server
    when: "os_family('debian')"

  - name: install-mysql-redhat
    type: package
    package: mysql-server
    when: "os_family('redhat')"

  - name: mysql-service
    type: service
    service: mysqld
    state: running
    enabled: true

  - name: mysql-root-password
    type: exec
    command: "mysqladmin -u root password"
    stdin: "{{ .Vars.mysql_root_password }}"
    creates: /root/.mysql_configured

  - name: mysql-configured-flag
    type: file
    target_path: /root/.mysql_configured
    content: "configured"
    depends_on: [mysql-root-password]

  # PHP
  - name: install-php-debian
    type: package
    package: "php{{ .Vars.php_version }}"
    when: "os_family('debian')"

  - name: install-php-redhat
    type: package
    package: php
    when: "os_family('redhat')"

  - name: install-php-mysql-debian
    type: package
    package: "php{{ .Vars.php_version }}-mysql"
    when: "os_family('debian')"

  - name: install-php-mysql-redhat
    type: package
    package: php-mysqlnd
    when: "os_family('redhat')"

  # Document root
  - name: document-root
    type: directory
    target_path: "{{ .Vars.document_root }}"
    owner: www-data
    group: www-data
    mode: "0755"
    when: "os_family('debian')"

  - name: document-root-redhat
    type: directory
    target_path: "{{ .Vars.document_root }}"
    owner: apache
    group: apache
    mode: "0755"
    when: "os_family('redhat')"
  - name: index-page
    type: file
    target_path: "{{ .Vars.document_root }}/index.php"
    content: |
      <?php
      echo "Managed by Vigo on {{ .Traits.identity.hostname }}";
      phpinfo();
    notify: [restart-apache]

  - name: restart-apache
    type: exec
    command: "systemctl restart {{ if eq .Traits.os.family \"debian\" }}apache2{{ else }}httpd{{ end }}"
    when: "changed"
```

## Linux-Specific Notes

- **SELinux on RHEL/CentOS**: If SELinux is enforcing, use the `selinux` executor to set booleans and contexts. The `selinux` trait collector reports the current mode (`enforcing`, `permissive`, `disabled`).
- **AppArmor on Ubuntu**: Not directly managed by a dedicated executor. Use `exec` resources to manage AppArmor profiles, or manage profile files via the `file` executor.
- **Static binary**: The agent is compiled with musl and statically linked. No glibc, no shared libraries, no runtime dependencies. Works on Alpine, Debian, RHEL, or any other distribution without modification.
- **Init system**: systemd is required for the `service`, `systemd_dropin`, and `tmpfile` executors. Systems without systemd (Alpine with OpenRC, Void with runit) can still use all other executors -- manage services via `exec` resources instead.
- **Container detection**: The `virtual` trait collector detects container runtimes (Docker, LXC, systemd-nspaced, Podman). Use `when: "is_container"` to skip resources that do not apply inside containers.
- **Kernel modules**: The `kernel_module` executor uses `modprobe` and persists module loading via `/etc/modules-load.d/`. Kernel module parameters are supported via `options:`.
- **Package manager detection**: Fully automatic. The agent reads the `os.family` trait and selects apt, dnf, yum, zypper, apk, or pacman accordingly. On systems with both yum and dnf, dnf is preferred.

## Building

```sh
# Build for the host system
make build-agent

# Cross-compile static musl binaries for both architectures
make dist-agent
# Output: dist/agent/linux/amd64/vigo, dist/agent/linux/arm64/vigo
```

## Related

- [Bootstrap](../../getting-started/bootstrap.md) -- Agent installation on all platforms
- [Executors](../executors/_index.md) -- Full executor reference
- [Traits](../traits/_index.md) -- Full trait collector reference
- [When Expressions](../../concepts/when-expressions.md) -- Conditional logic
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
