# cve

Search the fleet for hosts affected by a specific CVE.

## Usage

```bash
vigocli security cve <CVE-ID>
```

## Description

Scans all enrolled envoys' security scan traits for the given CVE ID. Returns a table of affected hosts with package name, installed version, fix version (if available), severity, and scanner source (Trivy, Debsecan, or Windows Update).

The search is case-insensitive and checks traits from all security scanners.

## Example

```
$ vigocli security cve CVE-2024-1234

  CVE-2024-1234 — 3 host(s) affected

  HOSTNAME        PACKAGE     INSTALLED  FIXED    SEVERITY  SCANNER
  web01.prod      openssl     1.1.1      3.0.0    CRITICAL  trivy
  web02.prod      openssl     1.1.1      3.0.0    CRITICAL  trivy
  db01.prod       libssl1.1   1.1.1      1.1.1w   HIGH      debsecan
```

## API

```
GET /api/v1/cve/{cveID}
```

Returns JSON:

```json
{
  "cve_id": "CVE-2024-1234",
  "affected_hosts": [
    {
      "hostname": "web01.prod",
      "envoy_id": "abc-123",
      "package": "openssl",
      "installed": "1.1.1",
      "fixed": "3.0.0",
      "severity": "CRITICAL",
      "scanner": "trivy"
    }
  ],
  "total_hosts": 1
}
```

## Web UI

A CVE Impact Analysis search box is available on the Security page. Enter a CVE ID and click "Search Fleet" to see results inline.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [license](../../legal/license.md).*
