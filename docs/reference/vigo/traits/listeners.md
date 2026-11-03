
# listeners

The `listeners` trait collector correlates TCP LISTEN-state sockets from `/proc/net/tcp*` with their owning processes via `/proc/<pid>/fd/*` symlinks, emitting per-listener rows with port, bind address, pid, command, and binary path. Answers fleet-wide questions like "what's listening on :443 across 200 hosts" without needing shell access to each one.

## Method

1. Read `/proc/net/tcp` and `/proc/net/tcp6` for sockets in LISTEN state (`0A`). Record each as `(port, bind_address, family, inode)`.
2. Walk `/proc/*/fd/*` once. Each entry is a symlink; targets of the form `socket:[<inode>]` identify which PID owns which socket.
3. For each attributed PID, read `/proc/<pid>/comm` (command name) and `/proc/<pid>/exe` (binary path).
4. Emit per-listener rows. The walk short-circuits as soon as every listening inode is matched.

Pure sysfs reads — no subprocesses, no network activity. The collector is a passive generalization of the `ports` trait: `ports` returns just port numbers; `listeners` adds process attribution and bind-address detail.

## Trait Structure

```json
{
  "listeners": {
    "listeners": [
      {
        "port": 22,
        "family": "ipv4",
        "protocol": "tcp",
        "bind_address": "0.0.0.0",
        "pid": 1234,
        "comm": "sshd",
        "exe": "/usr/sbin/sshd"
      },
      {
        "port": 443,
        "family": "ipv6",
        "protocol": "tcp",
        "bind_address": "::",
        "pid": 5678,
        "comm": "nginx",
        "exe": "/usr/sbin/nginx"
      }
    ]
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `listeners[].port` | int | Listening TCP port |
| `listeners[].family` | string | `ipv4` or `ipv6` |
| `listeners[].protocol` | string | Always `tcp` at present (UDP not reported) |
| `listeners[].bind_address` | string | Bind address parsed from `/proc/net/tcp*` — IPv4 dotted-decimal (`0.0.0.0`, `127.0.0.1`) or IPv6 colon-hex (`::`, `fe80::...`) |
| `listeners[].pid` | int or null | Owning process PID. `null` when the agent cannot attribute the socket (process exited between the two reads, or the agent lacks permission to inspect `/proc/<pid>/fd/*`) |
| `listeners[].comm` | string | Short command name from `/proc/<pid>/comm` |
| `listeners[].exe` | string | Absolute path from the `/proc/<pid>/exe` symlink |

Same port can appear twice when a process binds to both IPv4 and IPv6 (listening on `::` with `IPV6_V6ONLY=0` typically dual-stacks, but the kernel still exposes two rows). Consumers that want unique ports should dedupe by `(port, pid)`.

## Platform Support

| Platform | Supported | Method |
|----------|-----------|--------|
| Linux | Full | `/proc/net/tcp*` + `/proc/*/fd/*` |
| macOS | Not supported | Returns null — no `/proc/net/tcp` equivalent |
| BSD family | Not supported | Returns null |
| illumos | Not supported | Returns null |
| Windows | Not supported | Returns null |

On macOS/BSD, equivalent data could come from `sockstat` or `lsof`, but the passive-only constraint rules out spawning those subprocesses on every check-in. The existing `ports` collector still emits port numbers on those platforms.

## Use Cases

Find every host where something other than nginx is listening on :443:

```bash
vigocli inventory \
  --where "listeners.0.port=443,listeners.0.comm!=nginx" \
  --show "hostname,listeners.0.comm,listeners.0.exe"
```

Identify rogue binaries listening on typical service ports fleet-wide:

```bash
vigocli inventory \
  --where "listeners.0.port=22,listeners.0.exe!=/usr/sbin/sshd" \
  --show "hostname,listeners.0.exe"
```

## Relationship to `ports`

The `ports` trait emits a simple sorted array of listening port numbers. It's stable, has no process info, and is the right shape for `when:` expressions like `when: "has_port(443)"`. `listeners` is the richer, dashboard-oriented counterpart — use it when you need to know *who* is listening, not just *what* port.

Both collectors run on every check-in.

## Security Notes

- `/proc/<pid>/fd/*` symlink resolution requires root-equivalent capability on most kernels. Non-root agents will see only their own processes; sockets owned by other UIDs emit rows with `pid: null`.
- Binary paths (`exe`) and command names (`comm`) are informational only — they're not authenticated. A process can be renamed via `prctl(PR_SET_NAME)`; use `exe` over `comm` when integrity matters.
- The collector does not read process memory, command-line arguments, or environment variables.

## Classification

**Stable** — listener set changes on service restart, not at the pace of check-in cycles. Cached across convergence cycles.
