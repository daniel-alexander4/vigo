# Example Walkthroughs

These examples demonstrate real-world Hobnob configurations using `.hnb` YAML modules, roles, and node assignments.

Each example is a complete walkthrough with ready-to-use config snippets. Adapt the variable values and paths for your environment.

## Available Examples

- [Nginx Module](nginx-module.md) -- Package, config file, and service with notify chain
- [Multi-Environment Setup](multi-env.md) -- Production/staging with `env_vars`
- [Docker Stack Management](docker-stack.md) -- Container lifecycle with `docker_container` and `docker_image` executors
- [Security Hardening](security-hardening.md) -- Firewall, fail2ban, SSH, and sudo with `depends_on`
- [Monitoring Stack](monitoring-stack.md) -- Prometheus, node_exporter, and Grafana
- [Database Cluster](database-cluster.md) -- PostgreSQL with `secret:` references

## Conventions

All examples follow these patterns:

- Modules live in `thrall-configs/modules/`
- Roles live in `thrall-configs/roles/`
- Node assignments live in `thrall-configs/nodes.hnb`
- Go templates (`{{ }}`) are only used in `content:` attributes
- OS-conditional packages use separate resources with `when:` expressions
- Secrets use the `secret:` prefix and are resolved by the secrets provider
