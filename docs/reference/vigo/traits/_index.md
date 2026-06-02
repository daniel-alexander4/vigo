
# Trait Collectors

Traits are auto-discovered facts about a managed machine (envoy). The Vigo agent ships 50 built-in trait collectors covering OS identity, hardware, network, installed and running packages, security posture, certificates, listening services, kernel module attestation, swarm participation, CVE × running-binary correlation, and agent/host resource pressure. Most run cross-platform on Linux, macOS, FreeBSD, OpenBSD, and NetBSD; eight are Linux-only (`tpm`, `secureboot`, `package_source_audit`, `kernel_module_attestation`, `listeners`, `cve_running_correlation`, `host_self`, `hypervisor`); 13 run on Windows. Traits are reported to the server on each check-in and are available in `.vgo` config files for `when:` conditional expressions and Go template rendering (via `.Traits`).

Each collector runs in parallel under a per-collector timeout (5s default; up to 30s for `cve_running_correlation` and 60s for `security_scan`). Failed collectors are logged and skipped without affecting other collectors. Collectors are classified as **volatile** (run every cycle: time, uptime, loadavg, mountpoints, filesystem, users, memory, swap, ports, swarm, swarm_seeds, gitback, puddle, agent_self, host_self), **stable** (cached across ~12 cycles: everything else), or **periodic** (cached across ~hourly intervals: `package_updates`, `cloud`, `cve_running_correlation`).

## Trait Collectors

The trait paths and structure are identical across platforms so `when:` expressions and templates work without modification — each collector uses a platform-native implementation under the hood (for example, the `os` collector reads `/etc/os-release` on Linux, `sw_vers` on macOS, and `sysctl kern.ostype` on the BSDs).

| Name | Trait Path | Description | Doc |
|------|------------|-------------|-----|
| OS | `os` | Distribution, version, kernel, architecture, packaging family | [os.md](os.md) |
| Hardware | `hardware` | CPU count, memory MB, disk GB per mount | [hardware.md](hardware.md) |
| Network | `network` | Hostname, FQDN, IP addresses, MAC addresses | [network.md](network.md) |
| Packages | `packages` | Installed packages and versions (dpkg/rpm/brew/pkg/pkgin) | [packages.md](packages.md) |
| Package Updates | `package_updates` | Periodic: count of packages with an available update, plus any repos that failed to refresh. Distinct from `packages` (installed inventory) | [package_updates.md](package_updates.md) |
| Time | `time` | Current local/UTC time, date, clock, weekday | [time.md](time.md) |
| Identity | `identity` | UID, GID, username, group, privilege status | [identity.md](identity.md) |
| Virtual | `virtual` | Virtualization detection (VM/container type and role) | [virtual.md](virtual.md) |
| Uptime | `uptime` | System uptime in seconds, boot time | [uptime.md](uptime.md) |
| Load Average | `loadavg` | 1/5/15-minute load averages | [loadavg.md](loadavg.md) |
| Mountpoints | `mountpoints` | Physical mounts with device, fstype, size, inode usage | [mountpoints.md](mountpoints.md) |
| Processors | `processors` | CPU model, vendor, physical/logical cores, clock speed | [processors.md](processors.md) |
| DMI | `dmi` | BIOS, motherboard, chassis, system serial, and per-DIMM identity (SMBIOS type-17: manufacturer/part/serial/size/speed) | [dmi.md](dmi.md) |
| Cloud | `cloud` | Cloud provider + IMDS metadata (instance ID, type, region, zone, tags) | [cloud.md](cloud.md) |
| Filesystem | `filesystem` | Per-mount disk usage (total, used, free, percent) | [filesystem.md](filesystem.md) |
| Kernel | `kernel` | Kernel version, release, hostname | [kernel.md](kernel.md) |
| Users | `users` | Human, OS, and third-party accounts; logged-in users | [users.md](users.md) |
| Memory | `memory` | RAM and swap total, used, free, available, percent | [memory.md](memory.md) |
| Block Devices | `blockdevices` | Physical block devices with size, vendor, model, serial, firmware, read-only flag | [blockdevices.md](blockdevices.md) |
| Docker | `docker` | Docker version, running containers, storage/cgroup driver | [docker.md](docker.md) |
| Systemd | `systemd` | systemd version, default target, failed units | [systemd.md](systemd.md) |
| SELinux | `selinux` | SELinux enabled/mode/policy | [selinux.md](selinux.md) |
| Timezone | `timezone` | IANA timezone name, abbreviation, UTC offset | [timezone.md](timezone.md) |
| Locale | `locale` | System locale settings (LANG, LC_*) | [locale.md](locale.md) |
| Swap | `swap` | Active swap devices with type, size, usage, priority | [swap.md](swap.md) |
| GPU | `gpu` | GPU devices with driver, memory, temperature, utilization | [gpu.md](gpu.md) |
| Cert Lifecycle | `cert_lifecycle` | TLS certificates fleet-wide with CN, SANs, issuer, validity window, and days remaining; concatenated-PEM bundles supported | [cert_lifecycle.md](cert_lifecycle.md) |
| Ports | `ports` | Listening TCP ports | [ports.md](ports.md) |
| Security Scan | `security_scan` | CVE counts, hardening scores, rootkit status, file integrity | [security_scan.md](security_scan.md) |
| TPM | `tpm` | Trusted Platform Module presence and version | [tpm.md](tpm.md) |
| Secure Boot | `secureboot` | UEFI Secure Boot status and boot mode | [secureboot.md](secureboot.md) |
| Display | `display` | Display server detection (X11, Wayland, both, none) | [display.md](display.md) |
| Swarm Cache | `swarm` | Cached blobs, chunk sources, per-chunk timing, download duration | [swarm.md](../../../howto/set-up-swarm.md) |
| Swarm Seeds | `swarm_seeds` | Seed files and broken manifests for envoy-originated distribution | [swarm_seeds.md](swarm_seeds.md) |
| Gitback | `gitback` | Publisher-side state (active sources, per-source sha, discovery/publish errors) plus receiver-side materialized mirrors | [gitback.md](gitback.md) |
| Poolq | `poolq` | Per-user signed poolq messages (ADR-029): walks `~/.vigo-poolq/<topic_id>/messages/*.json` across users and emits the verifying ones verbatim for the server's `poolqmesh` aggregator | [poolq.md](poolq.md) |
| Puddle | `puddle` | Sole reporter of puddle identity + health (ADR-014): walks each user's `~/.vigo-puddle/` and emits the current pubkey, the friendly-name claim (ADR-022), and local health signals (initialized, session unlocked, pair/rekey window, retired count) for the server's `puddlemesh` aggregator | [puddle.md](puddle.md) |
| Curator | `curator` | Per-user signed curator catalog entries (ADR-024): walks `~/.vigo-curator/<artifact_id>/entry.json` across users and emits the verifying ones verbatim for the server's `curatormesh` aggregator | [curator.md](curator.md) |
| Lockbox | `lockbox` | Per-user encrypted-directory state — ciphertext file set, tombstones, and known recipients in `~/lockbox/`; consumed by `lockboxmesh` for cross-envoy consistency + recipient-drift views | [lockbox.md](lockbox.md) |
| Longdrawer | `longdrawer` | Per-user LAN directory state — file set + tombstones in `~/longdrawer/`; consumed by `longdrawermesh` | [longdrawer.md](longdrawer.md) |
| Agent Self | `agent_self` | Vigo agent's own resource footprint: RSS, VSZ, threads, open FDs, direct-descendant count + descendant FD total, uptime | [agent_self.md](agent_self.md) |
| Package Source Audit | `package_source_audit` | Linux-only: apt/yum/dnf package sources with URL (credentials stripped), suite, components, enabled state | [package_source_audit.md](package_source_audit.md) |
| Kernel Module Attestation | `kernel_module_attestation` | Linux-only: loaded kernel modules with signature status (signed / unsigned / bad_signature) from `/proc/modules` + sysfs taint | [kernel_module_attestation.md](kernel_module_attestation.md) |
| Listeners | `listeners` | Linux-only: per-listener rows correlating `/proc/net/tcp*` LISTEN sockets with owning PID, command, and binary via `/proc/*/fd/*` | [listeners.md](listeners.md) |
| CVE × Running Correlation | `cve_running_correlation` | Linux-only, periodic: joins trivy/debsecan CVE output with running binaries (`/proc/*/exe` + dpkg/rpm) to surface CVEs affecting actually-executing code, flagging `(deleted)` upgraded-but-not-restarted cases | [cve_running_correlation.md](cve_running_correlation.md) |
| Host Self | `host_self` | Linux-only: kernel-wide FD usage (`/proc/sys/fs/file-nr`), live process count, recent OOM-killer activity from kern.log/journalctl | [host_self.md](host_self.md) |
| Hypervisor | `hypervisor` | Linux-only: host-side libvirt detection — flips `host_is_libvirt: true` when the libvirt daemon's Unix socket is present. Sibling to `virtual` (which is the guest-side detector); gate `vm:` resources with `when: "host_is_libvirt"`. | [hypervisor.md](hypervisor.md) |
| Host Containers | `host_containers` | Linux + macOS: per-container state on the local Docker daemon — name, image, image_id, state, health, restart_count, oom_killed. Pairs with the `docker` collector (daemon-level metadata) | [host_containers.md](host_containers.md) |
| Battery | `battery` | Linux-only: laptop battery charge level, wear health (`energy_full` ÷ `energy_full_design`), cycle count, status, AC-online, and time-to-empty/full. `health_pct` drives the Agent Health card (warn < 80% · fail < 70%); empty on hosts with no `BAT*` node | [battery.md](battery.md) |

## Windows Trait Collectors (13)

| Name | Trait Path | Description | Doc |
|------|------------|-------------|-----|
| OS | `os` | Windows edition, version, build, architecture | [os_windows.md](os_windows.md) |
| Hardware | `hardware` | CPU count, memory MB, total disk GB | [hardware_windows.md](hardware_windows.md) |
| Identity | `identity` | Username, administrator privilege status | [identity_windows.md](identity_windows.md) |
| Memory | `memory` | Physical memory total, free, used, available, percent | [memory_windows.md](memory_windows.md) |
| Mountpoints | `mountpoints` | Logical drives with filesystem, size, usage | [mountpoints_windows.md](mountpoints_windows.md) |
| Network | `network` | Hostname, primary IP, MAC address | [network_windows.md](network_windows.md) |
| Packages | `packages` | Installed packages via Chocolatey or winget | [packages_windows.md](packages_windows.md) |
| Processors | `processors` | CPU model, core counts, clock speed | [processors_windows.md](processors_windows.md) |
| Swap | `swap` | Pagefile total and current usage | [swap_windows.md](swap_windows.md) |
| Timezone | `timezone` | Windows timezone name and abbreviation | [timezone_windows.md](timezone_windows.md) |
| Uptime | `uptime` | System uptime in seconds, boot time | [uptime_windows.md](uptime_windows.md) |
| Users | `users` | Local user account names | [users_windows.md](users_windows.md) |

| Filesystem | `filesystem` | Per-drive disk usage (total, used, free, percent) | [filesystem_windows.md](filesystem_windows.md) |

## Connwaer Trait Collector

If the [Connwaer](../../compliance-matrix.md) compliance verification agent is installed alongside the Vigo agent, the `connwaer` trait collector reads Connwaer's JSON result files from disk and reports them under the `connwaer` trait namespace (see [connwaer.md](connwaer.md)). Each capability's latest result (status, summary, evidence, controls) is available as `.Traits.connwaer.<capability_name>`. Results older than 24 hours are considered stale and excluded.

| Platform | Results Directory |
|----------|------------------|
| Linux/BSD/macOS | `/var/lib/connwaer/results/` |
| Windows | `C:\ProgramData\Connwaer\results\` |

## Network Device Traits

Network devices managed via the [gateway proxy](../../../howto/set-up-network-devices.md) pattern have a dedicated trait collector that runs over SSH. These traits are reported under the `device` namespace.

| Name | Trait Path | Description | Doc |
|------|------------|-------------|-----|
| Network Device | `device` | Device model, firmware, serial, interfaces, VLANs, routes | [network_device.md](network_device.md) |

## Platform Implementation Details

The following collectors share the same trait path across all platforms but use platform-specific implementations: `os`, `hardware`, `identity`, `memory`, `mountpoints`, `network`, `packages`, `processors`, `swap`, `timezone`, `uptime`, `users`, `filesystem`.

The `time` collector uses a single cross-platform implementation. The `dmi`, `cloud`, and `docker` collectors are cross-platform but may return limited data on some platforms.

On macOS, platform-specific collectors use `sysctl`, `sw_vers`, `system_profiler`, and `ifconfig`. On FreeBSD, OpenBSD, and NetBSD, shared BSD collectors use `sysctl`, `ifconfig`, and `/proc` (where available). macOS has dedicated implementations for `os`, `hardware`, `network`, `memory`, and `swap`; FreeBSD, OpenBSD, and NetBSD each have a dedicated `os` collector and share BSD implementations for the rest.

The `systemd` and `selinux` collectors are Linux-specific and return `null` on macOS, BSD, and Windows. The following collectors are unavailable on Windows and return `null`: `virtual`, `loadavg`, `kernel`, `blockdevices`, `systemd`, `selinux`, `locale`, `gpu`.

## Using Traits

### In When Expressions

Traits power `when:` conditional expressions on resources. Built-in functions like `os_family()` and `is_container` evaluate trait data:

```yaml
- name: install-nginx
  type: package
  package: nginx
  when: "os_family('debian')"
```

### In Templates

Traits are available as `.Traits` in Go templates within `content:` attributes:

```yaml
- name: motd
  type: file
  target_path: /etc/motd
  content: |
    Welcome to {{ .Traits.network.hostname }}
    OS: {{ .Traits.os.distro }} {{ .Traits.os.version }}
    CPUs: {{ .Traits.hardware.cpu_count }}
    Memory: {{ .Traits.hardware.memory_mb }} MB
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
