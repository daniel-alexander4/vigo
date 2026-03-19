# Security Scanning

Hobnob can deploy and manage security scanning tools across your fleet, collect scan results as traits, and display fleet-wide security posture on the Compliance & Security dashboard.

## Overview

The `security-scanning` example module installs seven open-source security tools on managed thralls. A built-in trait collector (`security_scan`) reads their output and reports summary data as traits — visible on the dashboard, queryable via CLI and API.

**Hobnob does not replicate what these tools do.** It deploys them, schedules scans, collects summaries, and provides fleet-wide visibility. The tools handle all scanning logic, CVE database lookups, and detection.

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

1. Copy the example module into your policy stage directory:

```bash
cp /srv/hobnob/examples/modules/security/security-scanning.hnb.example \
   /srv/hobnob/policy/stage/modules/security-scanning.hnb
```

2. Add it to your `common.hnb` (or a specific node mapping):

```yaml
modules: [motd, security-scanning]
```

3. Publish:

```bash
hobnobcli config publish
```

Thralls will install the tools and begin scanning on their next check-in cycle.

## How Results Flow

```
Module installs tools + schedules scans
        ↓
Exec resources run scans during configured hour window (default 2:00–5:00 AM)
        ↓
Each tool writes output to /var/lib/hobnob/security/
        ↓
Built-in security_scan trait collector reads output files
        ↓
Summary data reported as traits on next check-in
        ↓
Dashboard aggregates fleet-wide, links CVEs to NVD
```

## Scan Output

All tools write to `/var/lib/hobnob/security/` on each thrall:

| File | Tool |
|------|------|
| `trivy.json` | Trivy JSON report |
| `lynis-report.dat` | Lynis report data |
| `lynis.log` | Lynis full log |
| `chkrootkit.log` | chkrootkit output |
| `rkhunter.log` | rkhunter log |
| `clamav.log` | ClamAV scan results |
| `aide.log` | AIDE integrity check |
| `debsecan.log` | debsecan CVE list |

## Trait Data

The `security_scan` trait collector emits structured JSON under the `security_scan` key:

```json
{
  "trivy": {
    "critical": 2,
    "high": 5,
    "medium": 12,
    "low": 30,
    "total": 49,
    "cves": ["CVE-2024-1234", "CVE-2024-5678"],
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "lynis": {
    "score": 72,
    "warnings": 3,
    "suggestions": 8,
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "chkrootkit": {
    "clean": true,
    "infected": 0,
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "rkhunter": {
    "clean": true,
    "warnings": 0,
    "infected": 0,
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "clamav": {
    "clean": true,
    "infected": 0,
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "aide": {
    "added": 0,
    "removed": 0,
    "changed": 3,
    "clean": false,
    "last_scan": "2026-03-18T03:00:00Z"
  },
  "debsecan": {
    "high": 3,
    "medium": 10,
    "low": 5,
    "other": 2,
    "total": 20,
    "cves": ["CVE-2024-9999"],
    "last_scan": "2026-03-18T03:00:00Z"
  }
}
```

Tools that aren't installed produce no data — their section is simply absent.

## Querying

Find all thralls with critical CVEs:

```bash
hobnobcli inventory --where "security_scan.trivy.critical > 0"
```

Find thralls with rootkit warnings:

```bash
hobnobcli inventory --where "security_scan.rkhunter.clean = false"
```

Show Lynis scores across the fleet:

```bash
hobnobcli inventory --show "security_scan.lynis.score"
```

## CVE Links

The Compliance & Security dashboard links each CVE ID to the [National Vulnerability Database](https://nvd.nist.gov/):

```
https://nvd.nist.gov/vuln/detail/CVE-2024-1234
```

NVD provides descriptions, CVSS scores, affected versions, and links to patches and mitigations.

## Customizing Scan Schedules

Override the default scan window (2:00–5:00 AM) per thrall or fleet-wide via vars:

```yaml
# In common.hnb or a node mapping
vars:
  scan_hour_start: "1"
  scan_hour_end: "3"
```

Each tool runs once per day. The sentinel file pattern (`/tmp/.hobnob-<tool>-YYYYMMDD`) prevents repeated scans within the same day.

## Customizing Trivy Severity

By default, Trivy scans for all severities. To limit:

```yaml
vars:
  trivy_severity: "CRITICAL,HIGH"
```

## AIDE Baseline

AIDE initializes its database on first run. After legitimate system changes, update the baseline:

```bash
aide --init && cp /var/lib/aide/aide.db.new /var/lib/aide/aide.db
```

Or push this via a Hobnob exec resource.

## Network-Based Scanners

Tools like OpenVAS/Greenbone, Nessus, Qualys, and Rapid7 InsightVM are centralized network scanners — they run on a dedicated host and scan targets remotely. They don't fit the local-agent-per-thrall model.

A separate `openvas.hnb.example` module is provided for installing Greenbone Community Edition on a scanner host. Its results are not collected by the `security_scan` trait collector.

For enterprise scanner integration, deploy the scanner's agent via a custom Hobnob module and optionally write a custom trait collector to surface results on the dashboard.
