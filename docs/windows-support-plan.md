# Windows Agent Support — Implementation Plan

## Overview

Add support for Windows as a managed node OS. The server is unaffected (runs on Linux). Only the agent (`hobnob`) binary and server-side bootstrap delivery need changes.

The approach: build-tag-based platform files (`_linux.go` / `_windows.go`) so each platform compiles only its own implementation. Shared interfaces stay in common files.

---

## Phase 1: Foundation — Paths, Build, OS Detection

### 1.1 Centralize platform paths

Create `agent/platform/paths.go` with an interface, plus `paths_linux.go` and `paths_windows.go`:

| Purpose | Linux | Windows |
|---------|-------|---------|
| Data dir | `/etc/hobnob-node` | `%ProgramData%\Hobnob` |
| Binary | `/usr/local/bin/hobnob` | `%ProgramFiles%\Hobnob\hobnob.exe` |
| Snapshots | `/var/lib/hobnob/snapshots` | `%ProgramData%\Hobnob\snapshots` |
| Flags | `/etc/hobnob-node/flags` | `%ProgramData%\Hobnob\flags` |
| Custom executors | `/etc/hobnob-node/executors` | `%ProgramData%\Hobnob\executors` |
| Hosts file | `/etc/hosts` | `%SystemRoot%\System32\drivers\etc\hosts` |
| SSH known hosts | `/etc/ssh/ssh_known_hosts` | `%ProgramData%\ssh\ssh_known_hosts` |

Update `agent/main.go` flag defaults to call platform functions instead of hardcoded paths.

### 1.2 Cross-compile for Windows

Add Windows targets to Makefile `dist` and `Dockerfile.server`:

```makefile
GOOS=windows GOARCH=amd64 go build -o dist/agent/windows/amd64/hobnob.exe ./agent
GOOS=windows GOARCH=arm64 go build -o dist/agent/windows/arm64/hobnob.exe ./agent
```

Update `server/bootstrap/bootstrap.go` to serve `hobnob.exe` when OS is `windows`.

### 1.3 Signal handling

`agent/main.go` uses `syscall.SIGTERM` which doesn't exist on Windows. Split into:
- `signal_linux.go` — `SIGINT`, `SIGTERM`
- `signal_windows.go` — `os.Interrupt` only (Windows Services receive stop via SCM, not signals)

### 1.4 Custom executor discovery

`agent/main.go` `buildExecutors()` checks Unix mode `0111` for executability. On Windows:
- Check file extension (`.exe`, `.ps1`, `.bat`, `.cmd`)
- Strip extension from executor name

**Files changed:** `agent/main.go`, `agent/platform/` (new), `Makefile`, `Dockerfile.server`

---

## Phase 2: Trait Collectors

### 2.1 Collectors that already work (via gopsutil)

These use `gopsutil/v4` which supports Windows natively — no changes needed:
- `os`, `hardware`, `network`, `uptime`, `loadavg`, `processors`, `memory`, `dmi`, `cloud`, `gpu`, `time`, `identity`

### 2.2 Collectors that need Windows implementations

| Collector | Linux source | Windows approach |
|-----------|-------------|-----------------|
| `users` | `/etc/passwd` | `net user` or PowerShell `Get-LocalUser` |
| `packages` | dpkg/rpm/etc query | `Get-Package`, `choco list -l`, `winget list` |
| `filesystem` | `/proc/filesystems`, `df` | `Get-Volume` PowerShell |
| `mountpoints` | `/proc/mounts`, `findmnt` | `Get-Volume`, `Get-PSDrive` |
| `blockdevices` | `/sys/block`, `lsblk` | `Get-Disk`, `Get-Partition` |
| `locale` | `/etc/locale.conf` | `Get-Culture`, `Get-WinSystemLocale` |
| `virtual` | DMI + container checks | `gopsutil` (partial), `Get-ComputerInfo` |
| `swap` | `/proc/swaps` | `Get-CimInstance Win32_PageFileUsage` |

### 2.3 Collectors to skip on Windows

These have no Windows equivalent — return empty traits gracefully:
- `systemd` — Linux-only init system
- `selinux` — Linux-only security module
- `kernel` — reads `/proc/sys/kernel/*`

### 2.4 Implementation pattern

Use build tags per collector where needed:

```
agent/traits/users_linux.go     // parses /etc/passwd
agent/traits/users_windows.go   // calls Get-LocalUser
```

The `DefaultCollectors()` function already returns all collectors — each one should be a no-op or return platform-appropriate data based on its build-tagged implementation.

**Files changed:** `agent/traits/` — ~10 collector files get `_linux.go`/`_windows.go` splits

---

## Phase 3: Core Executors

### 3.1 Service executor

**Linux:** `systemctl` (show, start, stop, restart, reload, enable, disable)
**Windows:** Windows Service Control Manager

| Operation | Windows command |
|-----------|----------------|
| Check state | `sc.exe query <name>` |
| Start | `sc.exe start <name>` or `Start-Service` |
| Stop | `sc.exe stop <name>` or `Stop-Service` |
| Restart | Stop + Start (no native restart) |
| Enable | `sc.exe config <name> start=auto` |
| Disable | `sc.exe config <name> start=disabled` |

Split into `service_linux.go` and `service_windows.go`, both satisfying the `Executor` interface.

### 3.2 User executor

**Linux:** `useradd`, `usermod`, `userdel`, `/etc/passwd`, `/etc/shadow`
**Windows:** `net user` or PowerShell `New-LocalUser`, `Set-LocalUser`, `Remove-LocalUser`

Differences:
- No `/etc/passwd` or `/etc/shadow` — query via `net user <name>` or PowerShell
- No GECOS field — map `comment` to "Full Name" (`net user <name> /fullname:"..."`)
- No shell field — skip on Windows
- Home directory — `%UserProfile%` managed by Windows; `net user <name> /homedir:"..."`
- Groups — `net localgroup <group> <user> /add` (additive, same semantics)
- Password — `net user <name> <password>` (plaintext, not hash; consider `Set-LocalUser`)
- Authorized keys — `%ProgramData%\ssh\administrators_authorized_keys` or `%UserProfile%\.ssh\authorized_keys`

### 3.3 Package executor + Windows package managers

Add three new backends in `agent/modules/pkgmgr/`:

| Backend | File | Install | Remove | Query |
|---------|------|---------|--------|-------|
| Chocolatey | `choco.go` | `choco install -y <pkg>` | `choco uninstall -y <pkg>` | `choco list --exact <pkg>` |
| Winget | `winget.go` | `winget install --accept-source-agreements -e <pkg>` | `winget uninstall -e <pkg>` | `winget list --exact <pkg>` |
| Scoop | `scoop.go` | `scoop install <pkg>` | `scoop uninstall <pkg>` | `scoop info <pkg>` |

Detection in `detect.go`: check `runtime.GOOS == "windows"`, then probe for `choco.exe`, `winget.exe`, `scoop.cmd` in PATH. Preference order: chocolatey > winget > scoop.

### 3.4 Process executor

**Linux:** `pgrep -x`, `pkill`, `systemctl restart`
**Windows:** `tasklist /FI "IMAGENAME eq <name>"`, `taskkill /IM <name>`, `sc.exe start`

Split into `process_linux.go` and `process_windows.go`.

### 3.5 Hostname executor

**Linux:** reads/writes `/etc/hostname`, falls back to `hostnamectl`
**Windows:** `powershell -Command "Rename-Computer -NewName '<name>' -Force"` (requires reboot) or `wmic computersystem where name='%COMPUTERNAME%' call rename name='<name>'`

### 3.6 Timezone executor

**Linux:** `/etc/timezone`, `timedatectl`, `/etc/localtime` symlink
**Windows:** `tzutil /s "<timezone>"`, `tzutil /g` to read

### 3.7 Host executor

**Linux:** `/etc/hosts`
**Windows:** `%SystemRoot%\System32\drivers\etc\hosts` — same file format, different path. Use platform path abstraction.

### 3.8 SSH key executor

**Linux:** `/etc/ssh/ssh_known_hosts`
**Windows:** `%ProgramData%\ssh\ssh_known_hosts` — same file format, different path.

**Files changed:** `agent/modules/` — 8+ executor files get `_linux.go`/`_windows.go` splits, 3 new pkgmgr files

---

## Phase 4: Remaining Executors

### 4.1 Firewall executor

**Linux:** `ufw allow/deny/delete`
**Windows:** `netsh advfirewall firewall add/delete rule` or PowerShell `New-NetFirewallRule`, `Remove-NetFirewallRule`

| Operation | Windows command |
|-----------|----------------|
| Allow port | `netsh advfirewall firewall add rule name="<name>" dir=in action=allow protocol=<proto> localport=<port>` |
| Deny port | `netsh advfirewall firewall add rule name="<name>" dir=in action=block protocol=<proto> localport=<port>` |
| Delete rule | `netsh advfirewall firewall delete rule name="<name>"` |
| Check rule | `netsh advfirewall firewall show rule name="<name>"` |

### 4.2 Group executor

**Linux:** `groupadd`, `groupmod`, `groupdel`, `/etc/group`
**Windows:** `net localgroup <name> /add`, `net localgroup <name> /delete`

Simpler on Windows — no GID management.

### 4.3 Cron → Task Scheduler

**Linux:** writes `/etc/cron.d/<name>` files
**Windows:** `schtasks /create /tn "<name>" /tr "<command>" /sc <schedule> ...`

The `schedule` param in cron format needs translation to Windows Task Scheduler format. This is the most complex mapping:

| Cron concept | Windows equivalent |
|-------------|-------------------|
| `*/5 * * * *` (every 5 min) | `/sc minute /mo 5` |
| `0 * * * *` (hourly) | `/sc hourly` |
| `0 0 * * *` (daily) | `/sc daily` |
| `0 0 * * 0` (weekly) | `/sc weekly /d SUN` |
| `0 0 1 * *` (monthly) | `/sc monthly /d 1` |

Complex cron expressions (e.g., `0 9-17 * * 1-5`) may need to be split into multiple scheduled tasks or use XML task definitions.

### 4.4 Executors to skip on Windows

These have no Windows equivalent — return `skipped` with a clear message:
- `sysctl` — kernel parameters are Linux-only
- `mount` — Windows mounts are managed by the OS
- `kernel_module` — no Windows equivalent
- `swap` — Windows pagefile is auto-managed

The server should warn when these are assigned to a Windows node (via `os_family` trait conditional).

### 4.5 INI / JSON file executors

Already cross-platform — they operate on file content. Only path handling differs, which is covered by Phase 1.

**Files changed:** `agent/modules/` — 4 more `_linux.go`/`_windows.go` splits, cron→task scheduler mapping

---

## Phase 5: Bootstrap & Service Installation

### 5.1 PowerShell bootstrap script

Add a PowerShell script template alongside the existing shell script in `server/bootstrap/bootstrap.go`.

Server detection: check `User-Agent` header or add `?os=windows` query param. Serve `.ps1` for Windows, `.sh` for Unix.

PowerShell bootstrap flow:
1. Detect architecture (`$env:PROCESSOR_ARCHITECTURE`)
2. Download `hobnob.exe` via `Invoke-WebRequest`
3. Create `%ProgramData%\Hobnob\` directory
4. Generate ED25519 keypair + ECDSA TLS key
5. Send CSR to server
6. Save certs, keypair, UUID
7. Install Windows Service

Usage on Windows node:
```powershell
irm http://<server>:8181/bootstrap?os=windows | iex
```

### 5.2 Windows Service installation

Replace `installSystemdService()` with `installWindowsService()` on Windows:

```go
// agent/service_windows.go
func installWindowsService(binaryPath, serverAddr, dataDir string) error {
    // sc.exe create hobnob-node binPath= "C:\...\hobnob.exe" start= auto
    // sc.exe description hobnob-node "Hobnob configuration management agent"
    // sc.exe failure hobnob-node reset= 86400 actions= restart/60000/restart/60000/restart/60000
    // sc.exe start hobnob-node
}
```

Service recovery: auto-restart after 60 seconds on failure (3 attempts, reset after 24h).

### 5.3 Agent download endpoint

Update `server/api/download.go` (or equivalent) to serve `hobnob.exe` for `windows/amd64` and `windows/arm64` paths.

**Files changed:** `server/bootstrap/bootstrap.go`, `agent/main.go` (service install split), `server/api/`

---

## Phase 6: When Expression Builtins

### 6.1 Changes needed

| Builtin | Change |
|---------|--------|
| `process_running('name')` | Linux: `pgrep -x` → Windows: `tasklist /FI "IMAGENAME eq name.exe"` |
| `flag_set('name')` | Use platform path from Phase 1 |
| `in_group('name')` | Linux: `id -nG` + `/etc/group` → Windows: `net localgroup <name>` or `whoami /groups` |
| `command_succeeds('cmd')` | Works cross-platform, but shell quoting differs — document Windows usage |

All time-based builtins (`hour_range`, `day_of_week`, `day_of_month`) and trait-based builtins (`os_family`, `distro`, `arch`, `version_ge`) work unchanged.

**Files changed:** `agent/runner/runner.go` — split 3 builtins into platform files

---

## Phase 7: Documentation & Testing

### 7.1 Documentation updates
- README.md — add Windows bootstrap instructions
- CLAUDE.md — note Windows support in architecture
- agent/README.md — Windows-specific sections
- node-configs/ examples — add Windows module examples (IIS, Windows Update, etc.)

### 7.2 Testing
- Unit tests for each Windows executor (mock command output)
- Unit tests for Windows trait collectors
- Unit tests for PowerShell bootstrap script generation
- Unit tests for cron-to-task-scheduler mapping
- Cross-compilation CI check (`GOOS=windows go build ./agent`)
- Integration test on Windows VM (manual or CI with Windows runner)

### 7.3 Example Windows modules

```yaml
# node-configs/modules/iis.hnb
name: iis
resources:
  - name: iis-feature
    type: exec
    command: "powershell -Command \"Install-WindowsFeature -Name Web-Server -IncludeManagementTools\""
    creates: "C:\\inetpub\\wwwroot"

  - name: iis-service
    type: service
    service: W3SVC
    enabled: true
    state: running
    depends_on: [iis-feature]
```

---

## Implementation Order

| Phase | Description | Estimated scope |
|-------|-------------|-----------------|
| 1 | Foundation (paths, build, signals) | ~10 files |
| 2 | Trait collectors | ~12 files |
| 3 | Core executors (service, user, package, process, hostname, timezone, host, sshkey) | ~15 files |
| 4 | Remaining executors (firewall, group, cron/tasks, skip stubs) | ~8 files |
| 5 | Bootstrap & service installation | ~5 files |
| 6 | When expression builtins | ~3 files |
| 7 | Documentation & testing | ~10 files |

## Design Decisions

- **Build tags over runtime checks** — use `_linux.go`/`_windows.go` file suffixes so each platform only compiles what it needs. Use `runtime.GOOS` only in shared code that needs minor branching.
- **Same executor names** — `service`, `user`, `package`, `cron` keep the same resource type names in configs. The agent selects the right implementation at compile time. Module authors use `os_family` conditionals to handle platform-specific resources.
- **Executors with no Windows equivalent** — `sysctl`, `mount`, `kernel_module`, `swap` return `skipped` on Windows rather than failing. The server should warn when assigning these to Windows nodes.
- **Cron → Task Scheduler** — keep the `cron` resource type name. The Windows implementation translates cron-style schedules to `schtasks` parameters. Complex cron expressions that can't be cleanly mapped should return a clear error.
- **Package manager priority** — chocolatey > winget > scoop, auto-detected. Users can override via agent config flag.
- **No PowerShell dependency for core operations** — prefer `sc.exe`, `net.exe`, `schtasks.exe`, `netsh.exe` which are always available. Use PowerShell only where no cmd-line alternative exists.
