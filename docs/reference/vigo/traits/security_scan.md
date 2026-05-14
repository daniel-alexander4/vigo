
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
    "unique_critical": 14,
    "unique_high": 157,
    "unique_medium": 174,
    "unique_low": 7930,
    "trivy": {
      "critical": 120,
      "high": 1553,
      "medium": 1804,
      "low": 436,
      "total": 3913,
      "last_scan": "2026-05-10T13:01:54Z"
    },
    "debsecan": {
      "high": 0,
      "medium": 12,
      "low": 7500,
      "unassigned": 1,
      "total": 7513,
      "last_scan": "2026-05-10T12:58:00Z"
    },
    "lynis": {
      "score": 82
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
        "packages": 1,
        "title": "openssl: buffer overflow in X.509 name verification"
      }
    ]
  }
}
```

### Two CVE-count vocabularies — read this before comparing numbers

| Keys | What they count | Surfaced where |
|------|-----------------|----------------|
| `unique_critical` / `unique_high` / `unique_medium` / `unique_low` | **Distinct CVE IDs per severity**, deduplicated across *every* scanner that ran (Trivy + debsecan + Windows-updates), highest-severity-wins on dedup. Computed from the full deduped set — **not** capped by the `cves` list limit below. This is the operator-meaningful "how many distinct vulns does this host have" figure. | `/security` per-host "Unique CVEs by Host" table; the per-host "Scan Details" drawer's lead row; the envoy-detail attack-surface badges; the risk scorer's CVE factors. **All read these keys** — they agree. |
| `trivy.{critical,high,medium,low,total}` · `debsecan.{high,medium,low,unassigned,total}` · `windows_updates.{...}` | **Raw per-scanner package-level match counts** — a CVE affecting *N* packages counts *N* times, and each scanner is independent (no cross-scanner dedup). Trivy's `critical` is therefore typically *larger* than `unique_critical` (package multiplicity); debsecan floods `low`/`unassigned` with the full Debian backlog, so `unique_low` can be far *larger* than `trivy.low`. | The per-host "Scan Details" drawer only, under each scanner's own labelled row ("Trivy — N package-level matches", "debsecan — …"). Never aggregated fleet-wide. |

The cross-scanner `cves` list is capped at 200 entries (severity-desc, then CVE-ID-desc). Each entry carries the highest severity seen across scanners, the summed affected-package count, and a one-line `title` (NVD short title from Trivy / the update title on Windows; omitted when no scanner supplied one — debsecan output has no titles). Titles are trimmed to a single line, max 160 characters. When a host has more than 200 distinct CVEs, the `unique_*` counts stay exact but the listed CVEs are only the top 200 by severity (the long tail — all `low` — is dropped from the list); the drawer says "showing first 200", and the only figure that genuinely undercounts is the *fleet-wide* "Unique CVEs" stat (server-side dedup can only see each host's top 200).

Trait key constants for all `security_scan.*` keys live in `server/traits/keys.go` — UI and server code reads through those, never hardcoded strings.

## Classification

**Volatile** -- security scan results change as packages are updated and scans are re-run. Collected every cycle.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
