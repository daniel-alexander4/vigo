
# Host Self

Host-level resource pressure indicators that don't fit in any other collector. The agent's own footprint is tracked in [`agent_self`](agent_self.md); per-mountpoint disk in [`mountpoints`](mountpoints.md); system memory in [`memory`](memory.md). This collector covers the in-between signals that matter for *"is this host about to fall over"*: kernel-wide FD consumption, total process count, recent OOM-killer activity, and whether the host has been losing power without a graceful shutdown.

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
| `host_self.power_detector_state` | string | `"armed"` | `armed` (the counts below are meaningful) · `first_boot` (nothing observed yet) · `unsupported` (the kernel boot id could not be read) |
| `host_self.unclean_shutdowns_30d` | integer | `3` | Host power losses observed in a rolling 30-day window (omitted unless `armed`) |
| `host_self.boot_transitions_30d` | integer | `9` | All reboots observed in the same window — the denominator for the count above (omitted unless `armed`) |
| `host_self.last_unclean_date` | string | `"2026-09-04"` | Date of the most recent power loss (omitted when there is none) |

## Collection Method

All reads are pure procfs (no subprocess fork) on the happy path:
- `host_self.fd_used`, `host_self.fd_max`, `host_self.fd_pct` — read from `/proc/sys/fs/file-nr`
- `host_self.proc_count` — `read_dir("/proc")` filtered to numeric entries
- `host_self.power_detector_state`, `host_self.unclean_shutdowns_30d`, `host_self.boot_transitions_30d`, `host_self.last_unclean_date` — computed once at agent startup, not per cycle. The agent stores the kernel's `boot_id` (`/proc/sys/kernel/random/boot_id`) in its LMDB state store and marks the record clean when it receives SIGTERM or SIGINT. At the next start it compares: a *different* boot id whose stored record was never marked clean means the previous session lost power. An *unchanged* boot id means the agent restarted but the host did not reboot, so agent self-updates, crashes and `systemctl restart` are never counted.
- `host_self.oom_recent` — checks `/var/log/kern.log` if present (reads only the last 256 KiB to avoid scanning rotated 100 MB logs); falls back to `journalctl -k --since '1 hour ago' --no-pager -q` when kern.log is missing. The journalctl fallback is the only subprocess in this collector and is bounded to 2 seconds — a wedged journalctl can never block check-in.

Errors from the FD, process-count and OOM reads are silently swallowed and the field is reported as `0`. The point of those fields is signal, not certainty.

The power fields are the deliberate exception: they are **omitted entirely** rather than reported as `0` whenever the detector is not `armed`. A zero from a detector that has never observed a boot transition is byte-identical to a genuine "this host has not lost power", and the server treats a missing key as unknown but a zero as good news — so a broken detector would report every host healthy. `boot_transitions_30d` exists for the same reason: `0 unclean of 9 reboots` is a health claim, `0 of 0` is not.

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
    Power losses (30d): {{ .Traits.host_self.unclean_shutdowns_30d }}
```

## What This Catches

A host that loses power — a failing laptop battery, a dying PSU, a yanked cord, an overheating board — leaves nothing behind to find. The kernel does not get to write a log line, `wtmp` is absent on Ubuntu 26.04 and Debian 13 and no longer written on the latter, and an envoy that dies while off-network never even goes stale from the server's point of view. Recording the answer on the way *down* is the only way to have it afterwards, and the envoy reports what it found on its next check-in, however much later that is.

An increase in `unclean_shutdowns_30d` dispatches an `envoy.power_loss` integration event. `vigocli envoys show <host>` prints the same figures.

## Platform

Linux only. macOS, BSD, and Windows return `null` for the entire trait — none of them expose `/proc/sys/fs/file-nr` or `/proc` the same way, and substituting `lsof | wc -l` would mean spawning a subprocess on every check-in.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
