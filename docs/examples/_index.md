
# Example Walkthroughs

These examples demonstrate real-world Vigo configurations using `.vgo` YAML modules, roles, and node assignments.

Each example is a complete walkthrough with ready-to-use config snippets. Adapt the variable values and paths for your environment.

## Available Examples

- [Nginx Module](nginx-module.md) -- Package, config file, and service with notify chain
- [Multi-Environment Setup](multi-env.md) -- Production/staging with `environments.vgo`
- [Docker Stack Management](docker-stack.md) -- Container lifecycle with `docker_container` and `docker_image` executors
- [Security Hardening](security-hardening.md) -- Firewall, fail2ban, SSH, and sudo with `depends_on`
- [Monitoring Stack](monitoring-stack.md) -- Prometheus, node_exporter, and Grafana
- [Database Cluster](database-cluster.md) -- PostgreSQL with `secret:` references
- [Kubernetes Nodes](kubernetes-nodes.md) -- OS-level k8s node config with rolling drain-and-reboot workflow
- [Brown Noise Generator](brownnoise.md) -- Docker container with PulseAudio passthrough using `exec` resources
- [Disk Hygiene](disk-hygiene.md) -- Prevent disk exhaustion with journal caps, tmp aging, package cache cleanup, and Docker pruning
- [Multi-Cloud Configuration](multi-cloud.md) -- Cloud-aware modules using IMDS traits across AWS, GCP, and Azure

## Migration Guides

- [Migration Overview](migration/_index.md) -- Comparison table and guide index
- [From Puppet](migration/puppet.md) -- Pull-based, catalog, resources, Facter
- [From Ansible](migration/ansible.md) -- Push-to-pull shift, handlers to notify
- [From Chef](migration/chef.md) -- Ruby DSL to YAML, cookbooks to modules
- [From Salt](migration/salt.md) -- Both YAML, Pillar to vars, Grains to Traits
- [From CFEngine](migration/cfengine.md) -- Promise theory, classes to when:

## Conventions

All examples follow these patterns:

- Modules live in `stockpile/modules/`
- Roles live in `stockpile/roles/`
- Node assignments live in `stockpile/envoys/envoys.vgo`
- Go templates (`{{ }}`) are only used in `content:` attributes
- OS-conditional packages use separate resources with `when:` expressions
- Secrets use the `secret:` prefix and are resolved by the secrets provider
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
