
# cert_lifecycle

The `cert_lifecycle` trait collector walks configured certificate directories, parses every PEM certificate in each candidate file (including concatenated-PEM bundles like `/etc/ssl/certs/ca-certificates.crt`), and emits per-certificate rows with CN, SAN list, issuer, serial, validity window, and days-remaining.

Parsing is pure-Rust via `x509-parser` — no `openssl` subprocess, so the collector is cheap enough to run on every host without violating the "invisible to host" rule.

## Scanned Directories

**Linux:**

- `/etc/ssl/certs`
- `/etc/ssl/private`
- `/etc/pki/tls/certs`
- `/etc/pki/tls/private`
- `/etc/kubernetes/pki`

**macOS:**

- `/etc/ssl/certs`
- `/private/etc/ssl`

**FreeBSD / OpenBSD / NetBSD / illumos:**

- `/etc/ssl`
- `/etc/ssl/certs`

Only files with `.pem`, `.crt`, or `.cer` extensions are parsed. Symlinks are skipped to avoid duplicate rows when distros use hash-linked trust stores.

Java keystores in JKS or PKCS#12 format are **not** parsed by this collector. Operators needing coverage of Java trust stores should export them to PEM via `keytool -exportcert -rfc` or use a dedicated JKS-capable tool.

## Trait Structure

```json
{
  "cert_lifecycle": {
    "certificates": [
      {
        "path": "/etc/ssl/certs/server.pem",
        "cn": "server.example.com",
        "subject": "CN=server.example.com",
        "sans": ["server.example.com", "www.server.example.com"],
        "issuer": "CN=Example CA",
        "serial": "01:ab:cd:ef",
        "not_before": "2026-04-01T00:00:00Z",
        "not_after": "2027-04-01T00:00:00Z",
        "expires": "2027-04-01T00:00:00Z",
        "days_remaining": 348,
        "is_ca": false
      }
    ]
  }
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `certificates[].path` | string | Absolute path to the certificate file |
| `certificates[].cn` | string | Common Name from the subject DN, empty when absent |
| `certificates[].subject` | string | Full subject distinguished name |
| `certificates[].sans` | []string | Subject Alternative Names (DNS names + IPv4/IPv6 addresses, empty when the extension is absent) |
| `certificates[].issuer` | string | Full issuer distinguished name |
| `certificates[].serial` | string | Serial number formatted as colon-separated hex |
| `certificates[].not_before` | string | RFC 3339 start-of-validity timestamp |
| `certificates[].not_after` | string | RFC 3339 end-of-validity timestamp |
| `certificates[].expires` | string | Alias for `not_after` kept for back-compat with the prior `cert_expiry` trait |
| `certificates[].days_remaining` | int | Days between now and `not_after`. Negative when the certificate has already expired |
| `certificates[].is_ca` | bool | `true` when the certificate has the CA basic-constraints bit set |

## Platform Support

| Platform | Supported | Method |
|----------|-----------|--------|
| Linux | Full | Pure-Rust PEM parsing via `x509-parser` |
| macOS | Basic | Pure-Rust PEM parsing; paths narrower than Linux |
| FreeBSD | Basic | Pure-Rust PEM parsing; paths narrower than Linux |
| OpenBSD | Basic | Pure-Rust PEM parsing; paths narrower than Linux |
| NetBSD | Basic | Pure-Rust PEM parsing; paths narrower than Linux |
| illumos | Basic | Pure-Rust PEM parsing; paths narrower than Linux |
| Windows | Not supported | Returns null |

## Use Cases

Alert on certificates expiring within 30 days fleet-wide:

```bash
vigocli inventory \
  --where "cert_lifecycle.certificates.0.days_remaining<30" \
  --show "hostname,cert_lifecycle.certificates.0.cn,cert_lifecycle.certificates.0.days_remaining"
```

Template fragment that emits every expiring cert on the local host:

```yaml
{{range .Traits.cert_lifecycle.certificates}}
{{if lt .days_remaining 30}}
WARNING: {{.cn}} at {{.path}} expires in {{.days_remaining}} days ({{.not_after}})
{{end}}
{{end}}
```

## Rename from `cert_expiry`

This collector replaces the earlier `cert_expiry` trait (0.23.x and earlier). The rename is clean — any `.vgo` templates or `vigocli inventory` queries referencing `cert_expiry.certificates.*` need updating to `cert_lifecycle.certificates.*`. The old top-level key is no longer emitted.

Fields added during the rename: `cn`, `sans`, `issuer`, `serial`, `not_before`, `not_after`, `is_ca`. The `expires` and `days_remaining` fields from `cert_expiry` are preserved for straightforward migration.

## Classification

**Stable** — certificate files change infrequently. Cached across convergence cycles.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../../legal/license.md).*
