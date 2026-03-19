# CIS Benchmark Modules for Vigo

Pre-built `.vgo` modules enforcing CIS Benchmark Level 1 and Level 2 controls. Each module maps to a CIS Benchmark section and includes the control ID in resource names for traceability.

## Structure

```
cis-benchmarks/
├── ubuntu/          # CIS Ubuntu Linux 22.04 LTS Benchmark v2.0.0
│   ├── cis-filesystem.vgo
│   ├── cis-services.vgo
│   ├── cis-network.vgo
│   ├── cis-auth.vgo
│   ├── cis-logging.vgo
│   └── cis-permissions.vgo
├── rhel/            # CIS Red Hat Enterprise Linux 9 Benchmark v2.0.0
│   ├── cis-filesystem.vgo
│   ├── cis-services.vgo
│   ├── cis-network.vgo
│   ├── cis-auth.vgo
│   ├── cis-logging.vgo
│   └── cis-permissions.vgo
└── windows/         # CIS Microsoft Windows Server 2022 Benchmark v2.0.0
    ├── cis-account-policies.vgo
    ├── cis-audit-policy.vgo
    └── cis-security-options.vgo
```

## Usage

1. Copy the modules for your OS into your policy directory:
   ```bash
   cp examples/cis-benchmarks/ubuntu/*.vgo /srv/vigo/policy/stage/modules/
   ```

2. Create a CIS role in `roles.vgo`:
   ```yaml
   roles:
     - name: cis-level1
       modules:
         - cis-filesystem
         - cis-services
         - cis-network
         - cis-auth
         - cis-logging
         - cis-permissions
   ```

3. Assign to nodes and publish:
   ```yaml
   # In your node mappings
   envoys:
     - match: "*.prod.example.com"
       roles: [cis-level1]
   ```

## Level 1 vs Level 2

- **Level 1** — Practical security for most environments. Minimal performance impact. All checks in these modules are Level 1 unless noted.
- **Level 2** — Defense-in-depth for high-security environments. May impact functionality. Marked with `# L2` comments where included.

## Control ID Format

Each resource is named with the CIS control ID for audit traceability:

```yaml
- name: "1.1.1-disable-cramfs"    # CIS control 1.1.1
  type: exec
  ...
```

This makes it easy to map Vigo compliance reports to CIS audit worksheets.
