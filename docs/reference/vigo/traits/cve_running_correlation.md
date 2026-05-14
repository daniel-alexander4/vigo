
# cve_running_correlation

The `cve_running_correlation` trait collector answers the sysadmin question *"which of my currently-running binaries have known CVEs?"* by joining three local data sources on the host:

1. **Scanner output** — re-reads the same `/var/lib/vigo/security/trivy.json` and `debsecan.log` files `security_scan` consumes, extracting a per-CVE package-affected mapping.
2. **Running binaries** — walks `/proc/*/exe` symlinks, dedupes by resolved path, counts PIDs per binary, flags the `(deleted)` case (running code no longer matches the on-disk file).
3. **Package ownership** — for each unique binary, asks the system package manager who owns it. Batched into a single `dpkg-query` or `rpm` invocation to avoid per-binary subprocess storms.

The join is done on-agent so the server receives an already-correlated trait and doesn't need a separate query-time CVE × binary × package join.

Linux-only. Classified **periodic** — the walk + lookup is the most expensive operation in the trait pipeline, so it runs on the same hourly cadence as `package_updates` rather than on every check-in.

## Trait Structure

```json
{
  "cve_running_correlation": {
    "running_cves": [
      {
        "cve_id": "CVE-2024-1234",
        "severity": "critical",
        "package": "openssl",
        "package_version": "3.0.2-0ubuntu1.15",
        "binary_path": "/usr/bin/openssl",
        "pid_count": 1,
        "deleted": false
      },
      {
        "cve_id": "CVE-2024-5678",
        "severity": "high",
        "package": "openssh-server",
        "package_version": "1:8.9p1-3ubuntu0.4",
        "binary_path": "/usr/sbin/sshd",
        "pid_count": 1,
        "deleted": true
      }
    ],
    "summary": {
      "total": 2,
      "critical": 1,
      "high": 1,
      "medium": 0,
      "low": 0
    }
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `running_cves[].cve_id` | string | CVE identifier from the scanner output |
| `running_cves[].severity` | string | `critical`, `high`, `medium`, or `low`. Severity tier wins when the same CVE is reported by multiple scanners |
| `running_cves[].package` | string | Package name that owns the binary (dpkg or rpm) |
| `running_cves[].package_version` | string | Installed package version |
| `running_cves[].binary_path` | string | Absolute path from `/proc/<pid>/exe` symlink, with any ` (deleted)` suffix stripped |
| `running_cves[].pid_count` | int | Number of distinct processes running this binary |
| `running_cves[].deleted` | bool | `true` when at least one PID's `/proc/*/exe` link had the `(deleted)` suffix — the on-disk binary was replaced or removed while the process kept running. Typically means "the package was upgraded but the service hasn't been restarted" |
| `summary.total` | int | Count of rows (CVE × binary intersections) |
| `summary.critical` / `.high` / `.medium` / `.low` | int | Row counts per severity |

Rows are sorted deterministically: severity desc, then CVE id asc, then binary path asc. The top of the list is always the most-urgent intersection, which matters because the output is capped at 500 rows to protect the flattened trait payload.

## Deleted-Binary Flag

The `deleted` flag is one of the most operationally useful fields. Linux keeps a running process's `exe` pointer alive via the inode even after the file on disk has been unlinked or replaced (which is how `apt upgrade` works for a package whose binary is currently running). When `deleted: true` on a CVE row, the running code is literally the old vulnerable binary — the fix is on disk but not active until the service restarts.

Query fleet-wide for services running a patched binary that hasn't restarted yet:

```bash
vigocli inventory \
  --where "cve_running_correlation.running_cves.0.deleted=true" \
  --show "hostname,cve_running_correlation.running_cves.0.binary_path,cve_running_correlation.running_cves.0.cve_id"
```

## Scanner Prerequisites

This collector depends on the security-scanning module being active on the host — it reads scanner output from `/var/lib/vigo/security/`. Without at least one of `trivy.json` or `debsecan.log`, the trait emits an empty `running_cves` array. This is intentional: an envoy with no CVE feed reports "zero known running CVEs," which is accurate given what's been measured.

To pre-populate: install the security-scanning module or equivalent that writes trivy/debsecan output to the shared directory.

## Package Ownership Resolution

The collector probes for `dpkg-query` first, then `rpm`, picking the first tool that exists on PATH. Apple / Alpine / Arch ownership (via `brew`, `apk`, `pacman`) is not yet supported — binaries on those systems simply aren't annotated with a package, so they produce no rows. Adding support is a local extension of `lookup_package_ownership`.

Batching: the collector runs one `dpkg-query -S <paths...>` (or `rpm -qf <paths...>`) invocation for the full set of discovered binaries rather than one call per binary. On a realistic 150-binary host this is ~200ms instead of ~4s.

## Caps and Guardrails

| Parameter | Value | Why |
|-----------|-------|-----|
| Binary-walk cap | 2000 | Hosts beyond that are pathological; avoids runaway `/proc` walks |
| Row cap | 500 | Protects the flattened trait payload from 10k-row outliers |
| Subprocess timeout | 15s (each lookup call) | Short enough to bail on a wedged dpkg/rpm |
| Collector timeout | 30s | Walk + lookup + parsing fit comfortably on big hosts |

## Platform Support

| Platform | Supported |
|----------|-----------|
| Linux | Yes — `/proc/*/exe` + dpkg or rpm |
| macOS | No (returns null) |
| FreeBSD / OpenBSD / NetBSD / illumos | No (returns null) |
| Windows | No (returns null) |

## Classification

**Periodic** — runs on an hourly cadence rather than every check-in. Scanner output changes on a slower cadence than check-ins (scanners typically run nightly), and the `/proc` walk + dpkg/rpm lookup is cheap-but-not-free.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../../legal/license.md).*
