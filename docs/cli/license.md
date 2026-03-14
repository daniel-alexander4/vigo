# hobnobcli license

Manage the Hobnob license, including installation, status checking, and offline inspection.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `install` | Install a license file on this server |
| `status` | Show license status from the running server |
| `inspect` | Decode and display a license file (offline, no server needed) |

## Usage

```bash
hobnobcli license [subcommand] [flags]
```

## install

Install a license file on the server. Validates the license, checks expiry, copies it to the license directory, and signals the running `hobd` process for live reload via SIGHUP.

```bash
hobnobcli license install --file <path>
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--file` | | | Path to the `.lic` file (required) |

### Example

```bash
hobnobcli license install --file ./hobnob-enterprise.lic
```

```
License installed: /etc/hobnob-server/licenses/hobnob-enterprise.lic
  License ID:  lic-a1b2c3d4
  Customer:    Acme Corp
  Tier:        Enterprise
  Max thralls: 500
  Expires:     2027-03-13

Server notified — license reloaded.
```

## status

Show the current license status from the running server, including enforcement stage and violation details.

```bash
hobnobcli license status
```

### Example

```bash
hobnobcli license status
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

Thralls: 12 / 500
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
hobnobcli license inspect [file]
```

If no file is specified, inspects the license in the default license directory.

### Example

```bash
hobnobcli license inspect ./hobnob-enterprise.lic
```

```
SIGNATURE: VALID

License ID:    lic-a1b2c3d4
Customer:      Acme Corp (cust-001)
Tier:          Enterprise
Max thralls:   500
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

## Related

- [doctor](doctor.md) -- server health checks
