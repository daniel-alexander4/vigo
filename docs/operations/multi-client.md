
# Multi-Client, Multi-Environment Configuration

Managing infrastructure for multiple clients across multiple environments is one of the hardest problems in configuration management. This guide explains why the problem is difficult, how it creates real operational pain, and how Vigo solves it without custom code or complex abstractions.

## The Two-Axis Problem

Most infrastructure has two independent dimensions that vary simultaneously:

- **Client axis** — different customers, business units, or tenants. Each has its own domain names, TLS certificates, DNS servers, alert contacts, and compliance requirements.
- **Environment axis** — dev, staging, production. Each has different performance tuning, security hardening, logging verbosity, and module sets.

These axes are independent. "Acme's production web servers" combines one client (Acme) with one environment (production) and one function (web). Every machine in your fleet sits at an intersection of these dimensions.

The challenge: how do you define configuration once per axis instead of once per intersection?

## Why This Gets Complicated

### The naive approach: one directory per combination

The most obvious layout creates a directory for every client-environment pair:

```
stockpile/
  acme-dev/
    envoys.vgo
  acme-staging/
    envoys.vgo
  acme-prod/
    envoys.vgo
  globex-dev/
    envoys.vgo
  globex-staging/
    envoys.vgo
  globex-prod/
    envoys.vgo
```

With 3 clients and 3 environments, that's 9 directories. Each `envoys.vgo` repeats the same modules with minor variations. When you add a fourth client, you create 3 more directories. When you add a QA environment, you touch every client.

The real cost shows up in maintenance:

- **Security patch to the hardening module?** You update the module once, but you need to verify it's assigned in every production directory. Miss one and a client's prod fleet is unhardened.
- **New monitoring agent?** Add it to 9 directories, or create a role and hope everyone remembers to assign it.
- **Client onboarding?** Copy an existing client's directories, rename everything, and pray you didn't miss a hardcoded domain name in a var file.
- **Audit request: "Show me every production machine's security posture."** You're grepping across 3 separate directories with no single source of truth for what "production" means.

This is the **combinatorial explosion**: N clients x M environments x P functions = N*M*P match blocks that are mostly identical. A managed service provider with 20 clients, 3 environments, and 5 server functions would need 300 near-duplicate entries.

### The template approach: generate configs with code

Some teams solve this with scripts or templating engines that generate the YAML from a higher-level definition. This works but introduces its own problems:

- **The config you edit is no longer the config that runs.** Debugging requires tracing through the generation layer.
- **Generated YAML is hard to diff.** A one-line change to a template produces a sprawling diff across generated files.
- **New team members must learn the generator** in addition to the config format itself.

### What other tools do

- **Ansible** uses inventory groups and `group_vars/` directories. Two-axis inheritance requires nested group membership (`[acme:children]` containing `[acme_prod]`) and careful variable precedence across 22 levels.
- **Puppet** uses Hiera with a configurable hierarchy. You define a lookup order like `"clients/%{client}/environments/%{environment}"` and scatter data files across a deep directory tree.
- **Chef** uses environments, roles, and data bags as separate concepts with separate APIs, each with its own merge behavior.

All of these work, but they require understanding complex precedence rules and maintaining parallel hierarchies that can drift out of sync.

## How Vigo Handles It

Vigo solves two-axis inheritance with two mechanisms that already exist in the config system, composed together:

| Axis | Mechanism | Where it lives |
|------|-----------|----------------|
| **Client** | Directory hierarchy | `acme/`, `globex/`, `initech/` subdirectories |
| **Environment** | Roles | `env-prod`, `env-stage`, `env-dev` in `roles.vgo` |
| **Function** | Roles | `webserver`, `database`, `cache` in `roles.vgo` |
| **Fleet-wide baseline** | Root `common.vgo` | `stockpile/common.vgo` |

No new features, no scripting, no templating for control flow. Each piece of information exists in exactly one place. When you change it, every affected machine picks it up on the next convergence cycle.

## Walkthrough: Setting Up Multi-Client Config from Scratch

This walkthrough builds a complete config for an MSP managing two clients (Acme Corp and Globex Industries), each with production and staging environments, running web servers and databases.

### Step 1: Define the fleet-wide baseline

Every machine in your fleet — regardless of client or environment — needs basic infrastructure: time sync, SSH hardening, and monitoring.

```yaml
# stockpile/common.vgo
modules:
  - ntp
  - sshd-config
  - monitoring
vars:
  ntp_server: pool.ntp.org
  ssh_permit_root: false
```

This file sits at the root of the config tree. All subdirectories inherit these modules and vars automatically. You never need to repeat `ntp` or `sshd-config` in any match block — every machine gets them for free.

### Step 2: Define environment roles

Environments differ in two ways: which modules they include and how variables are tuned. Capture both in environment roles.

```yaml
# stockpile/roles.vgo
roles:
  # ── Environment roles ─────────────────────────────────────
  #
  # These define what's DIFFERENT about each environment.
  # Every client shares them — "production" means the same thing
  # regardless of which client owns the machine.

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

  # ── Function roles ────────────────────────────────────────
  #
  # These define what a machine DOES. A web server needs nginx
  # and logrotate. A database needs postgres and backups.
  # Function is independent of client and environment.

  base:
    modules:
      - ntp
      - sshd-config
      - monitoring

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
```

Notice that environment roles contain only the modules that are specific to that environment. They don't repeat `ntp` or `monitoring` — those come from the root `common.vgo` and the `base` role.

### Step 3: Create client directories with shared config

Each client gets a directory. The `common.vgo` inside it defines modules, vars, and settings that apply to all of that client's machines — across all environments.

```yaml
# stockpile/acme/common.vgo
modules:
  - acme-dns
  - acme-tls-certs

vars:
  client_name: Acme Corp
  alert_email: ops@acme.com
  ntp_server: ntp.acme.com     # overrides the fleet default
  domain_suffix: acme.com
```

```yaml
# stockpile/globex/common.vgo
modules:
  - globex-dns

vars:
  client_name: Globex Industries
  alert_email: infra@globex.net
  domain_suffix: globex.net
```

Acme has a custom TLS cert management module that Globex doesn't use. Acme overrides the fleet NTP server with their own. These differences are captured once, not repeated in every match block.

### Step 4: Map nodes to roles

Each client's `envoys.vgo` maps hostname patterns to a combination of environment role + function role. This is where the two axes meet.

```yaml
# stockpile/acme/envoys.vgo
envoys:
  # ── Production ────────────────────────────────────────────
  - match: "web*.prod.acme.com"
    environment: production
    roles: [env-prod, webserver]
    vars:
      workers: 16
      ssl_cert: secret:acme/prod/tls/cert
      ssl_key: secret:acme/prod/tls/key

  - match: "db*.prod.acme.com"
    environment: production
    roles: [env-prod, database]
    vars:
      pg_max_connections: 200
      backup_schedule: "0 2 * * *"
      backup_bucket: secret:acme/prod/backup/bucket

  # ── Staging ───────────────────────────────────────────────
  - match: "web*.stage.acme.com"
    environment: staging
    roles: [env-stage, webserver]
    vars:
      workers: 4
      ssl_cert: secret:acme/stage/tls/cert
      ssl_key: secret:acme/stage/tls/key

  - match: "db*.stage.acme.com"
    environment: staging
    roles: [env-stage, database]
    vars:
      pg_max_connections: 20
      backup_schedule: "0 4 * * 0"
```

```yaml
# stockpile/globex/envoys.vgo
envoys:
  # ── Production ────────────────────────────────────────────
  - match: "web*.prod.globex.net"
    environment: production
    roles: [env-prod, webserver]
    vars:
      workers: 8
      ssl_cert: secret:globex/prod/tls/cert
      ssl_key: secret:globex/prod/tls/key

  - match: "db*.prod.globex.net"
    environment: production
    roles: [env-prod, database]
    vars:
      pg_max_connections: 100
      backup_schedule: "0 3 * * *"

  # ── Dev ───────────────────────────────────────────────────
  - match: "*.dev.globex.net"
    environment: development
    roles: [env-dev, webserver, database]
    vars:
      workers: 1
      pg_max_connections: 10
```

The last Globex entry is worth noting: dev machines get both the `webserver` and `database` roles because Globex runs everything on one box in dev. Roles compose freely — you're not locked into one role per machine.

### Step 5: Verify the result

Use `vigocli config trace` to see exactly what a specific machine receives and where every module and variable came from:

```
$ vigocli config trace web01.prod.acme.com

Match: "web*.prod.acme.com" (acme/envoys.vgo)
Environment: production

Inheritance chain:
  stockpile/common.vgo        → ntp, sshd-config, monitoring
  stockpile/acme/common.vgo   → acme-dns, acme-tls-certs

Role expansion:
  env-prod                      → hardening, log-shipping, auditd
  webserver (includes: base)    → ntp, sshd-config, monitoring, nginx, logrotate

Final modules (deduplicated):
  ntp, sshd-config, monitoring, acme-dns, acme-tls-certs,
  hardening, log-shipping, auditd, nginx, logrotate

Variable sources:
  ntp_server    = "ntp.acme.com"     (acme/common.vgo — overrides root)
  client_name   = "Acme Corp"        (acme/common.vgo)
  alert_email   = "ops@acme.com"     (acme/common.vgo)
  workers       = 16                 (acme/envoys.vgo inline)
  ssl_cert      = secret:acme/prod/tls/cert  (acme/envoys.vgo inline)
```

The trace makes the inheritance visible. You can see that `ntp` appears in three places (root common, base role, webserver role via includes) but is deduplicated to one instance. You can see that `ntp_server` was overridden by the client's `common.vgo`.

## What the Final Layout Looks Like

```
stockpile/
├── common.vgo              # Fleet baseline: ntp, sshd-config, monitoring
├── roles.vgo               # Environment roles + function roles
├── modules/
│   ├── ntp.vgo
│   ├── sshd-config.vgo
│   ├── monitoring.vgo
│   ├── hardening.vgo
│   ├── log-shipping.vgo
│   ├── auditd.vgo
│   ├── debug-tools.vgo
│   ├── nginx.vgo
│   ├── logrotate.vgo
│   ├── postgres.vgo
│   ├── backup-agent.vgo
│   ├── acme-dns.vgo        # Client-specific module
│   ├── acme-tls-certs.vgo  # Client-specific module
│   └── globex-dns.vgo      # Client-specific module
├── acme/
│   ├── common.vgo          # Acme-wide: modules, vars, overrides
│   └── envoys.vgo           # Acme match blocks
└── globex/
    ├── common.vgo          # Globex-wide: modules, vars, overrides
    └── envoys.vgo           # Globex match blocks
```

Two clients, two to three environments each, two server functions — and the entire config tree is 5 YAML files plus modules. No duplication. No generation. No code.

## Day-to-Day Operations

The real test of a config system is what happens when things change. Here's how common operations work with this layout.

### Add a new client

Create two files:

```yaml
# stockpile/newclient/common.vgo
modules:
  - newclient-dns
vars:
  client_name: New Client Inc
  alert_email: ops@newclient.com
```

```yaml
# stockpile/newclient/envoys.vgo
envoys:
  - match: "*.prod.newclient.com"
    environment: production
    roles: [env-prod, webserver]
    vars:
      workers: 8
```

That's it. The new client inherits the fleet baseline, the environment hardening, and the webserver module set. No changes to any existing files.

### Add a new environment

Add one role to `roles.vgo`:

```yaml
  env-qa:
    modules:
      - log-shipping
      - name: load-test-agent
        when: "has_trait('tags', 'load-test')"
```

Then add entries to each client's `envoys.vgo` that uses it:

```yaml
  - match: "*.qa.acme.com"
    environment: qa
    roles: [env-qa, webserver]
    vars:
      workers: 2
```

The role definition captures what "QA" means across the fleet. Each client just references it.

### Change shared infrastructure

Update a module once. For example, switching from rsyslog to Vector for log shipping:

```yaml
# modules/log-shipping.vgo
name: log-shipping
resources:
  - name: vector-package
    type: package
    package: vector
  # ... rest of module
```

Every production and staging machine across every client picks up the change on the next convergence cycle. You don't touch `roles.vgo`, any `common.vgo`, or any `envoys.vgo`.

### Override a fleet default for one client

Acme uses their own NTP server. Set it in `acme/common.vgo`:

```yaml
vars:
  ntp_server: ntp.acme.com
```

This overrides the root `common.vgo` value of `pool.ntp.org` for all Acme machines. Globex and future clients still use the fleet default.

### Exclude an inherited module for specific machines

Acme runs some containers that shouldn't get NTP (they use the host clock):

```yaml
# acme/envoys.vgo
  - match: "docker*.prod.acme.com"
    environment: production
    roles: [env-prod]
    modules: [docker-engine]
    exclude_modules: [ntp]
```

The `exclude_modules` directive removes `ntp` from the inherited module list for these specific machines. Everything else they inherit stays intact.

## Using per_env_overrides.vgo for Variable-Only Differences

If environments differ only in variable values — not in which modules are assigned — put env-specific vars in `per_env_overrides.vgo` at the appropriate directory level. Each match block declares its `environment:`; the override file picks the right block by that key:

```yaml
# acme/envoys.vgo
envoys:
  - match: "prod-web*.acme.com"
    environment: production
    roles: [webserver]
  - match: "stg-web*.acme.com"
    environment: staging
    roles: [webserver]
  - match: "dev-web*.acme.com"
    environment: development
    roles: [webserver]
```

```yaml
# acme/per_env_overrides.vgo
production:
  vars:
    workers: 16
    log_level: warn
    debug: false
staging:
  vars:
    workers: 4
    log_level: debug
    debug: false
development:
  vars:
    workers: 1
    log_level: trace
    debug: true
```

The override file is scoped by filesystem location — `acme/per_env_overrides.vgo` only affects envoys whose matchfiles live at `acme/` or deeper. The same file is reused across every match block in the subtree; the tier is selected via each block's `environment:`.

**When to use `per_env_overrides.vgo` vs environment roles:**

| Situation | Use |
|-----------|-----|
| Environments differ in variable values only | `per_env_overrides.vgo` |
| Environments need different modules (hardening in prod, debug tools in dev) | Environment roles (`env-prod`, `env-dev`) |
| Both — different modules AND different vars | Environment roles + `per_env_overrides.vgo` |

You can combine both: environment roles cover module differences, `per_env_overrides.vgo` covers var tuning. No inline per-env overrides on individual match blocks — the cross-cutting env axis stays in its own file.

## Scaling Beyond Two Axes

As the fleet grows, additional patterns help manage complexity.

### Use role includes to avoid role explosion

If you find yourself creating roles like `prod-webserver`, `staging-webserver`, `prod-database`, `staging-database`, stop. That's the combinatorial explosion creeping back in.

Instead, keep environment and function as separate roles and compose them:

```yaml
roles: [env-prod, webserver]       # composition, not a new role
```

Role includes handle shared foundations without creating more combinations:

```yaml
roles:
  base:
    modules: [ntp, sshd-config, monitoring]
  webserver:
    includes: [base]
    modules: [nginx, logrotate]
  database:
    includes: [base]
    modules: [postgres, backup-agent]
```

### Use tags and lookup tables for per-host snowflakes

When a few machines need unique values that don't fit a pattern, tag them and use [lookup tables](../engineering/writing-modules/lookups.md) inside the relevant modules to vary field values by hostname or tag:

```yaml
  - match: "web01.prod.acme.com"
    environment: production
    roles: [env-prod, webserver]
    tags: [web, oversized]
    vars:
      workers: 32     # explicit override for this one host
```

For one-off values across many hosts, a lookup table inside the module keyed by `tag:oversized` or `hostname:web03.prod.acme.com` keeps all the variation in one place and keeps match blocks free of host-specific noise.

### Use conditional roles for cross-platform fleets

When a client runs mixed Linux and Windows:

```yaml
  - match: "*.prod.acme.com"
    environment: production
    roles:
      - env-prod
      - name: webserver-linux
        when: "os_family('debian') || os_family('redhat')"
      - name: webserver-windows
        when: "os_family('windows')"
```

One entry covers the entire production fleet. The agent's OS traits determine which role applies at check-in time.

## Common Mistakes

**Putting environment-specific modules in `common.vgo`.**
The `common.vgo` at a client's directory level applies to ALL of that client's machines — every environment. If you put `hardening` in `acme/common.vgo`, dev machines get hardened too. Use environment roles for environment-specific modules.

**Creating client-environment subdirectories.**
Don't make `acme/prod/` and `acme/staging/` directories. This recreates the combinatorial explosion. Keep each client flat with a single `envoys.vgo` that uses roles for environment differences.

**Duplicating module content between clients.**
If two clients need nginx but with different settings, don't create `acme-nginx` and `globex-nginx`. Use one `nginx` module with vars, and set the vars differently in each client's `envoys.vgo`.

**Forgetting that first match wins.**
In a client's `envoys.vgo`, more specific patterns must come before more general ones. A catch-all `"*.acme.com"` at the top would swallow all subsequent entries.

## Summary

The two-axis inheritance problem is solved by using two orthogonal mechanisms:

1. **Directories** separate clients. Each client's `common.vgo` captures client-specific shared config.
2. **Roles** separate environments and functions. Environment roles define what production/staging/dev means. Function roles define what a web server/database/cache does.
3. **Match blocks** compose roles at the intersection: `roles: [env-prod, webserver]`.
4. **Root `common.vgo`** captures the fleet-wide baseline that every machine gets.
5. **Module deduplication** ensures that modules inherited from multiple sources are applied exactly once.

Each piece of information lives in one place. Each change propagates automatically. The config trace shows exactly where every module and variable came from.

## Related

- [Composition Patterns](../engineering/writing-modules/composition-patterns.md) — All six composition layers in detail
- [Config Format](../engineering/writing-modules/config-format.md) — Module, role, and envoy structure reference
- [Spanner](../engineering/subsystems/spanner.md) — Hub-spoke scaling for multi-site deployments

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
