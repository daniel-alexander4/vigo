
# Multi-Axis Configuration

Vigo manages fleets where machines vary across multiple dimensions simultaneously. A single machine might be: client Acme, production environment, webserver function, running on GCP, Debian OS, in us-east region, under PCI compliance. Each axis is handled by a different Vigo mechanism -- no combinatorial explosion, no duplication.

## The Four Mechanisms

| Axis type | Mechanism | Example |
|-----------|-----------|---------|
| **Client/tenant** | Directory tree | `clients/acme/`, `clients/globex/` |
| **Environment** | `per_env_overrides.vgo` | production, staging, dev |
| **Function** | Roles | webserver, database, cache |
| **Infrastructure facts** | `when:` expressions on traits | Cloud provider, OS, region, architecture |

### Directory tree -- client/tenant axis

The directory hierarchy under `stockpile/` defines inheritance. `common.vgo` at any level applies modules, roles, and vars to all entries in subdirectories.

```
stockpile/
  common.vgo                     # fleet-wide baseline
  clients/
    acme/
      common.vgo                 # Acme-specific modules and vars
      per_env_overrides.vgo      # Acme's per-environment overrides
      envoys.vgo                  # Acme's node match patterns
    globex/
      common.vgo                 # Globex-specific modules and vars
      per_env_overrides.vgo      # Globex's per-environment overrides
      envoys.vgo
```

### per_env_overrides.vgo -- environment axis

A `per_env_overrides.vgo` file at any directory level defines variable and module overrides keyed by environment name. The `environment:` field on each match block determines which block applies.

```yaml
# per_env_overrides.vgo
env:
  production:
    modules: [waf, hardening, audit-logging]
    vars:
      worker_count: "auto"
      log_level: "warn"
      backup_schedule: "0 */4 * * *"

  staging:
    modules: [debug-tools]
    exclude_modules: [hardening]
    vars:
      worker_count: "2"
      log_level: "debug"

  dev:
    modules: [debug-tools, mock-services]
    exclude_modules: [hardening, audit-logging]
    vars:
      worker_count: "1"
      log_level: "debug"
```

Match blocks reference the environment:

```yaml
# envoys.vgo
nodes:
  - match: "web-*.prod.acme.com"
    environment: production
    roles: [webserver]

  - match: "web-*.staging.acme.com"
    environment: staging
    roles: [webserver]
```

The environment override merges with the node's modules and vars. `exclude_modules` removes specific modules inherited from `common.vgo` or roles.

**Precedence (last wins):**
1. Module default vars
2. `common.vgo` vars (directory inheritance)
3. Node-level vars
4. `per_env_overrides.vgo` vars for the matching environment

### Roles -- function axis

Roles group modules into reusable profiles. A machine's function is assigned via roles on the match block or in `common.vgo`.

```yaml
# roles.vgo
roles:
  - name: webserver
    modules: [nginx, certbot, logrotate]

  - name: database
    modules: [postgres, pg-backup, pg-monitoring]

  - name: pci-hardened
    modules: [cis-ubuntu, aide, auditd, fail2ban]
```

Roles compose freely. A PCI web server in production gets: `roles: [webserver, pci-hardened]` + production's modules from `per_env_overrides.vgo` + fleet-wide modules from `common.vgo`.

### when: expressions -- infrastructure facts axis

The agent discovers infrastructure facts as traits: cloud provider, OS family, region, architecture, container runtime, etc. Use `when:` expressions to conditionally include modules or resources based on traits -- no config axis needed because the machine already knows the answer.

```yaml
# In a module or match block:
modules:
  - name: gcp-logging
    when: "trait('cloud.provider') == 'gcp'"
  - name: cloudwatch
    when: "trait('cloud.provider') == 'aws'"
  - name: bare-metal-monitoring
    when: "!is_virtual"

# In a resource within a module:
resources:
  - name: apt-config
    type: file
    when: "os_family('debian')"
    # ...
  - name: yum-config
    type: file
    when: "os_family('redhat')"
    # ...
```

Common trait-based axes:

| Axis | Trait | Example when: expression |
|------|-------|--------------------------|
| Cloud provider | `cloud.provider` | `trait('cloud.provider') == 'gcp'` |
| OS family | `os.family` | `os_family('debian')` |
| Architecture | `os.arch` | `trait('os.arch') == 'arm64'` |
| Region | `cloud.region` | `trait('cloud.region') == 'us-east-1'` |
| Container | `virtual.system` | `is_container` |
| Virtualization | `virtual.system` | `is_virtual` |

## Putting It All Together

Example: MSP managing Acme Corp with production and staging environments across GCP and AWS.

```
stockpile/
  common.vgo                         # Fleet baseline: sshd, ntp, monitoring
  roles.vgo                          # webserver, database, pci-hardened
  modules/
    nginx.vgo                        # Module: installs nginx
    gcp-logging.vgo                  # Module: GCP Cloud Logging agent
    cloudwatch.vgo                   # Module: AWS CloudWatch agent
    waf.vgo                          # Module: web application firewall
  clients/
    acme/
      common.vgo                     # Acme: adds acme-specific modules/vars
      per_env_overrides.vgo          # prod: +waf, +hardening; staging: +debug-tools
      envoys.vgo                      # Match patterns for Acme's machines
```

Acme's `envoys.vgo`:
```yaml
nodes:
  - match: "web-*.prod.acme.com"
    environment: production
    roles: [webserver, pci-hardened]

  - match: "db-*.prod.acme.com"
    environment: production
    roles: [database, pci-hardened]

  - match: "*.staging.acme.com"
    environment: staging
    roles: [webserver]
```

A machine `web-1.prod.acme.com` running on GCP gets:
- **Directory inheritance**: fleet baseline (sshd, ntp, monitoring) + Acme modules
- **Roles**: nginx, certbot, logrotate (webserver) + cis-ubuntu, aide, auditd, fail2ban (pci-hardened)
- **Environment**: waf, hardening, production vars (from per_env_overrides.vgo)
- **Traits**: gcp-logging (because `cloud.provider == 'gcp'`)

No duplication. Each axis is handled exactly once. Adding a new client means adding a directory. Adding a new environment means adding a block to `per_env_overrides.vgo`. Adding cloud-specific behavior means adding a `when:` expression. None of these changes affect the others.

## Summary

| What you're deciding | Where it goes | Changes when... |
|---------------------|---------------|-----------------|
| Which clients you manage | Directory tree | New client onboarded |
| What environment a machine is in | `environment:` on match block | Machine promoted/demoted |
| What a machine does | `roles:` on match block | Machine repurposed |
| How environments differ | `per_env_overrides.vgo` | Environment policy changes |
| What modules a function needs | `roles.vgo` | Role definition evolves |
| Fleet-wide baseline | `common.vgo` | Security/compliance policy changes |
| Cloud/OS/region behavior | `when:` on traits | Never -- machines report their own facts |

---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
