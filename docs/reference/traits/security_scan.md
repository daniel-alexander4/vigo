
# security_scan

The `security_scan` trait collector reads output from security scanning tools and emits summary traits. Each tool section is only present if its output file exists -- tools that are not installed produce no data.

## Scan Output Directory

| Platform | Path |
|----------|------|
| Unix | `/var/lib/vigo/security/` |
| Windows | `C:\ProgramData\Vigo\security\` |

## Supported Tools

| Tool | Platform | Description |
|------|----------|-------------|
| `trivy` | All | Container and OS vulnerability scanner (CVE counts by severity) |
| `lynis` | Unix | System hardening auditor (hardening score 0-100) |
| `chkrootkit` | Unix | Rootkit detection (infected count) |
| `rkhunter` | Unix | Rootkit hunter (warnings count) |
| `clamav` | Unix | Antivirus (infected file count) |
| `aide` | Unix | File integrity monitoring (changed/added/removed counts) |
| `debsecan` | Debian | Debian security tracker (CVE counts by severity) |
| `windows_defender` | Windows | Windows Defender status and threat counts |
| `windows_updates` | Windows | Pending Windows Update counts |

## Trait Structure

```json
{
  "security_scan": {
    "trivy": {
      "critical": 2,
      "high": 5,
      "medium": 12,
      "low": 3,
      "total": 22
    },
    "lynis": {
      "hardening_score": 82
    },
    "chkrootkit": {
      "infected": 0
    },
    "aide": {
      "changed": 0,
      "added": 0,
      "removed": 0
    },
    "cves": [
      {
        "id": "CVE-2024-1234",
        "severity": "critical",
        "packages": 1
      }
    ]
  }
}
```

## Classification

**Volatile** -- security scan results change as packages are updated and scans are re-run. Collected every cycle.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
