# Example Walkthroughs

These examples demonstrate real-world Vigo configurations using `.vgo` YAML modules, roles, and node assignments.

Each example is a complete walkthrough with ready-to-use config snippets. Adapt the variable values and paths for your environment.

## Available Examples

- [Nginx Module](nginx-module.md) -- Package, config file, and service with notify chain
- [Multi-Environment Setup](multi-env.md) -- Production/staging with `environment_overrides`
- [Docker Stack Management](docker-stack.md) -- Container lifecycle with `docker_container` and `docker_image` executors
- [Security Hardening](security-hardening.md) -- Firewall, fail2ban, SSH, and sudo with `depends_on`
- [Monitoring Stack](monitoring-stack.md) -- Prometheus, node_exporter, and Grafana
- [Database Cluster](database-cluster.md) -- PostgreSQL with `secret:` references
- [Kubernetes Nodes](kubernetes-nodes.md) -- OS-level k8s node config with rolling drain-and-reboot workflow
- [Brown Noise Generator](brownnoise.md) -- Docker container with PulseAudio passthrough using `exec` resources

## Conventions

All examples follow these patterns:

- Modules live in `policy/stage/modules/`
- Roles live in `policy/stage/roles/`
- Node assignments live in `policy/stage/envoys/nodes.vgo`
- Go templates (`{{ }}`) are only used in `content:` attributes
- OS-conditional packages use separate resources with `when:` expressions
- Secrets use the `secret:` prefix and are resolved by the secrets provider
