
# Getting Started with Sandgorgon

## Prerequisites

- **vigosrv** running (Sandgorgon is built in -- no separate deployment).
- **BMC/Redfish access** from the vigosrv host to each machine's BMC endpoint (typically a dedicated management network). Redfish is the primary protocol; IPMI is used as a legacy fallback.
- **Network reachability** between vigosrv and BMC addresses on port 443 (Redfish) or 623 (IPMI).
- **BMC credentials** for each machine. Pass them per-command or store them as secrets (Phase 2+).

## Quickstart

### 1. Commission a machine

Register an existing machine by pointing Sandgorgon at its BMC address:

```bash
vigocli sandgorgon commission \
  --bmc 10.0.1.101 \
  --hostname web01.example.com \
  --serial "SN-2024-00451" \
  --model "Dell PowerEdge R660" \
  --rack "DC1-R04-U12" \
  --bmc-user admin \
  --bmc-pass "secret:sandgorgon/bmc/default"
```

If the hostname matches an enrolled Vigo envoy, the asset is automatically linked and its state is set to `commissioned`. Otherwise, the asset is created in `available` state.

### 2. Check fleet status

```bash
vigocli sandgorgon status
```

Output:

```
HOSTNAME                  STATE           BMC ADDRESS     MODEL
web01.example.com         commissioned    10.0.1.101      Dell PowerEdge R660
db01.example.com          active          10.0.1.102      Dell PowerEdge R760
gpu03.example.com         decommissioning 10.0.1.103      Supermicro SYS-421GE
```

### 3. Decommission a machine

Trigger a full decommission workflow -- cert revocation, disk sanitization via BMC, and NIST 800-88 certificate generation:

```bash
vigocli sandgorgon decommission web01.example.com \
  --technician "Jane Doe" \
  --method purge
```

The `--method` flag selects the NIST 800-88 sanitization category: `clear`, `purge`, or `destroy`. Defaults to `purge`. The actual sanitization command sent to each drive depends on its media type (NVMe Sanitize, SATA Secure Erase, SED Crypto Erase, or fallback wipe image).

### 4. View the certificate

```bash
vigocli sandgorgon certificate 7
```

Output includes the NIST 800-88 certificate fields: serial number, asset tag, make/model, sanitization method per drive, verification method and result, technician name, and timestamp.

To export:

```bash
vigocli sandgorgon certificate 7 --export html > cert-web01.html
vigocli sandgorgon certificate 7 --json
```

### 5. Bulk import (optional)

If you have an existing hardware inventory, import it from CSV:

```bash
vigocli sandgorgon import --source csv --file /srv/vigo/assets.csv
```

See [Import](import.md) for the CSV format specification.

---
*Confidential -- Alexander4, LLC. Not for redistribution.*
