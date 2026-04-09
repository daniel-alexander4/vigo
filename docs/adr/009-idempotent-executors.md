# ADR-009: Idempotent executors

**Status:** Accepted  
**Date:** 2025-01-01 (retroactive)

## Context

The agent applies desired state to managed nodes on every convergence cycle (every 15 seconds by default). If executors blindly apply changes on every cycle, they'll restart services unnecessarily, rewrite files that haven't changed, and reinstall packages that are already present.

## Decision

All executors must be idempotent: check the current state first, act only if there's drift. This is enforced by convention — every executor follows the pattern: read current state → compare to desired state → act only if different → report what changed.

## Consequences

- Convergence cycles are cheap when the node is already in desired state — just reads, no writes
- Services don't get restarted unnecessarily (which would cause downtime)
- Files aren't rewritten (which would trigger inotify watchers and log noise)
- `notify` / `subscribes` triggers only fire when a dependency actually changes
- The agent can run at high frequency (15s) without being destructive
- Trade-off: executors are more complex to write than "just apply it" scripts. Each must implement both the check and the apply path.
- Modules must be self-healing: if an external failure corrupts state, the next convergence cycle should detect the drift and repair it without manual intervention.

## Alternatives considered

- **Apply-always with change detection** — rejected because some apply operations have side effects even when the end state is the same (service restarts, file timestamp changes)
- **Hash-based skip** — partially used (file content hashing), but not sufficient alone — services, packages, and users need semantic comparison
