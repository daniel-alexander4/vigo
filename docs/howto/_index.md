
# How-to guides

Each page walks one operator task end-to-end. Pick the closest match to what you're trying to do.

## Where to start

Your reading order depends on how much of the fleet is already standing up:

| When | What you'll do | Pages |
|---|---|---|
| **Day 1** — first time | Stand up the server, enroll a single envoy, write and apply your first configcrate. | [Install](operate/install.md) → [Enroll an envoy](operate/enroll-an-envoy.md) → [Write your first configcrate](configcrates/write-your-first-configcrate.md) → [Publish your config](configcrates/publish-config.md) |
| **Day 7** — basic ops | Auth, monitoring, remote access. The fleet now has more than one operator and more than a handful of envoys. | [Set up auth](operate/set-up-auth.md) → [Set up Scrier](operate/set-up-scrier.md) → [Set up monitoring](operate/set-up-monitoring.md) |
| **Day 30** — production | Compliance reporting, backups, host hardening, the swarm content subsystems. The fleet is doing real work for real stakeholders. | [Compliance reporting](compliance/compliance-reporting.md) → [Back up and recover](operate/backup-and-recovery.md) → [Harden envoy secret memory](operate/harden-envoy-secret-memory.md) → [Set up Swarm](swarm/set-up-swarm.md) → [Set up Puddle](swarm/set-up-puddle.md) → [Set up Lockbox](swarm/set-up-lockbox.md) → [Set up Gitback](swarm/set-up-gitback.md) |
| **Day 90** — scale and DR | Sizing the server host, spanner federation, disaster recovery, artifact distribution, network devices, custom subsystems. | [Size a vigosrv host](operate/sizing.md) → [Set up Spanner](operate/set-up-spanner.md) → [Disaster recovery](operate/disaster-recovery.md) → [Set up Curator](swarm/set-up-curator.md) → [Set up Longdrawer](swarm/set-up-longdrawer.md) → [Set up network devices](operate/set-up-network-devices.md) |

The migration guides at the bottom are for coming from another config-management system — read them once when you start, not in the daily flow.

## Migrating from another CM

| Guide | |
|---|---|
| [Migrate from Ansible](migrate/migrate-from-ansible.md) | |
| [Migrate from Puppet](migrate/migrate-from-puppet.md) | |
| [Migrate from Chef](migrate/migrate-from-chef.md) | |
| [Migrate from Salt](migrate/migrate-from-salt.md) | |
| [Migrate from CFEngine](migrate/migrate-from-cfengine.md) | |
| [Migrate from a licensed Vigo release](migrate/migrate-from-licensed-vigo.md) | Moving a pre-0.76.95 licensed server to the open-source release |

## When something goes wrong

[Troubleshoot common issues](operate/troubleshoot.md) is symptom-keyed — search the page for the exact error string an envoy or session is producing and jump to its fix.

---
*Verified on Vigo 0.51.6 · 2026-05-13.*
