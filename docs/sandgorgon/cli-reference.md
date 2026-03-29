
# Sandgorgon CLI Reference

All commands are subcommands of `vigogo sandgorgon`. They communicate with vigosrv via the REST API and require a valid API token or localhost peer auth.

## commission

Register a machine's BMC with Sandgorgon.

```
vigogo sandgorgon commission [flags]
```

| Flag | Required | Description |
|------|----------|-------------|
| `--bmc` | Yes | BMC IP address or hostname |
| `--hostname` | No | Machine hostname. If it matches an enrolled envoy, the asset is linked automatically |
| `--serial` | No | Hardware serial number |
| `--model` | No | Hardware model (e.g., "Dell PowerEdge R660") |
| `--tag` | No | Asset tag |
| `--rack` | No | Rack location (e.g., "DC1-R04-U12") |
| `--bmc-user` | No | BMC username |
| `--bmc-pass` | No | BMC password. Supports `secret:` prefix for secrets provider resolution |

Example:

```bash
vigogo sandgorgon commission \
  --bmc 10.0.1.101 \
  --hostname web01.example.com \
  --serial "SN-2024-00451" \
  --model "Dell PowerEdge R660" \
  --rack "DC1-R04-U12" \
  --bmc-user admin \
  --bmc-pass "secret:sandgorgon/bmc/default"
```

## decommission

Trigger a full decommission workflow for a machine: envoy cert revocation, disk sanitization via BMC, verification, and NIST 800-88 certificate generation.

```
vigogo sandgorgon decommission <target> [flags]
```

`<target>` is a hostname or asset ID.

| Flag | Required | Description |
|------|----------|-------------|
| `--technician` | No | Name of the technician performing the decommission. Recorded on the NIST 800-88 certificate |
| `--method` | No | NIST 800-88 sanitization category: `clear`, `purge`, or `destroy`. Default: `purge` |

Example:

```bash
vigogo sandgorgon decommission web01.example.com \
  --technician "Jane Doe" \
  --method purge
```

## certificate

View or export a single NIST 800-88 certificate.

```
vigogo sandgorgon certificate <id> [flags]
```

`<id>` is the certificate ID (integer).

| Flag | Required | Description |
|------|----------|-------------|
| `--export` | No | Export format: `html`. Writes to stdout |
| `--json` | No | Output as JSON |

Examples:

```bash
# View certificate in terminal
vigogo sandgorgon certificate 7

# Export as HTML
vigogo sandgorgon certificate 7 --export html > cert-web01.html

# Export as JSON
vigogo sandgorgon certificate 7 --json
```

## certificates

List all NIST 800-88 certificates.

```
vigogo sandgorgon certificates
```

Example output:

```
ID  HOSTNAME              METHOD  TECHNICIAN    DATE
7   web01.example.com     purge   Jane Doe      2026-03-15T14:30:00Z
6   db02.example.com      clear   Mike Chen     2026-03-10T09:15:00Z
5   gpu01.example.com     purge   Jane Doe      2026-02-28T16:45:00Z
```

## status

Show lifecycle state of all managed hardware assets.

```
vigogo sandgorgon status
```

Example output:

```
HOSTNAME                  STATE           BMC ADDRESS     MODEL
web01.example.com         commissioned    10.0.1.101      Dell PowerEdge R660
db01.example.com          active          10.0.1.102      Dell PowerEdge R760
gpu03.example.com         decommissioning 10.0.1.103      Supermicro SYS-421GE
stor01.example.com        retired         10.0.1.104      Dell PowerEdge R760
spare02.example.com       available       10.0.1.105      HP ProLiant DL380
```

## import

Bulk import hardware assets from an external source.

```
vigogo sandgorgon import [flags]
```

| Flag | Required | Description |
|------|----------|-------------|
| `--source` | Yes | Import source: `csv` (Phase 1). Future: `netbox`, `snipe-it` |
| `--file` | Yes | Path to the import file (for `csv` source) |

Example:

```bash
vigogo sandgorgon import --source csv --file /srv/vigo/assets.csv
```

See [Import](import.md) for the CSV format specification and matching rules.

## events

Show lifecycle events for a specific asset.

```
vigogo sandgorgon events <target>
```

`<target>` is a hostname or asset ID.

Example:

```bash
vigogo sandgorgon events web01.example.com
```

Example output:

```
TIMESTAMP                 EVENT               DETAILS
2026-03-15T14:30:00Z      decommissioned      Technician: Jane Doe, Method: purge
2026-03-15T14:29:45Z      sanitization_done   All drives verified
2026-03-15T14:25:00Z      sanitization_start  3 drives, method: purge
2026-03-15T14:24:50Z      cert_revoked        Envoy certificate revoked
2025-11-01T10:00:00Z      commissioned        BMC: 10.0.1.101
```

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
