# Trait Collectors

## Summary

Trait collectors gather system information from managed nodes. Traits are used in Go templates (`.Traits` namespace), conditional variable evaluation, and `when` expression builtins. Collection runs in parallel, and a caching strategy separates volatile traits (refreshed every cycle) from stable traits (refreshed every 10 cycles).

- **Interface:** `Name() string` + `Collect(ctx) (map[string]any, error)`
- **25 built-in collectors** covering OS, hardware, network, containers, and more
- **Parallel collection** via goroutines
- **Smart caching:** volatile traits refresh every cycle, stable traits cached for 10 cycles
- **Extensible** via custom trait executables

## Collector Interface

```go
type Collector interface {
    Name() string
    Collect(ctx context.Context) (map[string]any, error)
}
```

## Built-in Collectors (25)

| Collector | Key | Classification | Collects |
|-----------|-----|----------------|----------|
| OSCollector | os | Stable | family, distro, version, arch, kernel |
| HardwareCollector | hardware | Stable | cpu_count, memory_mb, disk_gb |
| NetworkCollector | network | Stable | hostname, fqdn, ipv4, ipv6, mac addresses |
| PackageCollector | packages | Stable | installed package names and versions |
| TimeCollector | time | Volatile | hour, weekday, day, month, year, timezone |
| IdentityCollector | identity | Stable | uid, gid, user, group, is_root, groups |
| VirtualCollector | virtual | Stable | system (hypervisor/container), role (guest/host) |
| UptimeCollector | uptime | Volatile | uptime_seconds, boot_time |
| LoadAvgCollector | loadavg | Volatile | load_1m, load_5m, load_15m |
| MountpointsCollector | mountpoints | Volatile | per-mount device, fstype, total/used/avail_gb |
| ProcessorsCollector | processors | Stable | model_name, vendor, physical/logical cores, mhz |
| DMICollector | dmi | Stable | BIOS/system/board info from /sys/class/dmi/id/ |
| CloudCollector | cloud | Stable | provider (aws/gcp/azure/digitalocean/oracle/hetzner/none) |
| FilesystemCollector | filesystem | Volatile | per-filesystem total/used/avail_gb, percent_used |
| KernelCollector | kernel | Stable | version, release, hostname |
| UsersCollector | users | Volatile | logged-in users via `who` |
| MemoryCollector | memory | Volatile | total, available, used (MB), swap |
| BlockDevicesCollector | blockdevices | Stable | block device info from /sys/block/ |
| DockerCollector | docker | Stable | installed, version, running containers |
| SystemdCollector | systemd | Stable | version, default_target, failed_units |
| SELinuxCollector | selinux | Stable | enabled, mode, policy_type |
| TimezoneCollector | timezone | Stable | name, abbreviation, utc_offset |
| LocaleCollector | locale | Stable | LANG, LC_ALL, categories |
| SwapCollector | swap | Volatile | swap devices: path, type, size_kb, used_kb |
| GPUCollector | gpu | Stable | count, nvidia detection, DRM info |

## Caching Strategy

```
Volatile (8): time, uptime, loadavg, mountpoints, filesystem, users, memory, swap
  -> Collected every check-in cycle

Stable (17): os, hardware, network, packages, identity, virtual, processors,
             dmi, cloud, kernel, blockdevices, docker, systemd, selinux,
             timezone, locale, gpu
  -> Collected on first cycle, then every 10th cycle
  -> Cached results reused on intermediate cycles
```

Rationale: PackageCollector alone takes 100-300ms. Caching stable traits saves significant overhead per cycle.

## Usage Examples

### In templates

```yaml
content: |
  hostname={{ .Traits.network.hostname }}
  os={{ .Traits.os.distro }} {{ .Traits.os.version }}
  cpus={{ .Traits.hardware.cpu_count }}
```

### In conditional vars (YAML config)

```yaml
vars:
  is_debian:
    trait: os.family
    in: [debian]
  has_enough_ram:
    trait: hardware.memory_mb
    gte: 4096
```

### In when expressions (agent-side)

```yaml
when: "os_family('debian') && version_ge('22.04')"
when: "arch('amd64') && !distro('alpine')"
```

### CLI queries

```bash
hobnob traits                    # all traits as JSON
hobnob traits os                 # just the "os" collector
hobnob traits os hardware        # multiple collectors
hobnob traits os.distro          # dot-path to specific value: "ubuntu"
hobnob traits network.ipv4       # dot-path: ["192.168.1.10"]
```

## Custom Traits

Server-managed executables are synced to agents via SHA256-verified download:

1. Place the executable in the server's `custom-traits/` directory
2. The agent auto-syncs on check-in (downloads new/changed, removes stale)
3. Each executable runs with a 10-second timeout
4. Output: JSON to stdout (must be a valid JSON object)
5. Results are available under `traits["custom"]["<name>"]`

## Parallel Collection

All collectors run concurrently via goroutines. Failed collectors are logged at warn level and skipped -- collection continues with the remaining collectors. This ensures that a single broken collector (e.g., missing `/sys/class/dmi/id/` on a container) does not block the entire trait-gathering phase.
