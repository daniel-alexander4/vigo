
---
title: CIS Benchmarks
---

# CIS Benchmarks

CIS (Center for Internet Security) Benchmarks are consensus-based security configuration guides. Vigo provides pre-built enforcement modules for **Ubuntu**, **RHEL** (including CentOS, Rocky, Alma), and **Windows Server** covering **Level 1 + Level 2** controls across all major benchmark sections.

Each platform has its own compliance framework (`cis-ubuntu`, `cis-rhel`, `cis-windows`) with dedicated report pages, per-envoy coverage tracking, and distro-specific module sets.

## Module Inventory

### Ubuntu (5 modules, 100+ controls)

| Module | CIS Section | Key Controls |
|--------|-------------|-------------|
| `cis-ubuntu-filesystem` | 1. Filesystem | Disable 8 unused FS types, GRUB permissions, ASLR, ptrace, core dumps, disable apport |
| `cis-ubuntu-services` | 2. Services | Chrony NTP, disable 15 unnecessary services, remove 6 insecure clients |
| `cis-ubuntu-network` | 3. Network | 15 sysctl parameters, disable 4 uncommon protocols, UFW default deny |
| `cis-ubuntu-logging` | 4. Logging | auditd with 17 rule sets (immutable), rsyslog with 0640 permissions, log file hardening |
| `cis-ubuntu-access` | 5–6. Access/Auth/Maintenance | SSH hardening (20 settings), PAM pwquality + faillock, cron restrictions, password aging, umask, TMOUT, system file permissions |

Applies to: Ubuntu 20.04, 22.04, 24.04

### RHEL / CentOS / Rocky / Alma (5 modules, 100+ controls)

| Module | CIS Section | Key Controls |
|--------|-------------|-------------|
| `cis-rhel-filesystem` | 1. Filesystem | Disable 8 unused FS types, GRUB2/EFI permissions, ASLR, ptrace, core dumps (systemd + limits) |
| `cis-rhel-services` | 2. Services | Chrony NTP, disable 11 unnecessary services, remove 5 insecure clients |
| `cis-rhel-network` | 3. Network | 15 sysctl parameters, disable 4 uncommon protocols, firewalld default zone drop |
| `cis-rhel-logging` | 4. Logging | auditd with 17 rule sets (immutable), audit_backlog_limit, rsyslog 0640, log file hardening |
| `cis-rhel-access` | 5–6. Access/Auth/Maintenance | SSH hardening (20 settings), authselect sssd profile, pwquality + faillock, SELinux enforcing, cron restrictions, password aging, umask, TMOUT, system file permissions |

Applies to: RHEL 8/9, CentOS Stream 8/9, Rocky Linux 8/9, AlmaLinux 8/9

### Windows Server (8 modules, 60+ controls)

| Module | CIS Section | Key Controls |
|--------|-------------|-------------|
| `cis-windows-account-policies` | 1. Account Policies | Password history (24), max age (365), min length (14), complexity, lockout (5/15min) |
| `cis-windows-audit-policies` | 17. Audit Policy | 8 audit subcategories: credential validation, security group, logon/logoff, process creation, policy change, system integrity |
| `cis-windows-firewall` | 9. Windows Firewall | All 3 profiles enabled (domain, private, public), default inbound block |
| `cis-windows-security-options` | 2/18. Security Options | Disable guest, logon banner, SMB signing, Defender real-time + behavior monitoring |
| `cis-windows-user-rights` | 2.2. User Rights | Deny guest network/batch/service/local/RDP logon, restrict shutdown/debug/audit/ownership |
| `cis-windows-network` | 18.4. Network Security | Disable LLMNR/NetBIOS/WPAD/SMBv1/WDigest, SMB signing, LDAP signing, NTLMv2 only |
| `cis-windows-system-services` | 5. System Services | Disable Print Spooler, Remote Registry, Xbox, Windows Search, Bluetooth, SSDP, UPnP |
| `cis-windows-components` | 18. Windows Components | RDP NLA + encryption, WinRM encryption, PowerShell logging, disable AutoPlay/AutoRun, inactivity lock, Credential Guard |

Applies to: Windows Server 2019, 2022

## Quick Start

### 1. Copy modules

```bash
# Copy the pre-built compliance roles (includes cis-ubuntu, cis-rhel, cis-windows)
cp envoy-configs/compliance-roles.vgo.example /srv/vigo/stack/stage/compliance-roles.vgo

# Copy modules for your platforms
# Ubuntu
cp envoy-configs/modules/compliance/cis-ubuntu/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/cis-ubuntu-*.vgo.example; do mv "$f" "${f%.example}"; done

# RHEL
cp envoy-configs/modules/compliance/cis-rhel/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/cis-rhel-*.vgo.example; do mv "$f" "${f%.example}"; done

# Windows
cp envoy-configs/modules/compliance/cis-windows/*.vgo.example /srv/vigo/stack/stage/modules/
for f in /srv/vigo/stack/stage/modules/cis-windows-*.vgo.example; do mv "$f" "${f%.example}"; done
```

### 2. Use the pre-built roles

The `compliance-roles.vgo.example` file (copied in step 1) includes `cis-ubuntu`, `cis-rhel`, and `cis-windows` roles. No need to create them manually.

### 3. Assign to nodes

For a single-OS fleet:

```yaml
# nodes.vgo
envoys:
  - match: "*.example.com"
    roles: [cis-ubuntu]
```

For a mixed-OS fleet, use conditional role references to auto-select per OS:

```yaml
# nodes.vgo
envoys:
  - match: "*.example.com"
    roles:
      - name: cis-ubuntu
        when: "distro('ubuntu')"
      - name: cis-rhel
        when: "distro('rhel') || distro('centos') || distro('rocky') || distro('alma')"
      - name: cis-windows
        when: "os_family('windows')"
```

### 4. Publish and verify

```bash
vigogo config publish
# Check compliance reports
vigogo report cisubuntu
vigogo report cisrhel
vigogo report ciswindows
```

## Customization

### Override variables per node

Most modules expose configurable variables:

```yaml
envoys:
  - match: "bastion.example.com"
    roles: [cis-ubuntu-l1]
    vars:
      ssh_port: "2222"
      max_auth_tries: "6"
      pass_max_days: "90"
      shell_timeout: "600"
      ntp_server: "ntp.internal.example.com"
```

Available variables per module:

| Variable | Default | Used By |
|----------|---------|---------|
| `ssh_port` | 22 | access modules |
| `max_auth_tries` | 4 | access modules |
| `client_alive_interval` | 300 | access modules |
| `client_alive_count_max` | 3 | access modules |
| `login_grace_time` | 60 | access modules |
| `pass_max_days` | 365 | access modules |
| `pass_min_days` | 1 | access modules |
| `pass_warn_age` | 7 | access modules |
| `pass_min_len` | 14 | access modules |
| `shell_timeout` | 900 | access modules |
| `ntp_server` | time.cloudflare.com | services modules |
| `audit_max_log_file` | 8 | logging modules |
| `audit_space_left_action` | email | logging modules |

### Exclude specific controls

If a control conflicts with your environment:

```yaml
envoys:
  - match: "router.example.com"
    roles: [cis-ubuntu-l1]
    exclude_modules: [cis-ubuntu-network]  # This host needs IP forwarding
```

### Level 1 vs Level 2

The modules include both Level 1 and Level 2 controls. Level 2 controls that may impact functionality are marked in comments:

- **Filesystem:** squashfs and USB storage blacklisting (Level 2) — may break snap packages or USB devices
- **Services:** Windows Search disabled (Level 2) — may impact search functionality
- **Network:** Credential Guard (Level 2) — requires virtualization support

To exclude Level 2 controls, copy the module and remove the marked resources.

## Compliance Reports

Each platform has its own compliance framework and report:

```bash
# Web UI
# Navigate to Compliance → CIS Ubuntu / CIS RHEL / CIS Windows

# REST API
curl https://vigo.example.com/api/v1/report/cisubuntu
curl https://vigo.example.com/api/v1/report/cisrhel
curl https://vigo.example.com/api/v1/report/ciswindows

# HTML (printable)
curl https://vigo.example.com/api/v1/report/cisubuntu.html
```

## RHEL-Specific Differences

Key differences from Ubuntu modules:

| Area | Ubuntu | RHEL |
|------|--------|------|
| Firewall | UFW | firewalld |
| MAC | AppArmor | SELinux |
| PAM | libpam-pwquality | authselect + pwquality |
| Cron service | cron | crond |
| NTP service | chrony | chronyd |
| Shadow perms | 640 root:shadow | 000 root:root |
| GRUB path | /boot/grub/grub.cfg | /boot/grub2/grub.cfg or EFI |
| Audit package | auditd | audit |
| Network config | /etc/netplan/ | /etc/sysconfig/network-scripts/ |
| Crash reporter | apport (disabled) | abrtd (not targeted) |

## Control ID Format

Each resource is named with the CIS control ID for audit traceability:

```yaml
- name: "1.1.1.1-disable-cramfs"    # CIS Section 1.1.1.1
- name: "5.2.10-no-root-login"      # CIS Section 5.2.10
```

This makes it straightforward to map Vigo compliance reports to CIS audit worksheets.

## Cross-Reference to Other Frameworks

CIS Benchmarks are referenced by multiple compliance frameworks. Each CIS module carries cross-reference compliance tags:

| CIS Section | PCI DSS | HIPAA | NIST 800-53 | ISO 27001 | SOC 2 |
|-------------|---------|-------|-------------|-----------|-------|
| 1 (Filesystem) | 2.2.1, 2.2.5 | — | CM-6, CM-7, SC-7 | — | — |
| 2 (Services) | 2.2.2, 2.2.3 | — | CM-7 | — | — |
| 3 (Network) | 1.3.1, 1.3.2, 2.2.1 | 164.312(e)(1) | SC-7, CM-6 | A.13.1.1 | — |
| 4 (Logging) | 10.2.1–10.2.7 | 164.312(b) | AU-2, AU-3, AU-12 | A.12.4.1, A.12.4.3 | CC7.2 |
| 5 (Auth) | 2.2.1, 8.2.3, 8.3.1 | 164.312(d), 164.312(e)(1) | AC-17, IA-2, IA-5, SC-8 | A.9.2.1, A.9.4.1 | — |
| 6 (Permissions) | 2.2.1, 7.1.1 | — | CM-6, AC-3 | — | — |

Assigning CIS modules to an envoy automatically contributes to coverage scores for all cross-referenced frameworks.
