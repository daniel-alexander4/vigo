
# Vigo Documentation

Vigo is a distributed state enforcement engine for servers and network devices. Agents on managed nodes (envoys) pull desired state from a central server, apply idempotent changes, and report results back. 72 built-in resource types across 8 platforms. No DSL — plain YAML.

Full documentation is available inside the Vigo server web UI. The pages below are the public subset — concepts, getting started, compliance frameworks, executor reference, and examples.

## Getting Started

- [What is Vigo](getting-started/overview) — Architecture, design principles, how it works
- [Technical Comparison](getting-started/technical-comparison) — Detailed comparison with Puppet, Ansible, Chef, Salt, CFEngine
- [Installation](getting-started/installation) — Docker setup, first boot
- [Server Configuration](getting-started/server-config) — Full server.yaml reference
- [Bootstrap](getting-started/bootstrap) — Agent enrollment on all platforms
- [Quickstart](getting-started/quickstart) — 10-minute first-envoy walkthrough
- [First Module](getting-started/first-module) — Write your first .vgo module
- [Core Use Cases](getting-started/use-cases) — Configuration management, security, network, DR/IR, observability
- [Performance: 1-Minute](getting-started/performance-1m) — Baseline projections
- [Performance: 30-Second](getting-started/performance-30s) — Recommended for responsive fleets
- [Performance: 15-Second](getting-started/performance-15s) — Near-real-time state enforcement

## Concepts

- [Architecture](concepts/architecture) — Three binaries, ports, data flows
- [Check-in Lifecycle](concepts/check-in-lifecycle) — Pull loop, fingerprint, delta transfer
- [Convergence Walkthrough](concepts/convergence-walkthrough) — End-to-end with timing and traffic
- [Compiled Promises](concepts/compiled-promises) — Bundle signing, TTL, offline convergence
- [Config Format](concepts/config-format) — Modules, roles, envoys, vars
- [Resource DAG](concepts/resource-dag) — depends_on, notify, subscribes
- [Resource Language](concepts/resource-language) — foreach, case/match, conditional_block, defaults
- [When Expressions](concepts/when-expressions) — Boolean logic and builtin functions
- [Templates](concepts/templates) — Go templates in content: attributes
- [Secrets](concepts/secrets) — Two backends and the secret: prefix
- [Composition Patterns](concepts/composition-patterns) — Five layers of config reuse
- [Multi-Axis Configuration](concepts/multi-axis-config) — Per-environment overrides for multi-site fleets
- [Compliance](concepts/compliance) — Per-resource to fleet-wide status
- [Observe Mode](concepts/observe-mode) — Global and per-entry drift-only mode
- [Guardrails](concepts/guardrails) — Self-protection for agent and server paths
- [Module Retraction](concepts/module-retraction) — Auto-undo when modules are removed
- [Multi-Client Environments](concepts/multi-client-environments) — MSP pattern: clients x environments
- [Network Devices](concepts/network-devices) — Gateway proxy architecture
- [Spanner](concepts/spanner) — Hub-spoke multi-server topology
- [Swarm](concepts/swarm) — Peer-to-peer content distribution
- [Custom Executors](concepts/custom-executors) — JSON stdin/stdout protocol
- [Custom Traits](concepts/custom-traits) — Custom trait collector scripts

## Compliance Frameworks

- [HIPAA Checklist](compliance/hipaa-checklist) — 164.312 requirement mapping
- [HITRUST CSF](compliance/hitrust) — 31 control references across 8 domains
- [SOC 2](compliance/soc2) — 32 Trust Services Criteria across 5 categories
- [PCI DSS v4.0](compliance/pci-dss) — 42 requirements + enforcement modules
- [CIS Benchmarks](compliance/cis-benchmarks) — Ubuntu, RHEL, Windows Level 1 modules
- [NIST 800-53](compliance/nist-800-53) — 41 Federal/FedRAMP controls
- [ISO 27001](compliance/iso-27001) — 33 Annex A controls
- [GDPR](compliance/gdpr) — 13 EU data protection articles
- [NERC CIP](compliance/nerc-cip) — 18 energy sector requirements
- [Cyber Essentials Plus](compliance/cyber-essentials-plus) — UK government certification
- [IEC 62443](compliance/iec-62443) — Industrial cybersecurity
- [Compliance Reporting](compliance/reporting) — JSON, HTML, and OSCAL output
- [Security Scanning](compliance/security-scanning) — CVE scanning, hardening audits, rootkit detection
- [Remediation Workflow](compliance/remediation) — Automated drift detection, fix, verification

## Reference

- [Executors](reference/executors) — 72 built-in resource types across all platforms
- [Supported Platforms](reference/platforms) — Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, Windows, network devices

## Security

- [Self-Protection Guardrails](security/self-protection) — Blocked commands, protected paths, enforcement layers

## Examples

- [Nginx Module](examples/nginx-module) — Full nginx walkthrough
- [Multi-Environment](examples/multi-env) — Production/staging with environment_overrides
- [Docker Stack](examples/docker-stack) — Container management
- [Security Hardening](examples/security-hardening) — Firewall, fail2ban, SSH, sudo
- [Monitoring Stack](examples/monitoring-stack) — Prometheus, node_exporter, Grafana
- [Database Cluster](examples/database-cluster) — PostgreSQL with vars_from
- [Kubernetes Nodes](examples/kubernetes-nodes) — OS-level k8s node config
