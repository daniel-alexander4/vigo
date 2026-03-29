
# Multi-Client, Multi-Environment Config

How to manage multiple clients (tenants) across multiple environments (dev, stage, prod) without duplicating config.

## The Problem

An MSP manages three clients. Each has dev, stage, and prod environments. Naive config would require 9 copies of every module:

```
acme-dev/   acme-stage/   acme-prod/
globex-dev/ globex-stage/ globex-prod/
initech-dev/ initech-stage/ initech-prod/
```

That's unmaintainable. A monitoring change requires editing 9 files.

## The Solution: Roles as the Cross-Cutting Axis

Use directories for clients and roles for environments + function. Each axis stays DRY.

### Directory layout

```
stack/stage/
  common.vgo                  # modules every machine gets (ntp, sshd, monitoring)
  roles.vgo                   # environment + function roles
  modules/
    ntp.vgo
    sshd-config.vgo
    monitoring.vgo
    log-shipping.vgo
    hardening.vgo
    debug-tools.vgo
    nginx.vgo
    postgres.vgo
    ...
  acme/
    common.vgo                # Acme-specific modules/vars (DNS, certs, branding)
    nodes.vgo                 # Acme node-to-role mappings
  globex/
    common.vgo                # Globex-specific modules/vars
    nodes.vgo
  initech/
    common.vgo
    nodes.vgo
```

### Environment roles

Define roles that capture what's different about each environment. These are shared across all clients.

```yaml
# roles.vgo
roles:
  # ── Environment roles ──
  env-prod:
    modules:
      - hardening
      - log-shipping
      - name: auditd
        when: "os_family('debian') || os_family('redhat')"

  env-stage:
    modules:
      - log-shipping

  env-dev:
    modules:
      - debug-tools

  # ── Function roles ──
  webserver:
    includes: [base]
    modules:
      - nginx
      - logrotate

  database:
    includes: [base]
    modules:
      - postgres
      - backup-agent

  base:
    modules:
      - ntp
      - sshd-config
      - monitoring
```

### Client node mappings

Each client's `nodes.vgo` assigns two roles: an environment role + a function role. Vars handle client-specific values.

```yaml
# acme/nodes.vgo
- match: "web*.prod.acme.com"
  environment: production
  roles: [env-prod, webserver]
  vars:
    domain: acme.com
    ssl_cert: secret:acme/tls/cert
    workers: 16

- match: "web*.stage.acme.com"
  environment: staging
  roles: [env-stage, webserver]
  vars:
    domain: staging.acme.com
    workers: 4

- match: "web*.dev.acme.com"
  environment: development
  roles: [env-dev, webserver]
  vars:
    domain: dev.acme.com
    workers: 1

- match: "db*.prod.acme.com"
  environment: production
  roles: [env-prod, database]
  vars:
    pg_max_connections: 200
    backup_schedule: "0 2 * * *"

- match: "db*.dev.acme.com"
  environment: development
  roles: [env-dev, database]
  vars:
    pg_max_connections: 20
```

### Client-specific shared config

Each client's `common.vgo` defines modules, vars, or roles that apply to all of that client's machines.

```yaml
# acme/common.vgo
modules:
  - acme-dns        # client-specific DNS records
  - acme-tls-certs  # client-specific TLS certs

vars:
  client_name: Acme Corp
  alert_email: ops@acme.com
  ntp_server: ntp.acme.com
```

### What each machine gets

A production web server at `web01.prod.acme.com` receives:

| Source | Modules |
|--------|---------|
| `stack/stage/common.vgo` | ntp, sshd-config, monitoring |
| `acme/common.vgo` | acme-dns, acme-tls-certs |
| `env-prod` role | hardening, log-shipping, auditd |
| `webserver` role (includes base) | ntp, sshd-config, monitoring, nginx, logrotate |

Deduplicated: `ntp, sshd-config, monitoring, acme-dns, acme-tls-certs, hardening, log-shipping, auditd, nginx, logrotate`.

### Adding a client

1. Create `stack/stage/newclient/common.vgo` with client-specific vars
2. Create `stack/stage/newclient/nodes.vgo` with node mappings
3. Reuse existing roles — no module changes needed

### Adding an environment

1. Create an `env-qa` role in `roles.vgo` with the appropriate modules
2. Add entries to each client's `nodes.vgo` with `roles: [env-qa, webserver]`

### Changing shared infrastructure

Change the `hardening` module once. Every production machine across every client picks it up on next convergence. No per-client edits.

## Using environment_overrides for Variable-Only Differences

If environments differ only in variable values (not modules), use `environment_overrides` instead of separate environment roles:

```yaml
# acme/nodes.vgo
- match: "web*.acme.com"
  roles: [webserver]
  vars:
    workers: 1
    log_level: info
    debug: false
  environment_overrides:
    production:
      workers: 16
      log_level: warn
    staging:
      workers: 4
      log_level: debug
    development:
      workers: 1
      log_level: trace
      debug: true
```

One entry covers all three environments. The `environment` field on the envoy's check-in selects the override. This only works for vars — if different environments need different modules, use environment roles.

## When This Isn't Enough

If the number of role combinations grows unwieldy (e.g., 5 environments x 10 functions = 50 node entries per client), consider:

- **Role includes** to compose roles from smaller pieces without repeating module lists
- **`vars_from`** to pull client-specific or environment-specific variable files without duplicating entries
- **`exclude_modules`** on node entries to opt out of inherited modules without creating a new role

The goal: each piece of information exists in exactly one place. When you change it, every affected machine converges automatically.

## Related

- [Composition Patterns](composition-patterns.md) — Full guide to all composition layers
- [Config Format](config-format.md) — Module, role, and envoy structure
- [Spanner](spanner.md) — Hub-spoke scaling for multi-site deployments

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
