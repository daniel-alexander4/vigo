
# vigocli risk

Display fleet-wide or per-envoy risk scores computed from CVEs, hardening, rootkits, integrity, open ports, convergence status, and connectivity.

## Usage

```bash
vigocli risk [hostname] [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Output raw JSON |
| `--history` | `false` | Show risk trend over time |
| `--days` | `30` | Number of days for `--history` |
| `--export` | `""` | Export insurance report (`json` or `html`) |
| `-o, --output` | `""` | Output file (default: stdout) |

## Fleet summary (no arguments)

Shows the fleet-wide risk distribution and the highest-risk envoys.

```bash
vigocli risk
```

```
Fleet Risk Summary

  Scored Envoys:  12 / 14
  Average Score:  23
  Highest Score:  78

Distribution
  critical   ██ 2
  high       ███ 3
  medium     ████ 4
  low        ███ 3

Top Risks
  HOSTNAME                        SCORE  LEVEL
  db01.prod.example.com              78  critical
  web03.prod.example.com             65  high
  app02.prod.example.com             42  high
```

## Per-envoy breakdown

Shows the risk score for a single envoy with a breakdown of contributing factors.

```bash
vigocli risk db01.prod.example.com
```

```
db01.prod.example.com  78  critical
  FACTOR                         PTS  DETAIL
  Critical CVEs                   30  3 critical CVEs
  High CVEs                       15  3 high CVEs
  Hardening Gap                   18  Lynis score 82/100
  Convergence Failed              15  last run had failures
```

## Risk history

Show the fleet risk trend over time.

```bash
vigocli risk --history
```

```
Risk Trend (30 days)

  DATE        AVG   MAX   LOW  MED  HIGH  CRIT
  2026-03-21   23    78     3    4     3     2
  2026-03-20   25    80     2    5     3     2
  2026-03-19   24    79     3    4     3     2
  ...
```

Show 90 days of history:

```bash
vigocli risk --history --days 90
```

## Insurance report export

Export a cyber insurance attestation report to stdout:

```bash
vigocli risk --export json
```

Save an HTML report to a file:

```bash
vigocli risk --export html -o report.html
```

The insurance report includes the current risk posture, a 90-day trend summary, and fleet composition (OS distribution). The HTML version is self-contained and print-friendly.

## Risk scoring

Risk scores range from 0 (lowest risk) to 100 (highest risk), capped at 100.

### Factor weights

| Factor | Points | Source |
|--------|--------|--------|
| Critical CVEs | +10 per CVE | `security_scan.trivy.critical` |
| High CVEs | +5 per CVE | `security_scan.trivy.high` |
| Medium CVEs | +2 per CVE | `security_scan.trivy.medium` |
| Hardening gap | 100 − Lynis score | `security_scan.lynis.score` |
| Rootkit (chkrootkit) | +25 per infection | `security_scan.chkrootkit.infected` |

| Rootkit (rkhunter) | +25 per warning | `security_scan.rkhunter.warnings` |
| Malware (ClamAV) | +25 per file | `security_scan.clamav.infected` |
| Integrity breach (AIDE) | +15 | `security_scan.aide.changed` |
| Open ports | +1 per port | `ports.listening.*` |
| Degraded | +10 | convergence status |
| Diverged | +15 | convergence status |
| Convergence errors | +15 | convergence status |
| Lost contact | +20 | last seen > 2.5× check-in interval |
| Windows update CVEs | +5 per CVE | `security_scan.windows_updates.cves` |
| Debian CVEs (debsecan) | +3 per CVE | `security_scan.debsecan.cves` |

### Risk levels

| Level | Score range |
|-------|------------|
| Low | 0 -- 19 |
| Medium | 20 -- 39 |
| High | 40 -- 69 |
| Critical | 70 -- 100 |

## Related

- [cve](cve.md) -- search for hosts affected by a specific CVE
- [report](report.md) -- generate compliance reports
- [doctor](doctor.md) -- run health checks
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
