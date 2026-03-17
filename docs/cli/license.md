# hobnobcli license

View, install, and inspect the server license that governs thrall limits.

## Subcommands

### license status

Show current license tier, thrall usage, enforcement stage, and expiry.

```bash
hobnobcli license status
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

Thralls: 12 / 500
```

### license install

Copy a `.lic` file to the server's license directory and trigger a reload via SIGHUP.

```bash
hobnobcli license install --file /path/to/license.lic
```

**Flags:**

| Flag | Required | Description |
|------|----------|-------------|
| `--file` | Yes | Path to the `.lic` file |

The command validates the license file before installing and sends SIGHUP to the running `hobd` process for live reload.

### license inspect

Decode and display a `.lic` file without contacting the server. If no file is specified, inspects the installed license.

```bash
hobnobcli license inspect /path/to/license.lic
hobnobcli license inspect    # inspect installed license
```

### license fingerprint

Display this server's machine fingerprint for license binding. Send this fingerprint when requesting a paid license.

```bash
hobnobcli license fingerprint
hobnobcli license fingerprint --ca-file /srv/hobnob/tls/ca
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-file` | `/srv/hobnob/tls/ca` | Path to TLS CA certificate |

The fingerprint is `SHA256(machine-id + primary-MAC + CA-cert-fingerprint)`. It changes on OS reinstall, primary NIC replacement, or CA cert regeneration.

## See Also

- [Licensing](../operations/licensing.md) -- tiers, enforcement stages, and machine fingerprint
- [REST API: License](../api/rest.md#license)
