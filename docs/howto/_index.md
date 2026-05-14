
# How-to guides

Each page walks one operator task end-to-end. Pick the closest match to what you're trying to do.

## Day-zero

| Guide | What you'll do |
|---|---|
| [Install](install.md) | Stand up the Vigo server. |
| [Enroll an envoy](enroll-an-envoy.md) | Get a managed node talking to the server. |
| [Write your first module](write-your-first-module.md) | Declare desired state for a real-world workload (packages, files, services, dependencies). |
| [Publish config](publish-config.md) | Validate and ship your config tree, with rollback. |

## Operating the fleet

| Guide | What you'll do |
|---|---|
| [Set up Scrier](set-up-scrier.md) | Browser and CLI SSH/RDP/VNC to any envoy without inbound ports. |
| [Set up Swarm](set-up-swarm.md) | Peer-to-peer fleet content distribution (filecast, gitback, lockbox, longdrawer, curator). |
| [Set up auth](set-up-auth.md) | Web UI / CLI authentication: basic, OIDC, isowebauth. |
| [Set up monitoring](set-up-monitoring.md) | Prometheus scrape + the five Grafana dashboards. |
| [Backup and recovery](backup-and-recovery.md) | Litestream WAL replication to S3-compatible storage. |
| [Disaster recovery](disaster-recovery.md) | Restore a server from backup; move a server without re-enrolling. |
| [Compliance reporting](compliance-reporting.md) | Use the framework matrix; produce per-framework coverage reports for auditors. |
| [Troubleshoot](troubleshoot.md) | Common failure modes and how to fix them. |

## Coming from another config-management system

| Guide | |
|---|---|
| [Migrate from Ansible](migrate-from-ansible.md) | |
| [Migrate from Puppet](migrate-from-puppet.md) | |
| [Migrate from Chef](migrate-from-chef.md) | |
| [Migrate from Salt](migrate-from-salt.md) | |
| [Migrate from CFEngine](migrate-from-cfengine.md) | |

---
*Confidential — Alexander4, LLC. Not for redistribution. See [../legal/license.md](../legal/license.md).*
