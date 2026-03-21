
# vigogo risk

Display fleet-wide or per-envoy risk scores computed from CVEs, hardening, rootkits, integrity, open ports, convergence status, and connectivity.

## Usage

```bash
vigogo risk [hostname] [flags]
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | `false` | Output raw JSON |

## Fleet summary (no arguments)

Shows the fleet-wide risk distribution and the highest-risk envoys.

```bash
vigogo risk
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
vigogo risk db01.prod.example.com
```

```
db01.prod.example.com  78  critical

  FACTOR                         PTS  DETAIL
  Critical CVEs                   30  3 critical CVEs
  High CVEs                       15  3 high CVEs
  Hardening Gap                   18  Lynis score 82/100
  Convergence Errors              15  last run had failures
```

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
| Recurring drift | +10 | convergence status |
| Convergence errors | +15 | convergence status |
| Lost contact | +20 | last seen > 2× check-in interval |
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
