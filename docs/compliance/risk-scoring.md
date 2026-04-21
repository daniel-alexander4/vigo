
# Risk Scoring

Vigo computes a numeric risk score (0 – 100) for every enrolled envoy, aggregating inputs from security scans, convergence state, and connectivity signals into a single posture number. Scores drive the dashboard Risk Posture column, the cyber-insurance attestation PDF, and compliance-visible drift signals.

## How the score is computed

The scorer lives in `server/risk/` and runs off the hot path (computed on demand when the dashboard or API is queried; not on every check-in). It takes an `EnvoyInput` struct (ID, hostname, convergence status, last-seen time, trait map) and returns an `EnvoyRisk` with a score, level, and per-factor breakdown.

Inputs and point contributions (approximate weights):

| Input | Signal | Points |
|-------|--------|--------|
| CVE count | critical / high / medium / low unresolved CVEs from the security scan | up to 40 |
| Hardening | Lynis score (inverted — lower Lynis = more risk points) | up to 15 |
| Integrity | AIDE or Tripwire mismatches reported | 10 – 15 |
| Rootkit / malware | chkrootkit, rkhunter, ClamAV warnings | 10 – 20 |
| Ports | unexpected listeners outside the allowlist | up to 15 |
| Convergence | diverged, relapsed, or failed status | 15 – 20 |
| Connectivity | envoy offline relative to its expected check-in interval | up to 10 |

Revoked envoys score 0 by definition — they're no longer part of the fleet.

## Levels

Scores map to four human-readable levels:

| Level | Score | Meaning |
|-------|-------|---------|
| Low | 0 – 19 | No meaningful issues. Healthy. |
| Medium | 20 – 39 | Minor issues worth tracking but not urgent. |
| High | 40 – 69 | Should be investigated and remediated on a normal cadence. |
| Critical | 70 – 100 | Needs immediate attention. Shows in the Attention column on the dashboard. |

These bands are defined in `classify()` in `server/risk/score.go`.

## NVD enrichment

The scorer can optionally enrich CVE input with NIST NVD data (CVSS base score, attack vectors, vendor advisories). When enabled, CVE severity comes from NVD's authoritative record instead of relying solely on the scanner's classification.

```yaml
risk:
  nvd:
    enabled: true
    api_key: "secret:vigo/nvd/api_key"
```

NVD API keys are free; request one at <https://nvd.nist.gov/developers/request-an-api-key>. Without a key, unauthenticated NVD access is still possible but is heavily rate-limited (5 requests / 30 seconds) and not recommended for fleets of more than a handful of envoys.

## Where scores appear

- **Dashboard Risk Posture column** — fleet-wide average score (sparkline pinned 0–100), critical/high/medium distribution counts.
- **`/risk` page** — per-envoy list sorted by score, with factor breakdown on drill-down.
- **Cyber-insurance attestation PDF** (`/api/v1/risk/insurance.pdf`) — formal attestation document with current posture and 90-day trend, designed for insurance underwriting and audit evidence.
- **Per-envoy detail page** — risk score badge in the envoy header, factors listed under the Risk tab.

## History

Daily snapshots of fleet-wide aggregates are written to the `risk_posture_history` table and drive the 30-day trend sparkline on the dashboard. Per-envoy score history is not retained separately; the current score is always the authoritative view.

## See also

- [Server-config > risk](../getting-started/server-config.md#risk) — config reference
- [Cyber-insurance attestation endpoint](../reference/api/rest.md) — the `insurance.pdf` route
- [Compliance](overview.md) — separate but related: framework control coverage
