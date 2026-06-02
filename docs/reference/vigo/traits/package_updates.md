---
title: package_updates
---

# package_updates

Counts the packages with available updates on this envoy. This is a **periodic** collector — package-manager update checks are expensive (they hit configured repos), so it runs on a much longer interval than the per-cycle collectors and caches its result between runs. Distinct from the [`packages`](packages.md) trait, which inventories *installed* packages.

The count comes from the host's native package manager (apt/dnf/yum and friends; `dnf` reports exit code 100 when updates are available, 0 when none). Repositories that fail to refresh are reported separately under `broken_repos` so a broken mirror surfaces as data rather than a silently wrong count.

## Fields

| Field | Type | Description |
|---|---|---|
| `count` | int | Number of packages with an available update. `0` when fully up to date. |
| `broken_repos` | string[] | Repositories that failed to refresh during the check. Omitted when none. |
| `broken_repo_count` | int | Length of `broken_repos`. Omitted when none. |

## Cadence

Periodic (hourly-class interval), result cached between runs — see the [trait index](_index.md) for the volatile / stable / periodic classification. Use this for "how many envoys are behind on patches" fleet views, not for real-time state.

## See also

- [`packages`](packages.md) — installed-package inventory (the companion `stable` collector).
- [`vigocli swarm`](../../vigocli/swarm.md) and the CVE views for patch-posture reporting.
