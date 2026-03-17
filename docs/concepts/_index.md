# Concepts

Core concepts that explain how Hobnob works.

- [Architecture](architecture.md) — Three binaries, ports, data flows
- [Config Format](config-format.md) — Modules, roles, nodes.hnb, vars, environment_overrides
- [Resource DAG](resource-dag.md) — depends_on, notify, subscribes
- [Resource Language](resource-language.md) — defaults, foreach, case/match, conditional_block
- [When Expressions](when-expressions.md) — Boolean logic and 15 builtin functions
- [Templates](templates.md) — Go templates in content: attributes
- [Convergence Walkthrough](convergence-walkthrough.md) — End-to-end example with timing and traffic flow
- [Check-in Lifecycle](check-in-lifecycle.md) — Pull loop, fingerprint, delta transfer
- [Compiled Promises](compiled-promises.md) — Bundle signing, TTL, offline convergence
- [Compliance](compliance.md) — Per-resource to fleet-wide compliance
- [Secrets](secrets.md) — Two backends and the secret: prefix
- [Spanner](spanner.md) — Hub-spoke scaling
- [License](license.md) — Free tier and enforcement stages
- [Guardrails](guardrails.md) — Self-protection for agent and server paths
- [Custom Executors](custom-executors.md) — JSON stdin/stdout protocol
- [Custom Traits](custom-traits.md) — Custom trait collector scripts
- [Use Cases](use-cases.md) — Beyond configuration management: security, IoT, DR, observability
