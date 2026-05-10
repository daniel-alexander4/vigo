
# Security Scanning

Vigo can deploy and manage security scanning tools across your fleet, collect scan results as traits, and display fleet-wide security posture on the Compliance & Security dashboard.

## Overview

The `security-scanning` example module installs seven open-source security tools on managed envoys. A built-in trait collector (`security_scan`) reads their output and reports summary data as traits — visible on the dashboard, queryable via CLI and API.

**Vigo does not replicate what these tools do.** It deploys them, schedules scans, collects summaries, and provides fleet-wide visibility. The tools handle all scanning logic, CVE database lookups, and detection.

## Tools

| Tool | Purpose | Scan Frequency | License |
|------|---------|---------------|---------|
| [Trivy](https://trivy.dev/) | OS package CVE scanning | Daily | Apache 2.0 |
| [Lynis](https://cisofy.com/lynis/) | Security hardening audit (CIS-style) | Weekly | GPL 3.0 |
| [chkrootkit](http://www.chkrootkit.org/) | Rootkit detection | Daily | BSD |
| [rkhunter](https://rkhunter.sourceforge.net/) | Rootkit/backdoor/sniffer detection | Daily | GPL 2.0 |
| [ClamAV](https://www.clamav.net/) | Antivirus/malware scanning | Daily | GPL 2.0 |
| [AIDE](https://aide.github.io/) | File integrity monitoring | Daily | GPL 2.0 |
| [debsecan](https://packages.debian.org/debsecan) | Debian/Ubuntu CVE tracker | Daily (Debian only) | GPL 2.0 |

## Enabling

1. Copy the example module into your stockpile directory:

```bash
cp /srv/vigo/example-configs/stockpile/modules/security/security-scanning.vgo.example \
   /srv/vigo/stacks/stockpile/modules/security-scanning.vgo
```

2. Add it to your `common.vgo` (or a specific match block):

```yaml
modules: [motd, security-scanning]
```

3. Publish:

```bash
vigocli config publish
```

Envoys will install the tools and begin scanning on their next check-in cycle.

## How Results Flow

```
Module installs tools + schedules scans
        ↓
Exec resources spawn scans in the background (background: "true")
        ↓
Convergence completes immediately — scans run independently
        ↓
Each tool writes output to /var/lib/vigo/security/
        ↓
Built-in security_scan trait collector reads output files
        ↓
Summary data reported as traits on next check-in
        ↓
Dashboard aggregates fleet-wide, links CVEs to NVD
```

Scans use `background: "true"` so they never block convergence. This is critical on slow hardware (Raspberry Pi, small VMs) where a single AIDE or Trivy scan can take 10+ minutes.

## Scan Output

All tools write to `/var/lib/vigo/security/` on each envoy:

| File | Tool |
|------|------|
| `trivy.json` | Trivy JSON report |
| `lynis-report.dat` | Lynis report data |
| `lynis.log` | Lynis full log |
| `chkrootkit.log` | chkrootkit output |
| `rkhunter.log` | rkhunter log |
| `clamav.log` | ClamAV scan results |
| `aide.log` | AIDE integrity check |
| `debsecan.log` | debsecan CVE list (detail or default format) |

## Trait Data

The `security_scan` trait collector emits structured JSON under the `security_scan` key:

```json
{
  "trivy": {
    "critical": 2, "high": 5, "medium": 12, "low": 30, "total": 49,
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "unique_critical": 2, "unique_high": 4, "unique_medium": 10, "unique_low": 25,
  "cves": [
    {"id": "CVE-2024-1234", "severity": "critical", "packages": 3},
    {"id": "CVE-2024-5678", "severity": "high", "packages": 1}
  ],
  "lynis": { "score": 72, "warnings": 3, "suggestions": 8, "last_scan": "2026-03-18T03:00:00Z" },
  "chkrootkit": { "clean": true, "infected": 0, "last_scan": "2026-03-18T03:00:00Z" },
  "rkhunter": { "clean": true, "warnings": 0, "infected": 0, "last_scan": "2026-03-18T03:00:00Z" },
  "clamav": { "clean": true, "infected": 0, "last_scan": "2026-03-18T03:00:00Z" },
  "aide": { "added": 0, "removed": 0, "changed": 3, "clean": false, "last_scan": "2026-03-18T03:00:00Z" },
  "debsecan": { "high": 3, "medium": 10, "low": 5, "unassigned": 2, "total": 20, "last_scan": "2026-03-18T03:00:00Z" }
}
```

CVEs are deduplicated across all scanners (trivy, debsecan, windows_updates) into a unified `cves` array keyed by CVE ID. The `unique_*` counts are the authoritative deduplicated totals. Per-scanner raw counts (e.g., `trivy.critical`) are kept for scanner-specific visibility.

Tools that aren't installed produce no data — their section is simply absent.

## Querying

Find all envoys with critical CVEs:

```bash
vigocli inventory --where "security_scan.unique_critical > 0"
```

Find envoys with rootkit warnings:

```bash
vigocli inventory --where "security_scan.rkhunter.clean = false"
```

Show Lynis scores across the fleet:

```bash
vigocli inventory --show "security_scan.lynis.score"
```

## CVE Deduplication

CVEs are deduplicated at two levels:

1. **Per-host, cross-scanner** (agent-side): the agent merges CVEs from all scanners (trivy, debsecan, windows_updates) into a single map keyed by CVE ID. Same CVE reported by both trivy and debsecan counts once; severity = the highest rank any scanner reported; `packages` = sum across scanners. The per-host `unique_critical` / `unique_high` / `unique_medium` / `unique_low` traits are computed from this **full** deduped set, so they're always accurate for the host.

2. **Fleet-wide** (server-side): the server iterates every host's reported CVE list and dedups by CVE ID. When the same CVE appears on multiple hosts with different severities (typical when one host's agent saw it from both trivy + debsecan and another host's agent only saw one scanner), the **highest severity across all reporting hosts** wins the fleet bucket. This is deterministic across server restarts.

### Wire-cap and truncation

To bound trait-payload size on hosts with extremely long CVE tails, the agent caps the on-wire CVE list at **`MAX_CVES = 200`** entries (sorted by severity desc, then CVE-ID desc). Per-host `unique_*` counts use the full set and are unaffected; only the wire list is truncated. When this happens, the server detects the mismatch (`sum(unique_*) > len(cves)`) and surfaces a banner above the per-host panel on `/security`. The fleet "Unique CVEs" stat may undercount the long tail of the truncated host's CVEs in this case; per-host columns remain accurate.

If you operate hosts with more than 200 unique CVEs that you want represented in the fleet stat, raise `MAX_CVES` in `agent/src/traits/security_scan.rs` and rebuild + redeploy the agent.

## Severity Filter

The Trivy and Debsecan detail views include a severity filter bar. Click a level (Critical, High, Medium, Low) to show only cards at that severity, or click **All** to reset. Counts per level are shown on each button.

## Ask AI

Every finding card in the per-host security drawer carries an **Ask AI** button. Click it to open the AI chat drawer pre-populated with a structured prompt of the form `<hostname> · <kind>: <details>` (e.g. `webby01 · trivy CVE-2024-1234 (high): libssl3 3.0.11→3.0.12 — Memory leak in TLS handshake`).

The assistant replies in a fixed four-section format tailored to vigo:

- **Problem.** One sentence summarizing what the finding is.
- **Action.** One sentence on what needs to change on the host.
- **Vigo module.** A pasteable `.vgo` snippet for `stockpile/modules/`. When vigo can't fix the finding (active malware, forensic-only investigations), this section explains why instead of providing a snippet.
- **How it works.** Optional — only when the module's mechanism isn't obvious.

Available on Trivy CVEs, Debsecan CVEs, Lynis warnings/suggestions, rkhunter warnings, chkrootkit infections, ClamAV findings, AIDE integrity changes, Windows Defender threats, and SCAP failures, plus the cached-summary cards (Lynis, Rootkit, ClamAV, AIDE) that show host-level scan state without per-finding detail.

## CVE Links

The Compliance & Security dashboard links each CVE ID to the [National Vulnerability Database](https://nvd.nist.gov/):

```
https://nvd.nist.gov/vuln/detail/CVE-2024-1234
```

NVD provides descriptions, CVSS scores, affected versions, and links to patches and mitigations.

## Customizing Scan Schedules

Override the default scan window (2:00–5:00 AM) per envoy or fleet-wide via vars:

```yaml
# In common.vgo or a match block
vars:
  scan_hour_start: "1"
  scan_hour_end: "3"
```

Each tool runs once per day. The sentinel file pattern (`/tmp/.vigo-<tool>-YYYYMMDD`) prevents repeated scans within the same day.

**Sentinel-on-success vs unconditional sentinel.** The trivy block uses `&& touch` so the sentinel only fires when the scan completes successfully — a failed scan retries on the next convergence cycle instead of being silenced for 24h. The other scanners (chkrootkit, rkhunter, AIDE, ClamAV, debsecan) intentionally use `|| true; touch` because their non-zero exit codes are legitimate findings (e.g., chkrootkit returns non-zero when it detects warnings, AIDE returns non-zero when integrity changes are detected). For those, the sentinel reflects "the scan ran" rather than "the scan succeeded."

## Background Execution

All scan exec resources use `background: "true"` — the agent spawns each scan as a detached process and returns immediately. Convergence completes in seconds regardless of how long the scans take. Background commands automatically run at the lowest CPU priority (nice 19), which the agent promotes to the `ionice idle` class on Linux (agent 0.30.2+) so scans only get disk I/O when nothing else wants it. Each `run-*` resource also carries a `timeout:` ceiling so a wedged scan (e.g. `aide --check` on a full filesystem) can't linger across cycles — on expiry the agent SIGTERMs the process group, waits 5s, then SIGKILLs, and the next day's cycle respawns cleanly.

Since background processes run independently, there is no output capture or exit code checking by the agent. Scan results are consumed asynchronously by the trait collector from the output files.

## Trivy Scan Scope

`run-trivy` scans the host root filesystem (`trivy rootfs /`) for OS package vulnerabilities. The default `--skip-dirs` list excludes paths that don't carry install-managed package metadata and would otherwise dominate scan time on workstations or container hosts:

```
/home/*/.cache,/root/.cache,/var/cache,/tmp,/var/lib/docker,/var/lib/containerd,/var/lib/snapd,/var/lib/flatpak,/proc,/sys,/dev
```

User browser caches (chromium, firefox), package-manager caches (apt, yum), Docker/containerd image layers, snap/flatpak app sandboxes, and pseudo-filesystems are all skipped. Container layers should be scanned via `trivy image`, not via the host rootfs scan.

If you operate hosts where one of the skipped directories actually contains install-managed binaries (rare), override `run-trivy.command` in a per-envoy matchfile or copy the module and adjust.

## Customizing Trivy Severity

By default, Trivy scans for all severities. To limit:

```yaml
vars:
  trivy_severity: "CRITICAL,HIGH"
```


## AIDE Configuration

The module deploys a focused `/etc/aide/aide.conf` that monitors only security-critical paths:

- **System binaries**: `/usr/bin`, `/usr/sbin`, `/usr/local/bin`, `/usr/local/sbin`
- **Boot/kernel**: `/boot`, `/lib/modules`
- **Auth files**: `/etc/passwd`, `/etc/shadow`, `/etc/sudoers`, `/etc/pam.d`, `/etc/ssh`
- **System config**: `/etc/crontab`, `/etc/cron.d`, `/etc/sysctl.conf`, firewall configs
- **Cron spool**: `/var/spool/cron`

High-churn directories are excluded: `/var/log`, `/var/cache`, `/var/lib/docker`, `/home`, `/tmp`, `/run`, `/proc`, `/sys`.

This replaces the distro default which often monitors everything and causes multi-hour scans. The DB is automatically reinitialized when the config changes.

## AIDE Baseline

After legitimate system changes (package upgrades, new binaries), update the baseline:

```bash
aide --init && cp /var/lib/aide/aide.db.new /var/lib/aide/aide.db
```

Or push this via a Vigo exec resource.

## Windows

A separate `security-scanning-windows.vgo.example` module provides security scanning for Windows envoys:

| Tool | Purpose | Install Method |
|------|---------|---------------|
| **Trivy** | OS package and Windows Update CVE scanning | Chocolatey |
| **Windows Defender** | Antivirus status, real-time protection, threat count | Built-in (PowerShell) |
| **Windows Updates** | Missing patches with CVE IDs and MSRC severity | Built-in (PowerShell) |

Enable it the same way as the Linux module:

```yaml
# In common.vgo or a Windows-specific match block
modules: [security-scanning-windows]
```

All `when:` guards include `os_family('windows')` so it's safe to include alongside the Linux module — each runs only on its platform.

Scan output is written to `C:\ProgramData\Vigo\security\`:

| File | Tool |
|------|------|
| `trivy.json` | Trivy JSON report |
| `defender.json` | Windows Defender status |
| `updates.json` | Missing Windows Updates with CVE IDs |

The same `security_scan` trait collector reads these files on Windows, so results appear on the dashboard alongside Linux envoys.

## Network-Based Scanners

Tools like OpenVAS/Greenbone, Nessus, Qualys, and Rapid7 InsightVM are centralized network scanners — they run on a dedicated host and scan targets remotely. They don't fit the local-agent-per-envoy model.

A separate `openvas.vgo.example` module is provided for installing Greenbone Community Edition on a scanner host. Its results are not collected by the `security_scan` trait collector.

For enterprise scanner integration, deploy the scanner's agent via a custom Vigo module and optionally write a custom trait collector to surface results on the dashboard.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
