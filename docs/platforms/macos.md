---
title: macOS Agent Support
weight: 30
---

# macOS Agent Support

Vigo supports macOS with native integration for launchd services, Homebrew package management, and macOS-specific executors for user/group management via Directory Services and hostname/timezone via system utilities.

## Supported Versions

- **macOS 12 Monterey**
- **macOS 13 Ventura**
- **macOS 14 Sonoma**
- **macOS 15 Sequoia**

Architectures: **Intel (x86_64)** and **Apple Silicon (aarch64)**.

## Bootstrap

```sh
curl -sSfk https://<server>:8443/bootstrap | sudo sh
```

The bootstrap script detects macOS via `uname -s`, downloads the agent binary, generates ED25519 keys, enrolls with the server, and installs a launchd service.

**Manual installation:**

```sh
# Download agent binary
curl -o /usr/local/sbin/vigo https://<server>:8443/api/v1/agent/download/darwin/arm64
chmod +x /usr/local/sbin/vigo

# Enroll and start
/usr/local/sbin/vigo --server <server>:1530 --enroll --token <token>
sudo launchctl load /Library/LaunchDaemons/com.vigo.envoy.plist
```

## launchd Service

The agent runs as a launchd daemon with a plist at `/Library/LaunchDaemons/com.vigo.envoy.plist`.

```sh
# Check status
sudo launchctl list | grep vigo

# Stop
sudo launchctl unload /Library/LaunchDaemons/com.vigo.envoy.plist

# Start
sudo launchctl load /Library/LaunchDaemons/com.vigo.envoy.plist

# View logs
log show --predicate 'process == "vigo"' --last 1h
```

The plist is configured with `KeepAlive = true` for automatic restart on failure and `RunAtLoad = true` for boot-time startup.

## Package Management

macOS uses **Homebrew** (auto-detected):

```yaml
- name: install-nginx
  type: package
  package: nginx

- name: install-jq
  type: package
  package: jq
  version: "1.7"
```

The agent detects Homebrew at `/opt/homebrew/bin/brew` (Apple Silicon) or `/usr/local/bin/brew` (Intel). Package queries use `brew list` and `brew info`.

## Available Executors

macOS has access to all cross-platform executors, macOS-specific executors, and shared BSD executors.

### Cross-Platform (work on all systems)

| Executor | Description |
|----------|-------------|
| `file` | File content, permissions, ownership |
| `directory` | Directory creation and permissions |
| `symlink` | Symbolic links |
| `exec` | Command execution with guards (`creates`, `unless`, `onlyif`) |
| `package` | Package install/remove via Homebrew |
| `service` | Service management via launchctl |
| `cron` | Cron job management |
| `archive` | Download and extract archives |
| `repository` | Package repository configuration |
| `git` | Git repository cloning/updating |
| `sshkey` | SSH authorized_keys management |
| `host` | /etc/hosts entries |
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

### macOS-Specific Executors

| Executor | Implementation |
|----------|---------------|
| `service` | launchctl (`launchctl load`/`unload`/`list`) |
| `user` | Directory Services via `dscl` |
| `group` | Directory Services via `dscl` |
| `hostname` | `scutil --set HostName`/`LocalHostName`/`ComputerName` |
| `timezone` | `systemsetup -settimezone` |

### Shared BSD Executors

| Executor | Description |
|----------|-------------|
| `hostname` | BSD hostname via `hostname_bsd` (fallback) |
| `timezone` | Timezone via `/etc/localtime` symlink |
| `mount` | BSD-style mount management |
| `swap` | Swap device management |
| `sysctl` | Sysctl parameter management |
| `firewall` | PF (Packet Filter) rule management |

### service (launchctl)

The `service` executor manages launchd services via `launchctl`:

```yaml
- name: nginx-service
  type: service
  service: homebrew.mxcl.nginx
  state: running
  enabled: true

- name: custom-daemon
  type: service
  service: com.example.myapp
  state: running
  enabled: true
```

For Homebrew-installed services, the service name follows the `homebrew.mxcl.<formula>` convention. For custom services, use the bundle identifier from the plist.

### user (dscl)

Manage user accounts via Directory Services:

```yaml
- name: deploy-user
  type: user
  username: deploy
  shell: /bin/zsh
  home: /Users/deploy
  groups: staff,admin

- name: service-account
  type: user
  username: myapp
  shell: /usr/bin/false
  system: true
```

### group (dscl)

Manage groups via Directory Services:

```yaml
- name: dev-group
  type: group
  group: developers
  gid: "800"
```

### hostname (scutil)

Set the macOS hostname (all three variants: HostName, LocalHostName, ComputerName):

```yaml
- name: set-hostname
  type: hostname
  hostname: dev-macbook-01
```

### timezone (systemsetup)

Set the system timezone:

```yaml
- name: set-timezone
  type: timezone
  timezone: America/New_York
```

### firewall (PF)

Manage PF firewall rules (shared with FreeBSD/OpenBSD):

```yaml
- name: allow-ssh
  type: firewall
  port: 22
  protocol: tcp
  action: allow
```

## Trait Collectors

macOS has native trait collectors for system-specific data sources, plus shared BSD collectors:

### macOS-Native Collectors

| Collector | Data Source |
|-----------|------------|
| `os` | `sw_vers`, `uname` (`os_macos`) |
| `hardware` | `sysctl hw.model`, `system_profiler` (`hardware_macos`) |
| `memory` | `sysctl hw.memsize`, `vm_stat` (`memory_macos`) |
| `network` | `ifconfig`, `networksetup` (`network_macos`) |
| `swap` | `sysctl vm.swapusage` (`swap_macos`) |

### Shared BSD Collectors

| Collector | Data Source |
|-----------|------------|
| `identity` | `hostname`, `scutil` |
| `virtual` | `sysctl kern.hv_vmm_present` |
| `uptime` | `sysctl kern.boottime` |
| `loadavg` | `sysctl vm.loadavg` |
| `mountpoints` | `mount` output |
| `processors` | `sysctl hw.ncpu`, `machdep.cpu` |
| `dmi` | `sysctl` hardware identifiers |
| `filesystem` | `df -k` |
| `kernel` | `uname -r/-v` |
| `users` | `/etc/passwd`, `dscl` |
| `blockdevices` | `diskutil list` |
| `timezone` | `/etc/localtime` symlink |
| `locale` | `defaults read NSGlobalDomain` |
| `packages` | `brew list --versions` |
| `gpu` | `system_profiler SPDisplaysDataType` |

### Cross-Platform Collectors

| Collector | Description |
|-----------|-------------|
| `cloud` | Instance metadata (AWS, GCP, Azure) |
| `docker` | Docker socket |
| `time` | System clock |
| `cert_expiry` | TLS certificate expiration checks |
| `ports` | Listening ports |

Use traits in `when:` expressions and templates:

```yaml
- name: apple-silicon-config
  type: file
  target_path: /usr/local/etc/myapp/config.yaml
  content: |
    arch: {{ .Traits.os.arch }}
    cores: {{ .Traits.processors.count }}
    memory_gb: {{ div .Traits.hardware.memory_mb 1024 }}
```

## Example: Development Workstation Module

```yaml
# modules/macos-dev.vgo
name: macos-dev

vars:
  default_shell: /bin/zsh
  git_email: ""
  git_name: ""

resources:
  # Core development tools
  - name: install-git
    type: package
    package: git

  - name: install-jq
    type: package
    package: jq

  - name: install-ripgrep
    type: package
    package: ripgrep

  - name: install-fd
    type: package
    package: fd

  - name: install-fzf
    type: package
    package: fzf

  - name: install-tmux
    type: package
    package: tmux

  # Language runtimes
  - name: install-go
    type: package
    package: go

  - name: install-node
    type: package
    package: node

  - name: install-python
    type: package
    package: python@3.12

  - name: install-rust
    type: package
    package: rustup

  # Container tools
  - name: install-docker
    type: package
    package: docker

  - name: install-colima
    type: package
    package: colima

  # Git configuration
  - name: git-config-email
    type: exec
    command: "git config --global user.email '{{ .Vars.git_email }}'"
    unless: "git config --global user.email | grep -q '{{ .Vars.git_email }}'"

  - name: git-config-name
    type: exec
    command: "git config --global user.name '{{ .Vars.git_name }}'"
    unless: "git config --global user.name | grep -q '{{ .Vars.git_name }}'"

  # Shell configuration
  - name: zshrc
    type: file
    target_path: /Users/{{ .Traits.identity.hostname }}/.zshrc
    content: |
      # Managed by Vigo
      export EDITOR=vim
      export GOPATH=$HOME/go
      export PATH=$GOPATH/bin:$HOME/.cargo/bin:$PATH

      # Homebrew
      eval "$(/opt/homebrew/bin/brew shellenv)"

      # fzf
      [ -f ~/.fzf.zsh ] && source ~/.fzf.zsh

  # SSH directory
  - name: ssh-directory
    type: directory
    target_path: /Users/{{ .Traits.identity.hostname }}/.ssh
    mode: "0700"

  # Hostname
  - name: set-hostname
    type: hostname
    hostname: "{{ .Traits.identity.hostname }}"

  # Timezone
  - name: set-timezone
    type: timezone
    timezone: America/New_York

  # Launch agent for custom daemon
  - name: custom-plist
    type: file
    target_path: /Library/LaunchDaemons/com.example.myapp.plist
    content: |
      <?xml version="1.0" encoding="UTF-8"?>
      <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
      <plist version="1.0">
      <dict>
          <key>Label</key>
          <string>com.example.myapp</string>
          <key>ProgramArguments</key>
          <array>
              <string>/usr/local/bin/myapp</string>
              <string>--config</string>
              <string>/etc/myapp/config.yaml</string>
          </array>
          <key>RunAtLoad</key>
          <true/>
          <key>KeepAlive</key>
          <true/>
      </dict>
      </plist>
    notify: [load-myapp]

  - name: load-myapp
    type: exec
    command: "launchctl load /Library/LaunchDaemons/com.example.myapp.plist"
    refreshonly: true
```

## macOS-Specific Notes

- **Homebrew paths**: On Apple Silicon (M1/M2/M3/M4), Homebrew installs to `/opt/homebrew/`. On Intel Macs, it installs to `/usr/local/`. The agent detects both locations automatically.
- **launchd vs cron**: macOS supports both launchd (via the `service` executor with plist files) and cron (via the `cron` executor). Prefer launchd for system daemons and services; use cron for simple scheduled tasks.
- **dscl for users/groups**: macOS uses Directory Services (`dscl`) instead of standard Unix `useradd`/`groupadd`. The macOS-specific `user_macos` and `group_macos` executors handle this transparently.
- **System Integrity Protection (SIP)**: The agent cannot modify SIP-protected paths (`/System`, `/usr` except `/usr/local`). Place managed files in `/usr/local/`, `/opt/`, `/Library/`, or user home directories.
- **Full Disk Access**: For the agent to manage files in certain protected locations (Desktop, Documents, Downloads), it may need Full Disk Access granted via System Settings > Privacy & Security.
- **No systemd**: macOS uses launchd exclusively. The `systemd_dropin`, `systemd` trait collector, and `tmpfile` executors are not available. Use launchd plists and the `file` executor instead.
- **Firewall**: macOS includes PF (Packet Filter), shared with the BSD platforms. The built-in macOS Application Firewall (socketfilterfw) is not managed -- use PF rules or `exec` resources for ALF configuration.

## Building

```sh
# Cross-compile for macOS (both architectures)
make dist-agent-macos
# Output: dist/agent/darwin/amd64/vigo, dist/agent/darwin/arm64/vigo
```

## Related

- [Bootstrap](../getting-started/bootstrap.md) -- Agent installation on all platforms
- [Executors](../reference/executors/_index.md) -- Full executor reference
- [Traits](../reference/traits/_index.md) -- Full trait collector reference
- [When Expressions](../concepts/when-expressions.md) -- Conditional logic
