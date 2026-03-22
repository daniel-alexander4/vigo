
---
title: CIS Benchmarks
---

# CIS Benchmarks

CIS (Center for Internet Security) Benchmarks are consensus-based security configuration guides. Vigo provides pre-built enforcement modules for Ubuntu 22.04, RHEL 9, and Windows Server 2022 covering **Level 1** controls across all major benchmark sections.

## Module Inventory

### Ubuntu 22.04 LTS (6 modules)

| Module | CIS Section | Controls |
|--------|-------------|----------|
| `cis-filesystem` | 1. Filesystem | Disable unused FS types, GRUB permissions, ASLR, core dumps |
| `cis-services` | 2. Services | Time sync, disable 15 unnecessary services, remove 5 insecure clients |
| `cis-network` | 3. Network | Sysctl hardening (15 parameters), disable 4 uncommon protocols |
| `cis-logging` | 4. Logging | auditd with 15+ rules (immutable), rsyslog with secure permissions |
| `cis-auth` | 5. Access/Auth | SSH hardening (20 settings), password policy, umask, shell timeout, lock system accounts |
| `cis-permissions` | 6. Maintenance | System file permissions, no empty passwords, home directory permissions |

### RHEL 9 (6 modules)

Same structure as Ubuntu with RHEL-specific differences:
- Package names (e.g., `audit` vs `auditd`, `httpd` vs `apache2`)
- Service names (e.g., `chronyd` vs `chrony`, `rsyncd` vs `rsync`)
- SSH groups (`wheel` vs `sudo`)
- Password quality (`pwquality.conf` with `minclass=4`)
- SELinux audit rules (vs AppArmor)

### Windows Server 2022 (3 modules)

| Module | CIS Section | Controls |
|--------|-------------|----------|
| `cis-account-policies` | 1. Account Policies | Password history (24), max age (365), min length (14), complexity, lockout (5/15min) |
| `cis-audit-policy` | 17. Audit Policy | 13 audit subcategories: credential validation, user mgmt, logon/logoff, process creation, policy change, system integrity |
| `cis-security-options` | 2/9/18. Security Options | Disable guest, SMB signing, WDigest disabled, inactivity timeout (900s), firewall enabled (all profiles), disable autoplay |

## Quick Start

### 1. Copy modules

```bash
# Ubuntu
cp /path/to/examples/cis-benchmarks/ubuntu/*.vgo /srv/vigo/stack/stage/modules/

# RHEL
cp /path/to/examples/cis-benchmarks/rhel/*.vgo /srv/vigo/stack/stage/modules/

# Windows (for Windows nodes)
cp /path/to/examples/cis-benchmarks/windows/*.vgo /srv/vigo/stack/stage/modules/
```

### 2. Create roles

```yaml
# roles.vgo
roles:
  - name: cis-ubuntu-l1
    modules:
      - cis-filesystem
      - cis-services
      - cis-network
      - cis-auth
      - cis-logging
      - cis-permissions

  - name: cis-rhel-l1
    modules:
      - cis-filesystem
      - cis-services
      - cis-network
      - cis-auth
      - cis-logging
      - cis-permissions

  - name: cis-windows-l1
    modules:
      - cis-account-policies
      - cis-audit-policy
      - cis-security-options
```

### 3. Assign to nodes

```yaml
# Node mappings
envoys:
  - match: "*.ubuntu.example.com"
    roles: [cis-ubuntu-l1]
  - match: "*.rhel.example.com"
    roles: [cis-rhel-l1]
  - match: "*.win.example.com"
    roles: [cis-windows-l1]
```

### 4. Publish and verify

```bash
vigogo config publish
vigogo report cis
```

## Customization

### Override variables per node

Most modules expose configurable variables:

```yaml
envoys:
  - match: "bastion.example.com"
    roles: [cis-ubuntu-l1]
    vars:
      ssh_allow_groups: "sshusers sudo ops"
      ssh_max_auth_tries: "6"
      pass_max_days: "90"
```

### Exclude specific controls

If a control conflicts with your environment, remove that resource from the module or use `when:` conditions:

```yaml
# Example: keep IPv6 enabled on specific hosts
envoys:
  - match: "ipv6-host.example.com"
    roles: [cis-ubuntu-l1]
    exclude_modules: [cis-network]  # Use a custom network module without IPv6 disable
```

### Level 2 controls

The provided modules focus on Level 1. To add Level 2 controls, copy a module and add the additional resources. Level 2 checks are marked with `# L2` in the benchmark and typically include:

- Mandatory SELinux/AppArmor enforcement
- Stricter audit rules (all privileged commands)
- Additional filesystem restrictions (separate /var, /tmp, /var/log partitions)
- USB device restrictions

## Generating CIS Reports

```bash
# JSON report
vigogo report cis

# HTML report
vigogo report cis --format html --output cis-report.html
```

## Control ID Format

Each resource is named with the CIS control ID for audit traceability:

```yaml
- name: "1.1.1.1-disable-cramfs"    # CIS Section 1.1.1.1
- name: "5.2.8-disable-root-login"  # CIS Section 5.2.8
- name: "W-1.1.4-min-password-len"  # CIS Windows Section 1.1.4
```

This makes it straightforward to map Vigo compliance reports to CIS audit worksheets.

## Cross-Reference to Other Frameworks

CIS Benchmarks are referenced by multiple compliance frameworks:

| CIS Section | PCI DSS | HIPAA | NIST 800-53 |
|-------------|---------|-------|-------------|
| 1 (Filesystem) | 2.2.5 | — | CM-7 |
| 2 (Services) | 2.2.4 | — | CM-7 |
| 3 (Network) | 1.2.1 | 164.312(e)(1) | SC-7 |
| 4 (Logging) | 10.2.1 | 164.312(b) | AU-2, AU-12 |
| 5 (Auth) | 8.3.6 | 164.312(d) | IA-5, AC-7 |
| 6 (Permissions) | 2.2.5 | 164.312(a)(1) | AC-3 |
