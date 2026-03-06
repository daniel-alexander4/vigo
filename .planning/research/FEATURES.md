# Feature Research

**Domain:** Configuration Management Systems (Puppet, Chef, Ansible, Salt class)
**Researched:** 2026-03-05
**Confidence:** HIGH

## Feature Landscape

This analysis is grounded in what Puppet, Chef, Ansible, and SaltStack provide, mapped against isogov's project scope (pull-based, Go, YAML configs, isopass secrets, Linux/macOS only). Features are categorized by what sysadmins and DevOps teams expect from a configuration management tool in 2026.

### Table Stakes (Users Expect These)

Features users assume exist. Missing any of these and the tool is not taken seriously.

#### Resource Management (Core Primitives)

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| File resource (content, permissions, ownership, symlinks) | Every CM tool manages files -- it is the most fundamental operation. Puppet, Chef, Ansible, Salt all have file resources as tier-1. | MEDIUM | isogov plans this with SHA256 content checking. Must handle directories, symlinks, templates, and recursive ownership. Puppet's `file` type handles all of these. |
| Package resource (install, remove, version pin) | Package management is the second most common CM operation. All four major tools support multi-platform package managers. | MEDIUM | isogov plans apt/dnf/yum/apk/pacman/brew/zypper. Version pinning with drift detection is critical -- Puppet and Chef both enforce version compliance. |
| Service resource (start, stop, enable, restart) | Services are the third leg of the classic "package/file/service" triad that Puppet documentation calls "one of the most useful idioms." | LOW | isogov plans systemctl integration. Must handle enable/disable (boot persistence) and running/stopped state independently. |
| User resource (create, modify, remove) | User management is a core resource type in every CM tool. Puppet has `user` and `group` as built-in types. | MEDIUM | isogov plans /etc/passwd-based management. Consider whether group management is needed separately (Puppet treats `group` as a distinct resource type). |
| Exec/command resource (run arbitrary commands) | Puppet's `exec`, Chef's `execute`, Ansible's `command`/`shell`, Salt's `cmd.run` all provide escape-hatch command execution. Essential for tasks that don't fit declarative models. | LOW | Not currently in isogov's spec. This is a gap. Every CM tool has this. Must be idempotent via `onlyif`/`unless`/`creates` guards (Puppet's approach). |

#### Convergence Engine

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Idempotent execution (check-before-act) | The defining characteristic of CM tools. Puppet, Chef, Ansible, Salt all guarantee this. Without idempotency, the tool is just a script runner. | HIGH | isogov's core constraint. Every module executor must verify current state before acting. SHA256 for files, version checks for packages, status checks for services. |
| Resource dependency ordering (DAG) | Puppet's dependency graph, Chef's compile-then-converge model, Salt's requisite system all enforce ordering. Without explicit ordering, convergence is unpredictable. | HIGH | isogov plans topological sort with circular dependency detection. This is correct -- Puppet's approach. |
| Notify/subscribe triggers | Puppet's notify/subscribe, Chef's notifies/subscribes, Salt's watch requisite. The classic pattern: "restart nginx when nginx.conf changes." | MEDIUM | isogov plans this. Critical for the package/file/service triad to work correctly. |
| Desired-state declaration (declarative syntax) | All CM tools use declarative configuration (what, not how). Users declare the end state, tool figures out how to get there. | MEDIUM | isogov uses YAML. This is table stakes -- Puppet uses its DSL, Chef uses Ruby DSL, Ansible/Salt use YAML. YAML is the modern standard. |
| Convergence reporting (what changed, what failed) | Every CM tool reports back what actions it took. Puppet has reports, Chef has run lists with status, Ansible has recap output. | MEDIUM | isogov plans run results reporting via gRPC. Must include: resource name, action taken (created/modified/skipped/failed), before/after values. |

#### Agent and Communication

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Pull-based agent with configurable interval | Puppet agent runs every 30 minutes by default. Chef client runs every 30 minutes. Salt minion maintains persistent connection. Pull is the standard model for CM. | MEDIUM | isogov plans jittered pull loop with exponential backoff. Standard pattern. |
| Secure transport (TLS/mTLS) | Puppet uses SSL with its own CA. Chef uses HTTPS. Salt uses AES encryption over ZeroMQ. Secure transport is non-negotiable. | HIGH | isogov plans gRPC over mTLS with ED25519 keys. Stronger than most competitors. |
| Node enrollment/registration | Puppet has `puppet cert sign`, Chef has knife bootstrap, Salt has key acceptance. New nodes must be approved before receiving config. | MEDIUM | isogov plans hostname/IP pattern matching + one-time tokens. Solid approach. |
| Fact/inventory collection | Puppet has Facter, Chef has Ohai, Salt has Grains, Ansible gathers facts via setup module. Knowing what the node IS enables conditional configuration. | MEDIUM | isogov plans pluggable Collector interface for OS, hardware, network, packages. Must include: OS family, OS version, architecture, hostname, IP addresses, memory, disk. |

#### Configuration Language

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| Template rendering (variable substitution in files) | Puppet uses ERB, Chef uses ERB, Ansible uses Jinja2, Salt uses Jinja2. Templated config files are a core workflow. | MEDIUM | isogov plans Go templates. Good choice for a Go project -- no external dependency. Must support accessing facts and node variables within templates. |
| Conditional resources (apply only when condition met) | Puppet has `if`/`unless`/`case`, Chef has `only_if`/`not_if`, Ansible has `when`, Salt has Jinja conditionals. Applying resources conditionally based on node facts is fundamental. | MEDIUM | isogov plans `when:` with Go template expressions evaluated against node facts. Correct approach. |
| Variable/parameter hierarchy (defaults + overrides) | Puppet has Hiera, Chef has data bags + attributes, Ansible has variable precedence, Salt has Pillar. Layered variable resolution enables reuse. | HIGH | isogov plans environment tiers with per-environment variable overrides merged at render time. This is the right pattern but simpler than Hiera's 5+ layers. |

#### Operational Visibility

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| CLI for administration | Puppet has `puppet` CLI, Chef has `knife`, Salt has `salt` CLI, Ansible has `ansible`/`ansible-playbook`. A CLI is the primary admin interface. | MEDIUM | isogov plans `gvctl` with subcommands for clients, push, runs, tokens, webhooks, packages, env, preview, rollback. Comprehensive. |
| Compliance/drift status per node | Puppet Enterprise dashboard shows compliant/non-compliant nodes. Chef Automate has compliance views. Knowing which nodes are drifted is critical for ops. | MEDIUM | isogov plans compliant/drifted/failing/stale/unknown status with history. Good design -- covers the key states. |
| Dry-run/preview mode | Puppet has `--noop`, Chef has `--why-run`, Ansible has `--check`, Salt has `test=True`. Preview before apply is a safety requirement. | LOW | isogov plans dry-run via agent + gvctl + DryRun RPC. Correct approach. |
| Run history and audit trail | All enterprise CM tools track what ran, when, on which nodes, with what results. Required for change management and compliance. | MEDIUM | isogov plans this via PostgreSQL backend. Must be queryable -- "what changed on node X in the last 24 hours?" |

### Differentiators (Competitive Advantage)

Features where isogov can distinguish itself from the established players. These are not required for a CM tool to function, but they add significant value.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Single Go binary (agent) | Puppet requires Ruby runtime + gems. Chef requires Ruby + omnibus. Salt requires Python + dependencies. A single static Go binary is dramatically easier to deploy, has no dependency conflicts, and starts in milliseconds. This is isogov's biggest DX advantage. | LOW | Already planned. This is a genuine differentiator -- no other major CM tool ships as a single binary. |
| YAML-only config (filesystem as source of truth) | Puppet has its own DSL. Chef uses Ruby. Salt uses SLS/YAML+Jinja. Pure YAML is the simplest possible config format. No DSL to learn, no programming language required. GitOps-native by default. | LOW | Already planned. Aligns with the design philosophy of "sysadmins can read YAML and immediately understand." No equivalent in Puppet/Chef. |
| Integrated secrets management (isopass) | Puppet requires hiera-eyaml or external Vault integration. Chef requires encrypted data bags or Vault. Ansible requires ansible-vault. Salt requires GPG or Vault pillar. isogov's built-in isopass integration with `secret:` prefix is zero-config secrets. | MEDIUM | Already planned. The `secret:` prefix resolution and automatic rotation watching is cleaner than any competitor's approach. |
| Secret rotation awareness (auto force-run) | No major CM tool automatically detects when secrets rotate and triggers convergence. This is novel. Puppet/Chef/Ansible all require external tooling or manual intervention for secret rotation. | MEDIUM | Already planned. Server polls isopass for rotation, triggers force-run on affected nodes. Genuine innovation. |
| Agent self-update from server | Puppet has `puppet-agent` module for upgrades but it is complex. Chef has `chef-client` cookbook. Having the agent update itself from server-hosted binaries with checksum verification is much simpler for a single-binary agent. | MEDIUM | Already planned. Much simpler than Puppet/Chef's approach because there is only one file to replace. |
| Bootstrap via curl one-liner | Ansible doesn't need this (agentless). Puppet has `curl | bash` but it pulls from Puppet's repos, not your server. Chef has `curl -L https://omnitruck.chef.io/install.sh`. Having your own server host the bootstrap script means no external dependency. | LOW | Already planned. Standard modern pattern. |
| Webhook dispatcher with HMAC signing | Puppet has webhooks in PE. Chef has webhook notifications in Automate. But built-in HMAC-signed webhooks for run/drift/enrollment/rotation events in the open-source core is unusual. Most competitors gate this behind enterprise editions. | MEDIUM | Already planned. Enables integration with Slack, PagerDuty, CI/CD without requiring the enterprise tier. |
| Prometheus metrics built-in | Puppet requires exporters. Chef requires custom reporting handlers. Salt has limited metrics. Built-in Prometheus metrics endpoint is modern and expected by teams running Kubernetes/Grafana stacks. | LOW | Already planned. Expose client count, check-in rates, drift percentages, run durations, compliance ratios. |
| Auto node classification from pattern rules | Puppet has node classification via ENC (External Node Classifier) or console groups. Chef has roles and environments. Salt has targeting by grains/pillars. isogov's `classification.yaml` with pattern rules is a simpler, file-based approach. | LOW | Already planned. Simpler than Puppet's ENC but less flexible. Good tradeoff for the target audience. |
| File snapshots with rollback | Puppet has filebucket for backup/restore. Chef has backup attributes on file resources. But explicit rollback via CLI (`gvctl rollback`) with snapshot history is a better UX. | MEDIUM | Already planned. Store pre-change file contents, allow reverting specific files or entire runs. |
| Server TLS hot reload | Most CM servers require restart for cert rotation. Hot reload via `GetCertificate` callback means zero-downtime cert rotation. Minor but appreciated. | LOW | Already planned. Small but meaningful operational improvement. |

### Anti-Features (Commonly Requested, Often Problematic)

Features that seem good but create problems. isogov's PROJECT.md already identifies several of these correctly.

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| Push-based execution | "I want to apply changes NOW across all nodes." Ansible's popularity proves push is appealing. | Requires server to reach all nodes (firewall/NAT issues). Creates thundering herd on large fleets. Makes server a single point of failure during runs. Pull model is more resilient. | isogov's approach: pull-based with `gvctl push` setting a DB flag that agents pick up on next check-in. Achieves urgency without push architecture. |
| GUI-based config editing | "Let me edit configs in the web UI." Puppet Enterprise console allows some config via UI. Chef Manage had this. | Creates config drift between UI and files. Breaks GitOps workflows. Makes audit trail ambiguous (who changed what, via which interface?). Two sources of truth is always worse than one. | isogov's approach: YAML files on disk are sole source of truth. Web UI is read-only for visibility. Edit files in your editor, commit to git, server watches for changes. |
| Windows support | "We need to manage Windows too." Puppet, Chef, Ansible, Salt all support Windows to varying degrees. | Windows CM is a completely different world -- WinRM vs SSH, PowerShell vs bash, MSI vs apt, Windows services vs systemd, registry vs config files. Supporting it doubles complexity for a fraction of the user base. | isogov correctly scopes to Linux and macOS only. If Windows is needed, use a purpose-built tool (DSC, Ansible for Windows). |
| Domain-specific language (DSL) | "Puppet's DSL is powerful, we should have one." Chef's Ruby DSL allows arbitrary code in recipes. | DSLs create learning curves, limit tooling (no generic YAML linters), and make the tool feel like a programming language rather than a config format. Puppet's DSL is regularly cited as its steepest learning curve. | isogov uses YAML with Go template expressions for conditionals. Zero DSL. Sysadmins already know YAML. |
| Agent-to-agent communication | "Nodes should coordinate with each other." Salt has peer communication via mine. Puppet has exported resources. | Dramatically increases security surface. Creates distributed systems complexity. Makes debugging convergence nearly impossible. If node A depends on node B's state, failures cascade unpredictably. | isogov correctly prohibits agent-to-agent communication. Agents only talk to the server. Cross-node coordination happens at the config level (server knows all node states). |
| Multi-language agent implementations | "Write agents in Python/Rust/Node for different platforms." | Maintaining multiple codebases for the same agent is an enormous maintenance burden. Feature parity across implementations is nearly impossible. Bug fixes must be applied N times. | isogov correctly ships a single Go binary for all platforms. Go cross-compiles trivially. |
| Real-time streaming / persistent connections | "I want to see live output from all agents." Salt uses ZeroMQ persistent connections. | Persistent connections consume server resources per-node, create state management complexity, and don't scale well beyond a few thousand nodes. Pull with jitter is simpler and scales better. | isogov uses pull with jittered intervals. Webhooks provide near-real-time notifications for events that matter. |
| Arbitrary code execution in configs | "Let me write Go/Python/shell in my config files." Chef allows arbitrary Ruby in recipes. | Turns configuration into programming. Makes configs harder to review, audit, and reason about. Security risk -- arbitrary code execution on every convergence run. | isogov uses declarative YAML with constrained Go template expressions for conditionals. No arbitrary code execution in configs. Exec resource (if added) handles one-off commands with explicit guards. |
| Cloud provisioning / infrastructure as code | "CM tool should also provision VMs and cloud resources." Ansible can provision AWS/GCP/Azure resources. Salt Cloud does this. | Configuration management and infrastructure provisioning are different concerns. Terraform/Pulumi/OpenTofu handle provisioning. Mixing them creates scope creep and a tool that does both poorly. | isogov manages what is ON nodes, not the nodes themselves. Use Terraform to provision, isogov to configure. Clear separation of concerns. |

## Feature Dependencies

```
[Secure Transport (mTLS/gRPC)]
    |
    +--requires--> [Node Enrollment/Registration]
    |                  |
    |                  +--requires--> [One-time Tokens]
    |
    +--enables--> [Agent Pull Loop]
                      |
                      +--requires--> [Fact Collection]
                      |                  |
                      |                  +--enables--> [Conditional Resources (when:)]
                      |                  +--enables--> [Auto Node Classification]
                      |
                      +--requires--> [Config Parsing/Rendering]
                      |                  |
                      |                  +--requires--> [Template Engine (Go templates)]
                      |                  +--requires--> [Variable/Environment Hierarchy]
                      |                  +--requires--> [Secret Resolution (isopass)]
                      |
                      +--enables--> [Resource Execution]
                                        |
                                        +--requires--> [DAG Ordering]
                                        +--requires--> [Idempotent Module Executors]
                                        |                  |
                                        |                  +--includes--> [File Resource]
                                        |                  +--includes--> [Package Resource]
                                        |                  +--includes--> [Service Resource]
                                        |                  +--includes--> [User Resource]
                                        |
                                        +--enables--> [Notify/Subscribe Triggers]
                                        +--enables--> [Convergence Reporting]
                                        |                  |
                                        |                  +--enables--> [Compliance Status]
                                        |                  +--enables--> [Run History]
                                        |                  +--enables--> [Webhook Dispatch]
                                        |
                                        +--enables--> [Dry-Run Mode]
                                        +--enables--> [File Snapshots]
                                                          |
                                                          +--enables--> [Rollback]

[CLI (gvctl)] --consumes--> [REST API]
[Web UI]      --consumes--> [REST API]

[Prometheus Metrics] --independent-- (can be added at any phase)
[Agent Self-Update]  --independent-- (only needs basic transport)
[Bootstrap Script]   --independent-- (only needs server HTTP endpoint)
```

### Dependency Notes

- **Fact Collection requires Agent Pull Loop:** Facts are gathered at check-in time, before config is rendered. The agent must be able to connect to the server first.
- **Conditional Resources require Fact Collection:** `when:` expressions evaluate against node facts. Without facts, conditions cannot be resolved.
- **Notify/Subscribe requires DAG + Module Executors:** Triggers fire based on resource state changes, which requires resources to have executed and reported their state.
- **Compliance Status requires Convergence Reporting:** Drift/compliance status is derived from run results. No reporting = no compliance tracking.
- **Rollback requires File Snapshots:** Cannot roll back without snapshots of pre-change state.
- **Secret Resolution requires isopass integration:** Configs with `secret:` prefixes cannot render without the secrets provider.
- **Web UI and CLI both consume REST API:** REST API must exist before either UI surface can function.

## MVP Definition

### Launch With (v1)

The minimum set of features needed to manage a small fleet of Linux nodes.

- [ ] gRPC transport with mTLS -- the secure communication foundation
- [ ] Node enrollment with one-time tokens -- nodes must be able to register
- [ ] Agent pull loop with jitter -- nodes must be able to check in
- [ ] Fact collection (OS, hostname, IP, architecture) -- minimum facts for conditional config
- [ ] YAML config parsing with Go template rendering -- the configuration language
- [ ] Environment variable hierarchy (base + per-environment overrides) -- enables multi-environment use
- [ ] File resource (content, permissions, ownership, SHA256 check) -- most used resource type
- [ ] Package resource (install/remove/version pin, apt + dnf minimum) -- second most used
- [ ] Service resource (start/stop/enable/disable via systemctl) -- third most used
- [ ] Resource DAG ordering with circular dependency detection -- correct execution order
- [ ] Notify/subscribe triggers -- the package/file/service triad requires this
- [ ] Convergence reporting (what changed, what failed) -- operators must know what happened
- [ ] isopass secret resolution (`secret:` prefix) -- secrets must resolve or configs are incomplete
- [ ] gvctl CLI (basic: list clients, view runs, trigger push) -- operators need a control plane
- [ ] Dry-run mode -- safety net before applying to production

### Add After Validation (v1.x)

Features to add once core convergence is working reliably on a small fleet.

- [ ] User resource -- add once file/package/service are proven stable
- [ ] Exec resource (with onlyif/unless/creates guards) -- escape hatch for non-declarative tasks
- [ ] Auto node classification from pattern rules -- reduces manual per-node config
- [ ] Conditional resources (`when:` expressions) -- enables smarter, fact-driven configs
- [ ] Compliance status tracking (compliant/drifted/failing/stale/unknown) -- operational visibility
- [ ] File snapshots and rollback -- safety net for bad changes
- [ ] Webhook dispatcher with HMAC signing -- integration with external systems
- [ ] Prometheus metrics endpoint -- observability for monitoring stacks
- [ ] Secret rotation watcher with force-run -- operational excellence for secret lifecycle
- [ ] REST API for web UI -- foundation for the dashboard
- [ ] Web UI (dashboard, clients, runs) -- visual operational visibility
- [ ] Multi-platform package managers beyond apt/dnf (apk, pacman, brew, zypper) -- broader OS support
- [ ] Agent self-update with checksum verification -- operational convenience
- [ ] Bootstrap curl one-liner -- streamlined onboarding

### Future Consideration (v2+)

Features to defer until product-market fit is established.

- [ ] Cron/scheduled task resource -- useful but not critical for v1
- [ ] Group resource (separate from user) -- Puppet treats this as distinct; may be needed for precise group management
- [ ] Mount/filesystem resource -- specialized, not universally needed
- [ ] Host resource (/etc/hosts management) -- niche
- [ ] SSH authorized keys resource -- useful but can be handled via file resource initially
- [ ] Advanced template functions (custom Go template funcs) -- power user feature
- [ ] Multi-server high availability -- needed at scale, not for initial adoption
- [ ] RBAC for API/CLI/UI access -- needed for teams, not solo operators
- [ ] Plugin/module system for custom resource types -- extensibility for power users
- [ ] Orchestration across nodes (rolling updates, canary) -- crosses into orchestration territory
- [ ] AI-assisted config generation (a la Puppet Code Assist) -- trendy but not core
- [ ] Event-driven automation (react to external events) -- Ansible's EDA is in this space

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| File resource | HIGH | MEDIUM | P1 |
| Package resource | HIGH | MEDIUM | P1 |
| Service resource | HIGH | LOW | P1 |
| mTLS gRPC transport | HIGH | HIGH | P1 |
| Agent pull loop | HIGH | MEDIUM | P1 |
| DAG ordering | HIGH | HIGH | P1 |
| Notify/subscribe | HIGH | MEDIUM | P1 |
| YAML config + Go templates | HIGH | MEDIUM | P1 |
| isopass secret resolution | HIGH | MEDIUM | P1 |
| Fact collection | HIGH | MEDIUM | P1 |
| Node enrollment | HIGH | MEDIUM | P1 |
| Dry-run mode | HIGH | LOW | P1 |
| Convergence reporting | HIGH | MEDIUM | P1 |
| gvctl CLI (basic) | HIGH | MEDIUM | P1 |
| Environment variable hierarchy | MEDIUM | MEDIUM | P1 |
| User resource | MEDIUM | MEDIUM | P2 |
| Exec resource | MEDIUM | LOW | P2 |
| Conditional resources (when:) | MEDIUM | MEDIUM | P2 |
| Auto node classification | MEDIUM | LOW | P2 |
| Compliance status tracking | MEDIUM | MEDIUM | P2 |
| File snapshots + rollback | MEDIUM | MEDIUM | P2 |
| Webhook dispatcher | MEDIUM | MEDIUM | P2 |
| Prometheus metrics | MEDIUM | LOW | P2 |
| Secret rotation watcher | MEDIUM | MEDIUM | P2 |
| REST API | MEDIUM | MEDIUM | P2 |
| Web UI | MEDIUM | HIGH | P2 |
| Agent self-update | MEDIUM | MEDIUM | P2 |
| Bootstrap script | LOW | LOW | P2 |
| Multi-platform packages | LOW | MEDIUM | P2 |
| Cron resource | LOW | LOW | P3 |
| Group resource | LOW | LOW | P3 |
| Mount resource | LOW | LOW | P3 |
| Host resource | LOW | LOW | P3 |
| SSH keys resource | LOW | LOW | P3 |
| HA / multi-server | LOW | HIGH | P3 |
| RBAC | LOW | HIGH | P3 |
| Plugin/module system | LOW | HIGH | P3 |
| Orchestration | LOW | HIGH | P3 |

**Priority key:**
- P1: Must have for launch -- the tool is not usable without these
- P2: Should have, add after core works -- these make the tool competitive
- P3: Nice to have, future consideration -- these make the tool enterprise-ready

## Competitor Feature Analysis

| Feature | Puppet | Chef | Ansible | Salt | isogov Approach |
|---------|--------|------|---------|------|-----------------|
| Architecture | Pull (agent) | Pull (agent) | Push (SSH) | Both (agent or SSH) | Pull (agent), gRPC over mTLS |
| Config language | Puppet DSL (custom) | Ruby DSL | YAML + Jinja2 | YAML + Jinja2 (SLS) | YAML + Go templates |
| Agent runtime | Ruby | Ruby (omnibus) | None (agentless) | Python | Go (single binary) |
| Secrets | hiera-eyaml, external Vault | Encrypted data bags, Vault | ansible-vault | GPG pillar, Vault | Built-in isopass with `secret:` prefix |
| Secret rotation | Manual/external | Manual/external | Manual/external | Manual/external | Auto-detect + force-run |
| Facts system | Facter | Ohai | setup module | Grains | Pluggable Collector interface |
| Dry run | --noop | --why-run | --check | test=True | DryRun RPC + gvctl preview |
| Node classification | ENC / console groups | Roles + environments | Inventory groups | Targeting (grains/pillars) | Pattern rules in classification.yaml |
| Resource types | 40+ built-in + Forge | 80+ built-in resources | 1300+ modules | 200+ execution modules | 4 core (file, package, service, user) + exec |
| Templating | ERB (Ruby) | ERB (Ruby) | Jinja2 (Python) | Jinja2 (Python) | Go templates (stdlib) |
| Dependency model | DAG (explicit) | Compile-then-converge | Sequential (playbook order) | Requisite system | DAG with topological sort |
| Web UI | PE Console (paid) | Automate (paid) | AWX/Tower | SaltStack Enterprise (paid) | Built-in SPA (included) |
| CLI | puppet CLI | knife CLI | ansible/ansible-playbook | salt CLI | gvctl |
| Webhooks | PE only (paid) | Automate only (paid) | Callback plugins | Reactor system | Built-in with HMAC signing |
| Metrics | Via exporters | Via handlers | Via callback plugins | Limited | Built-in Prometheus endpoint |
| Bootstrap | curl from puppet repos | curl from chef repos | N/A (agentless) | salt-bootstrap | curl from your server |
| Learning curve | HIGH (DSL + Ruby ecosystem) | HIGH (Ruby + cookbook patterns) | LOW (YAML + SSH) | MEDIUM (YAML + Python ecosystem) | LOW (YAML + single binary) |

### Key Competitive Observations

1. **Simplicity gap:** Ansible is popular because of YAML + agentless simplicity. isogov combines Ansible's YAML simplicity with Puppet's pull-based reliability and Go's single-binary deployment. This is an underserved niche.

2. **Enterprise feature gap:** Puppet, Chef, and Salt gate features like webhooks, dashboards, and compliance behind paid enterprise editions. isogov includes these in the open-source core. This is a significant differentiator for small-to-medium teams.

3. **Secrets gap:** No major CM tool has built-in secret rotation awareness. All require external tooling. isogov's isopass integration with auto-rotation detection is genuinely novel.

4. **Resource type gap:** isogov starts with 4 resource types vs Puppet's 40+ and Ansible's 1300+ modules. This is acceptable for v1 if the core types (file, package, service, user) are excellent. Most real-world configs use only 5-10 resource types.

5. **Ecosystem gap:** Puppet Forge has thousands of community modules. Chef Supermarket has thousands of cookbooks. Ansible Galaxy has thousands of roles/collections. isogov has none. This is the biggest long-term challenge, mitigated by the plugin/module system in v2+.

## Notable Gap: Exec Resource

The PROJECT.md spec lists file, package, service, and user as module executors but does not include an exec/command resource. **Every major CM tool has this** (Puppet `exec`, Chef `execute`, Ansible `command`/`shell`, Salt `cmd.run`). It serves as the escape hatch for tasks that don't fit declarative models:

- Running database migrations
- Reloading firewall rules
- Compiling a binary
- Running a custom script

Recommendation: Add an `exec` resource type with idempotency guards (`onlyif`, `unless`, `creates`) to v1.x. Without it, users will hit a wall quickly when they need to do something the four core resource types cannot express.

## Sources

- [Chef vs Puppet vs Ansible: DevOps Tool Comparison (2026)](https://www.veritis.com/blog/chef-vs-puppet-vs-ansible-comparison-of-devops-management-tools/)
- [Ansible vs Puppet vs Chef vs SaltStack: Configuration Management Tool Comparison](https://dasroot.net/posts/2026/02/ansible-vs-puppet-vs-chef-vs-saltstack-configuration-management/)
- [Ansible vs Puppet vs Chef vs SaltStack: 2025 Comparison](https://teachmeansible.com/blog/configuration-management-comparison)
- [Puppet Enterprise 2025.7 Release Notes](https://jasonstcyr.com/2026/01/09/puppet-enterprise-2025-7-delivers-code-assist-and-rhel-10-primary-support-and-new-scm/)
- [Puppet Resource Type Reference](https://www.puppet.com/docs/puppet/7/type.html)
- [Chef Infra Overview](https://docs.chef.io/chef_overview/)
- [Ohai System Configuration Data](https://docs.chef.io/ohai/)
- [Ansible in 2025: Best Practices](https://www.gocodeo.com/post/ansible-in-2025-best-practices-for-configuration-and-provisioning)
- [Salt Project Overview](https://docs.saltproject.io/salt/user-guide/en/latest/topics/overview.html)
- [SaltStack Review 2026](https://www.siit.io/tools/trending/saltstack-review)
- [Configuration Management Push vs Pull](https://ianbicking.org/blog/2010/03/configuration-management-push-vs-pull.html)
- [Idempotent Configuration Management (TechTarget)](https://www.techtarget.com/searchitoperations/tip/Idempotent-configuration-management-sets-things-right-no-matter-what)
- [Config Drift Detection: Top Open Source Tools 2025](https://www.ai-infra-link.com/mastering-config-drift-detection-top-open-source-tools-for-2025/)
- [GitOps Definition (GitLab)](https://about.gitlab.com/topics/gitops/)
- [Ansible Modules Types Explained](https://blog.cloudmylab.com/ansible-modules-types-explained)

---
*Feature research for: Configuration Management Systems*
*Researched: 2026-03-05*
