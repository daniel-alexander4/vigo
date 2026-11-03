
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
| `certificates[].is_ca` | bool | `true` when the certificate has the CA basic-constraints bit set |

Days-remaining is **not** a field: it is a pure function of `not_after` and the current time, so emitting it would change every collection and force a needless re-send. The server derives it from `not_after` for the dashboard's expiring-certs card, the per-envoy certificate drill-down, and risk scoring.

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

**Expiring-cert alerting** is computed server-side from `not_after` (days-remaining
is not a trait — see the note under Fields). The fleet dashboard's Security
Posture card surfaces expired / ≤7-day / ≤30-day counts with a per-envoy
drill-down, and an expiring cert raises the envoy's risk score — no query needed.

To inspect a specific cert's validity window from a template, use `not_after`
directly:

```yaml
{{range .Traits.cert_lifecycle.certificates}}
{{.cn}} at {{.path}} — valid until {{.not_after}}
{{end}}
```

## Rename from `cert_expiry`

This collector replaces the earlier `cert_expiry` trait (0.23.x and earlier). The rename is clean — any `.vgo` templates or `vigocli inventory` queries referencing `cert_expiry.certificates.*` need updating to `cert_lifecycle.certificates.*`. The old top-level key is no longer emitted.

Fields added during the rename: `cn`, `sans`, `issuer`, `serial`, `not_before`, `not_after`, `is_ca`. The `expires` field from `cert_expiry` is preserved for straightforward migration; `days_remaining` is no longer emitted (the server derives it from `not_after`).

## Classification

**Stable** — certificate files change infrequently. Cached across convergence cycles.
