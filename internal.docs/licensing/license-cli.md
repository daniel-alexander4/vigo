# vigocli license

Manage the Vigo license, including installation, status checking, and offline inspection.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `install` | Install a license file on this server |
| `status` | Show license status from the running server |
| `inspect` | Decode and display a license file (offline, no server needed) |
| `fingerprint` | Display the machine fingerprint for license binding |

## Usage

```bash
vigocli license [subcommand] [flags]
```

## install

Install a license file on the server. Validates the license, checks expiry, copies it to the license directory, and signals the running `vigosrv` process for live reload via SIGHUP.

```bash
vigocli license install --file <path>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--file` | | | Path to the `.lic` file (required) |

### Example

```bash
vigocli license install --file ./vigo-enterprise.lic
```

```
License installed: /srv/vigo/licenses/vigo-enterprise.lic
  License ID:  lic-a1b2c3d4
  Customer:    Acme Corp
  Tier:        Enterprise
  Max envoys: 500
  Expires:     2027-03-13

Server notified — license reloaded.
```

## status

Show the current license status from the running server, including enforcement stage and violation details.

```bash
vigocli license status
```

### Example

```bash
vigocli license status
```

```
Enforcement Stage: Compliant

License:
  License ID:  lic-a1b2c3d4
  Customer:    Acme Corp (cust-001)
  Tier:        Enterprise
  Version:     1
  Issued:      2026-01-01
  Expires:     2027-03-13
  Days left:   365

Envoys: 12 / 500
```

### Enforcement stages

| Stage | Description |
|-------|-------------|
| Compliant | License valid, within limits |
| Grace Period | License violation detected, 15-day grace period |
| Enrollment Block | New enrollments blocked, 15 days remaining |
| Service Stop | Server stops accepting traffic after 30 days |

## inspect

Decode and display a license file without needing a running server. Useful for verifying license files before installation.

```bash
vigocli license inspect [file]
```

If no file is specified, inspects the license in the default license directory.

### Example

```bash
vigocli license inspect ./vigo-enterprise.lic
```

```
SIGNATURE: VALID

License ID:    lic-a1b2c3d4
Customer:      Acme Corp (cust-001)
Tier:          Enterprise
Max envoys:   500
Issued:        2026-01-01
Expires:       2027-03-13
Version:       1
Status:        Valid (365 days remaining)

Raw payload:
  {
    "license_id": "lic-a1b2c3d4",
    "customer_name": "Acme Corp",
    "tier": "enterprise",
    ...
  }
```

## fingerprint

Display the SHA256 machine fingerprint used for paid license binding. The fingerprint is computed from the machine ID, primary MAC address, and CA certificate fingerprint.

```bash
vigocli license fingerprint
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--ca-file` | | `/srv/vigo/tls/ca` | Path to the CA certificate file |

### Example

```bash
vigocli license fingerprint
```

```
Machine fingerprint: a1b2c3d4e5f6...
```

Send this fingerprint when requesting a paid license. Free tier licenses do not require a fingerprint.

```bash
# Use a custom CA file path
vigocli license fingerprint --ca-file /etc/vigo/tls/ca
```

## Related

- [doctor](doctor.md) -- server health checks
