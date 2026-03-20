
# vigogo license

View, install, and inspect the server license that governs envoy limits.

## Subcommands

### license status

Show current license tier, envoy usage, enforcement stage, and expiry.

```bash
vigogo license status
```

**Output:**

```
Enforcement Stage: Compliant

License:
  License ID:  lic-20260313-acme
  Customer:    Acme Corp (acme)
  Tier:        enterprise
  Version:     1
  Issued:      2026-03-13
  Expires:     2027-03-13
  Days left:   365

Envoys: 12 / 500
```

### license install

Copy a `.lic` file to the server's license directory and trigger a reload via SIGHUP.

```bash
vigogo license install --file /path/to/license.lic
```

**Flags:**

| Flag | Required | Description |
|------|----------|-------------|
| `--file` | Yes | Path to the `.lic` file |

The command validates the license file before installing and sends SIGHUP to the running `vigosrv` process for live reload.

### license inspect

Decode and display a `.lic` file without contacting the server. If no file is specified, inspects the installed license.

```bash
vigogo license inspect /path/to/license.lic
vigogo license inspect    # inspect installed license
```

### license fingerprint

Display this server's machine fingerprint for license binding. Send this fingerprint when requesting a paid license.

```bash
vigogo license fingerprint
vigogo license fingerprint --ca-file /srv/vigo/tls/ca
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-file` | `/srv/vigo/tls/ca` | Path to TLS CA certificate |

The fingerprint is `SHA256(machine-id + primary-MAC + CA-cert-fingerprint)`. It changes on OS reinstall, primary NIC replacement, or CA cert regeneration.

## See Also

- [Licensing](../operations/licensing.md) -- tiers, enforcement stages, and machine fingerprint
- [REST API: License](../api/rest.md#license)
