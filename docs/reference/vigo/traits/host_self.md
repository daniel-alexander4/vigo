
# Host Self

Host-level resource pressure indicators that don't fit in any other collector. The agent's own footprint is tracked in [`agent_self`](agent_self.md); per-mountpoint disk in [`mountpoints`](mountpoints.md); system memory in [`memory`](memory.md). This collector covers the in-between signals that matter for *"is this host about to fall over"*: kernel-wide FD consumption, total process count, and recent OOM-killer activity.

## Trait Path
`host_self`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `host_self.fd_used` | integer | `9472` | File descriptors currently allocated kernel-wide (`/proc/sys/fs/file-nr` field 1) |
| `host_self.fd_max` | integer | `9223372036854775807` | Kernel ceiling for the file table (`/proc/sys/fs/file-nr` field 3) |
| `host_self.fd_pct` | number | `0.0001` | `fd_used / fd_max × 100`, rounded to 2 decimal places |
| `host_self.proc_count` | integer | `412` | Number of `/proc/<pid>` entries (live processes) |
| `host_self.oom_recent` | integer | `0` | `1` if dmesg/kern.log shows an OOM-killer invocation in the recent log window, `0` otherwise |

## Collection Method

All reads are pure procfs (no subprocess fork) on the happy path:
- `host_self.fd_used`, `host_self.fd_max`, `host_self.fd_pct` — read from `/proc/sys/fs/file-nr`
- `host_self.proc_count` — `read_dir("/proc")` filtered to numeric entries
- `host_self.oom_recent` — checks `/var/log/kern.log` if present (reads only the last 256 KiB to avoid scanning rotated 100 MB logs); falls back to `journalctl -k --since '1 hour ago' --no-pager -q` when kern.log is missing. The journalctl fallback is the only subprocess in this collector and is bounded to 2 seconds — a wedged journalctl can never block check-in.

Errors from any of the reads are silently swallowed and the field is reported as `0`. The point of the collector is signal, not certainty.

## Why This Matters

Per-process FD limits (`agent_self.open_fds`) and per-mount disk usage (`mountpoints`) catch most resource-exhaustion modes, but two host-level signals slip through:
- **Kernel file table exhaustion.** `/proc/sys/fs/file-nr` is the only place that reports kernel-wide FD usage. A runaway process can blow past `agent_self.open_fds` thresholds while the kernel itself is healthy, or — rarely — push the kernel ceiling itself.
- **OOM kills that didn't hit the agent.** If the OOM killer fires on something else (a package manager, a stray child, a noisy neighbor on a shared host), the agent process survives but the host is clearly under memory pressure.

The collector is classified `volatile` — it refreshes every cycle so a sudden FD or proc-count spike is visible in real time.

## Using in When Expressions

```yaml
- name: page-oncall-fd-pressure
  type: exec
  command: /usr/local/bin/alert host-fd-pressure
  when: "host_self.fd_pct > 80"

- name: hold-deploys-after-oom
  type: file
  target_path: /var/lib/vigo/deploy-paused
  content: "OOM-killer fired in last hour\n"
  when: "host_self.oom_recent == 1"
```

## Using in Templates

```yaml
- name: host-pressure-report
  type: file
  target_path: /var/lib/vigo/host-pressure.txt
  content: |
    FDs: {{ .Traits.host_self.fd_used }} / {{ .Traits.host_self.fd_max }} ({{ .Traits.host_self.fd_pct }}%)
    Processes: {{ .Traits.host_self.proc_count }}
    Recent OOM: {{ .Traits.host_self.oom_recent }}
```

## Platform

Linux only. macOS, BSD, and Windows return `null` for the entire trait — none of them expose `/proc/sys/fs/file-nr` or `/proc` the same way, and substituting `lsof | wc -l` would mean spawning a subprocess on every check-in.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
