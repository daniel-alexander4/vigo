
# Migration Guides

Vigo borrows ideas from every major configuration management system. If you're coming from Puppet, Ansible, Chef, Salt, or CFEngine, these guides map the concepts you already know to their Vigo state enforcement equivalents.

## Quick Comparison

| Aspect | Puppet | Ansible | Chef | Salt | CFEngine | **Vigo** |
|---|---|---|---|---|---|---|
| Config language | Puppet DSL | YAML + Jinja2 | Ruby DSL | YAML + Jinja2 | CFEngine DSL | YAML (`.vgo`) |
| Data lookup | Hiera | host_vars / group_vars | Attributes (15 levels) | Pillar | augments | `vars` / `common.vgo` / `environments.vgo` |
| System facts | Facter | Facts | Ohai | Grains | hard classes | Traits |
| Reusable unit | Class / Module | Role | Cookbook / Recipe | State file / Formula | Bundle | Module |
| Node classification | site.pp / ENC | Inventory | Run list / Policy | top.sls | promises.cf | `envoys.vgo` (first-match-wins) |
| Agent model | Pull (30 min) | Push (SSH) | Pull (30 min) | Push (ZMQ) / Pull | Pull (5 min) | Pull (configurable) |
| Secrets | hiera-eyaml | Vault | Chef Vault / Data Bags | GPG pillar | cf-secret | `secret:` prefix |
| Templates | ERB | Jinja2 | ERB | Jinja2 | Mustache | Go templates (`content:` only) |
| Ordering | auto + require/before | task order | compile-time + notifies | requisites | normal ordering | `depends_on` / `before` DAG |
| Notification | notify / subscribe | handlers | notifies / subscribes | watch / listen | classes | `notify` / `subscribes` |

## Guides

- [Migrating from Puppet](puppet.md) — Closest conceptual match (pull-based, catalog, resources, Facter)
- [Migrating from Ansible](ansible.md) — Push-to-pull shift, Jinja2 to Go templates, handlers to notify
- [Migrating from Chef](chef.md) — Ruby DSL to YAML, 15 attribute levels to 3, cookbooks to modules
- [Migrating from Salt](salt.md) — Both YAML, Pillar to vars, Grains to Traits
- [Migrating from CFEngine](cfengine.md) — Promise theory heritage, classes to when:, bundles to modules
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
