
# How-to guides

Each page walks one operator task end-to-end. Pick the closest match to what you're trying to do.

## Where to start

Your reading order depends on how much of the fleet is already standing up:

| When | What you'll do | Pages |
|---|---|---|
| **Day 1** — first time | Stand up the server, enroll a single envoy, write and apply your first module. | [Install](install.md) → [Enroll an envoy](enroll-an-envoy.md) → [Write your first module](write-your-first-module.md) → [Publish your config](publish-config.md) |
| **Day 7** — basic ops | Auth, monitoring, remote access. The fleet now has more than one operator and more than a handful of envoys. | [Set up auth](set-up-auth.md) → [Set up Scrier](set-up-scrier.md) → [Set up monitoring](set-up-monitoring.md) |
| **Day 30** — production | Compliance reporting, backups, host hardening, the swarm content subsystems. The fleet is doing real work for real stakeholders. | [Compliance reporting](compliance-reporting.md) → [Back up and recover](backup-and-recovery.md) → [Harden envoy secret memory](harden-envoy-secret-memory.md) → [Set up Swarm](set-up-swarm.md) → [Set up Puddle](set-up-puddle.md) → [Set up Lockbox](set-up-lockbox.md) → [Set up Gitback](set-up-gitback.md) |
| **Day 90** — scale and DR | Sizing the server host, hub-spoke sharding, disaster recovery, artifact distribution, network devices, custom subsystems. | [Size a vigosrv host](sizing.md) → [Set up Spanner](set-up-spanner.md) → [Disaster recovery](disaster-recovery.md) → [Set up Curator](set-up-curator.md) → [Set up Longdrawer](set-up-longdrawer.md) → [Set up network devices](set-up-network-devices.md) |

The migration guides at the bottom are for coming from another config-management system — read them once when you start, not in the daily flow.

## Migrating from another CM

| Guide | |
|---|---|
| [Migrate from Ansible](migrate-from-ansible.md) | |
| [Migrate from Puppet](migrate-from-puppet.md) | |
| [Migrate from Chef](migrate-from-chef.md) | |
| [Migrate from Salt](migrate-from-salt.md) | |
| [Migrate from CFEngine](migrate-from-cfengine.md) | |

## When something goes wrong

[Troubleshoot common issues](troubleshoot.md) is symptom-keyed — search the page for the exact error string an envoy or session is producing and jump to its fix.

---
*Verified on Vigo 0.51.6 · 2026-05-13.*

*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
