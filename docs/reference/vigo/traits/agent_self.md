
# Agent Self

Per-process resource footprint of the Vigo agent itself plus its direct child processes. Used by the fleet to detect FD leaks, RSS climb, and child-process pile-ups before the agent is killed.

## Trait Path
`agent_self`

## Fields

| Path | Type | Example | Description |
|------|------|---------|-------------|
| `agent_self.rss_bytes` | integer | `45678912` | Resident set size of the agent process in bytes |
| `agent_self.vsz_bytes` | integer | `123456789` | Virtual memory size in bytes |
| `agent_self.threads` | integer | `12` | Live thread count for the agent process |
| `agent_self.open_fds` | integer | `47` | Open file descriptor count for the agent process (0 on macOS — `lsof` would require a subprocess per check-in) |
| `agent_self.descendants` | integer | `0` | Live count of processes whose PPID is the agent. Linux only; 0 on macOS/BSD/Windows |
| `agent_self.descendant_open_fds` | integer | `0` | Sum of `open_fds` across those direct descendants. Linux only; 0 on macOS/BSD/Windows |
| `agent_self.uptime_secs` | integer | `86400` | Seconds since the agent binary started (anchored at process startup, independent of host uptime) |

## Collection Method

On Linux, four short procfs reads:
- `/proc/self/status` → `VmRSS` and `VmSize` (parsed as kB, converted to bytes)
- `/proc/self/stat` → live thread count
- `/proc/self/fd/` → open fd count via directory enumeration
- `/proc/<all>/stat` → walked once to identify direct descendants (PPID matches agent's PID); for each match, `/proc/<pid>/fd/` is enumerated to sum descendant fd usage

The descendant walk is one level deep. The agent's direct children (gitback helper's `git` child, `exec` resource subprocesses, package-manager probes) are what FD leaks would surface in. Going deeper to grandchildren would be more accurate but adds proc-walk cost on every check-in; deferred until a real grandchild leak shows up.

On macOS, RSS / VSZ / thread count come from `ps -o rss=,vsz=,nlwp= -p <pid>`. `open_fds` is reported as `0` because exposing it would require `lsof` (a subprocess per check-in). `descendants` and `descendant_open_fds` are also `0` for the same reason.

## Why This Matters

The agent regularly spawns short-lived children (git-receive-pack via the gitback helper, package-manager probes, exec resources). FD leaks in those children would not surface in `open_fds` alone — the parent's count stays stable while the child accumulates. Tracking `descendant_open_fds` lets the fleet detector catch leaks in child code paths that the agent's own `/proc/self/fd` count is blind to.

The collector is classified `volatile` — it refreshes every cycle. The point is to catch leaks *within* a single envoy's lifetime, not to track historical trends.

## Using in When Expressions

```yaml
- name: alert-fd-pressure
  type: exec
  command: /usr/local/bin/page-oncall fd-leak
  when: "agent_self.open_fds > 500"
```

## Using in Templates

```yaml
- name: agent-stats
  type: file
  target_path: /var/lib/vigo/agent-stats.txt
  content: |
    Agent uptime: {{ .Traits.agent_self.uptime_secs }}s
    RSS: {{ .Traits.agent_self.rss_bytes }} bytes
    Open FDs: {{ .Traits.agent_self.open_fds }} (+ {{ .Traits.agent_self.descendant_open_fds }} in descendants)
```

## Platform

Linux + macOS. BSD platforms return zeroed fields; Windows N/A.

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
