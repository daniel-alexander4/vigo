# Envoy Agent Footprint

**Location:** Tab on the envoy detail page (`/envoys/{id}`) — the **Agent Footprint** button at the right end of the tab bar. HTMX-loads into the detail page's content area like the other tabs; there is no standalone route. Drilldown target for the dashboard's [Agent Pressure card](dashboard.md#card-grid).

Per-envoy view of the agent process's own resource usage and the host's FD pressure. Sourced entirely from the `agent_self.*` and `host_self.*` traits the agent emits on every check-in — no extra collection, no live RPC.

## Why this exists

Pre-0.41.23 the data was collected but invisible in the UI; the only operator path was `vigocli traits livequery <trait> --target '*'`, which nobody ran proactively. The Agent Footprint tab and the dashboard's Agent Pressure rollup are the two surfaces that close that gap.

## Sections

### Agent Process

From `/proc/self/{status,fd}` on Linux; `ps` fallback on macOS. Each row is one row in the table:

| Field | Trait | Threshold |
|---|---|---|
| Resident memory (RSS) | `agent_self.rss_bytes` | uncolored — RSS varies too much by host load to threshold cleanly; track trend, not absolute |
| Open file descriptors | `agent_self.open_fds` | warn > 250 / fail > 500 |
| Threads | `agent_self.threads` | uncolored — context only |
| Live descendants | `agent_self.descendants` | uncolored — context only |
| Open FDs across descendants | `agent_self.descendant_open_fds` | warn > 200 / fail > 500 |
| Agent uptime | `agent_self.uptime_secs` | uncolored — context only |

The descendant rows matter because the agent regularly spawns short-lived children (git-receive-pack via the gitback helper, package-manager probes, exec resources) and FD leaks in those children would not show up in `open_fds`. Tracking the descendant total catches leaks in child code paths the agent's own /proc/self/fd count is blind to.

### Host Pressure

Linux only. Sourced from the `host_self` trait collector.

| Field | Trait | Threshold |
|---|---|---|
| Host FD usage | `host_self.fd_used` / `host_self.fd_max` / `host_self.fd_pct` | warn > 50% / fail > 80% |
| Total processes on host | `host_self.proc_count` | uncolored — context only |
| Recent oom-killer events | `host_self.oom_recent` | warn on any non-zero (no fail level) |

Missing values render as `—`, distinct from a real `0` — this lets you tell "the trait is absent" from "everything is fine."

## Empty state

When the envoy has no traits yet (just enrolled, hasn't checked in once), the body shows:

> No agent or host telemetry yet — the agent has not reported a check-in for this envoy.

## Source of truth

Both this tab and the dashboard's Agent Pressure rollup go through `server/fleetpressure` — `Compute(EnvoySource) → EnvoyPressure` for this view, `Aggregate([]EnvoySource) → FleetSummary` for the dashboard. Same threshold consts, same severity classification. Per the [`feedback_envoy_stats_sot.md`](../../../.claude/projects/-home-dan-repos-vigo/memory/feedback_envoy_stats_sot.md) rule, the dashboard "highest open_fds" cell can never disagree with the per-envoy row because both are derived from one function.
