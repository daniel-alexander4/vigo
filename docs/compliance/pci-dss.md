---
title: PCI DSS v4.0 Compliance
---

# PCI DSS v4.0 Compliance

PCI DSS (Payment Card Industry Data Security Standard) v4.0 defines security requirements for systems that store, process, or transmit cardholder data. Hobnob maps **42 PCI DSS requirements** across 9 of the 12 requirement categories and provides **4 enforcement modules** for automated hardening.

## Coverage Summary

| Requirement | Title | Controls Mapped | Enforcement Module |
|-------------|-------|----------------|-------------------|
| Req 1 | Network Security Controls | 4 | `pci-network` |
| Req 2 | Secure Configurations | 6 | `pci-hardening` |
| Req 3 | Protect Stored Data | 2 | — |
| Req 5 | Protect from Malicious Software | 3 | — (use `security-scanning` module) |
| Req 6 | Develop Secure Systems | 3 | — |
| Req 7 | Restrict Access | 3 | — |
| Req 8 | Identify Users | 6 | `pci-hardening` (PAM/timeout) |
| Req 10 | Log and Monitor | 9 | `pci-audit` |
| Req 11 | Test Security | 3 | `pci-fim` |
| Req 12 | Security Policies | 3 | — |

## Enforcement Modules

Hobnob provides four PCI-specific enforcement modules in `thrall-configs/modules/compliance/`:

### pci-hardening

Enforces Req 2.2 (secure configurations):
- Disables unnecessary services (avahi, cups, rpcbind)
- Locks default accounts (games, lp)
- Applies sysctl hardening (IP forwarding, source routing, SYN flood protection, ASLR)
- Configures PAM faillock (account lockout after 5 attempts)
- Sets shell idle timeout (15 minutes)
- Removes insecure remote access (telnet, rsh)

```yaml
# In your role definition:
roles:
  - name: pci-cardholder
    modules:
      - pci-hardening
      - pci-audit
      - pci-fim
      - pci-network
      - openssh
      - chrony
```

**Customizable vars:**

| Variable | Default | Description |
|----------|---------|-------------|
| `pam_deny_count` | `5` | Failed logins before lockout |
| `pam_unlock_time` | `900` | Lockout duration (seconds) |
| `tmout_seconds` | `900` | Shell idle timeout (seconds) |

### pci-audit

Enforces Req 10.2 (audit logging):
- Monitors cardholder data directory access
- Logs all administrative/privileged actions
- Tracks audit trail access attempts
- Records identity and authentication changes
- Monitors file creation/deletion
- Makes audit rules immutable (requires reboot to change)

**Customizable vars:**

| Variable | Default | Description |
|----------|---------|-------------|
| `cde_data_dir` | `/var/data/cardholder` | Cardholder data directory to monitor |

### pci-fim

Enforces Req 11.5 (file integrity monitoring):
- Installs and configures AIDE
- Monitors critical system binaries, configs, and cardholder data
- Daily integrity checks via cron (3:30 AM)
- Initializes baseline database on first run

This works alongside Hobnob's built-in drift detection, which also functions as FIM for all managed files.

**Customizable vars:**

| Variable | Default | Description |
|----------|---------|-------------|
| `aide_db_path` | `/var/lib/aide/aide.db` | AIDE database location |
| `cde_data_dir` | `/var/data/cardholder` | Cardholder data directory |

### pci-network

Enforces Req 1 (network security):
- Default-deny inbound firewall policy
- Whitelisted TCP/UDP ports only
- Drops and logs unauthorized traffic
- Persists rules across reboots

**Customizable vars:**

| Variable | Default | Description |
|----------|---------|-------------|
| `allowed_tcp_ports` | `22 443` | Space-separated allowed TCP ports |
| `allowed_udp_ports` | (empty) | Space-separated allowed UDP ports |
| `log_dropped` | `true` | Log dropped packets |

Override per node to allow application-specific ports:

```yaml
# In nodes.hnb
thralls:
  - match: "web-*.prod.example.com"
    vars:
      allowed_tcp_ports: "22 80 443"
  - match: "db-*.prod.example.com"
    vars:
      allowed_tcp_ports: "22 5432"
```

## Generating PCI DSS Reports

### CLI

```bash
# JSON report
hobnobcli report pci

# HTML report (printable, Hobnob-branded)
hobnobcli report pci --format html --output pci-report.html
```

### REST API

```bash
# JSON
curl -s https://hobnob:8443/api/v1/report/pci | jq .

# HTML
curl -s https://hobnob:8443/api/v1/report/pci.html > pci-report.html
```

## Deployment Checklist

1. Copy the PCI modules to your policy directory:
   ```bash
   cp /path/to/thrall-configs/modules/compliance/pci-*.hnb.example \
      /srv/hobnob/policy/stage/modules/
   # Rename .hnb.example to .hnb
   for f in /srv/hobnob/policy/stage/modules/pci-*.hnb.example; do
     mv "$f" "${f%.example}"
   done
   ```

2. Create a PCI role in `roles.hnb`:
   ```yaml
   roles:
     - name: pci-cardholder
       modules:
         - pci-hardening
         - pci-audit
         - pci-fim
         - pci-network
         - openssh
         - chrony
   ```

3. Assign the role to PCI-scope nodes in your node mappings:
   ```yaml
   thralls:
     - match: "*.pci.example.com"
       roles: [pci-cardholder]
       vars:
         allowed_tcp_ports: "22 443 8443"
         cde_data_dir: "/var/data/cardholder"
   ```

4. Publish and verify:
   ```bash
   hobnobcli config publish
   # Wait for agents to check in, then verify compliance
   hobnobcli report pci
   ```

## Hobnob as File Integrity Monitoring (Req 11.5)

Hobnob provides two layers of FIM:

1. **AIDE module** (`pci-fim`) — traditional FIM tool, daily scans, detects changes to all system files including unmanaged ones
2. **Built-in drift detection** — every managed file is checked on each agent check-in (default every 5 minutes); changes trigger compliance alerts via webhook/SMTP

Together, these satisfy Req 11.5.1 (FIM deployed) and Req 11.5.1.1 (FIM alerts on unauthorized changes).

## Cross-Reference

| PCI DSS | HIPAA | HITRUST | SOC 2 | Description |
|---------|-------|---------|-------|-------------|
| 1.2.1 | 164.312(e)(1) | 01.n | CC5.2 | Network security |
| 2.2.5 | — | 09.a | CC5.1 | Secure configuration |
| 8.3.6 | 164.308(a)(5)(ii)(D) | 01.d | CC6.7 | Password complexity |
| 8.3.9 | 164.312(d) | 01.j | CC6.1 | MFA |
| 10.2.1 | 164.312(b) | 09.q | CC7.3 | Audit logging |
| 10.3.1 | 164.312(b) | 09.s | CC7.3 | Log protection |
| 11.5.1 | — | 06.g | CC7.1 | File integrity monitoring |

## Requirements Not Covered

PCI DSS requirements that need additional tools or organizational processes:

| Requirement | Title | Why |
|-------------|-------|-----|
| Req 4 | Protect Data in Transit (cardholder) | Application-level encryption of card data |
| Req 9 | Physical Access | Physical security controls |
| Req 3.3-3.4 | Mask/Render PAN Unreadable | Application-level PAN masking |
| Req 6.2 | Secure Development | SDLC process, not infrastructure config |
| Req 11.4 | Penetration Testing | External assessment service |
