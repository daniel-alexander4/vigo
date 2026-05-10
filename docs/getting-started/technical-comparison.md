
# Technical Comparison

A factual comparison of Vigo against five established configuration management systems — Puppet, CFEngine, Chef, Ansible, and SaltStack — covering architecture, security, performance, expressiveness, scalability, and operational characteristics.

Where capacity and performance are compared, all tools are evaluated against the same reference hardware: **4 vCPU, 8 GB RAM, SSD storage, with a 5-minute agent check-in interval.** Vendor-documented requirements are cited where available.

> **On Vigo's numbers.** Vigo has no production deployment and no end-to-end load test. Every Vigo figure below is one of: (a) a Go microbenchmark from the codebase (`go test -bench`), (b) a value read directly out of the source (`unsafe.Sizeof`, registry counts, binary size on disk), or (c) a single ad-hoc observation from the developer's home-lab fleet — labelled as such. This document **does not extrapolate a node-count ceiling** from those inputs; that projection, with its assumptions stated, lives in the [Performance Analysis](performance-1m.md) pages. Read the comparison as architecture-and-cost, not as a capacity guarantee.

## Architecture at a Glance

| | Vigo | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| Model | Agent pull (gRPC) | Agent pull (HTTPS) | Agent pull (HTTPS) | Agentless push (SSH) | Agent pull+push (ZeroMQ) | Agent pull (custom) |
| Server language | Go | Ruby/JRuby + Clojure (PuppetDB) | Ruby + Erlang (Chef Server) | Python | Python | C |
| Agent language | Rust | Ruby + C (Facter) | Ruby | None (SSH) | Python | C |
| Config language | YAML | Puppet DSL | Ruby DSL | YAML + Jinja2 | YAML + Jinja2 | Promise DSL |
| Transport security | gRPC over mTLS + ED25519 signatures | HTTPS + client certs | HTTPS + API tokens | SSH | ZeroMQ + AES | Custom TLS |
| State store (server) | SQLite | PostgreSQL (PuppetDB) | PostgreSQL | None | SQLite/MySQL/PostgreSQL | None |
| State store (agent) | LMDB | YAML cache | JSON cache | None | Msgpack cache | BerkeleyDB |
| Agent binary size | ~8.1 MiB x86_64 / ~6.2 MiB arm64 (static musl, measured) | ~200 MB (Ruby + gems) | ~100 MB (omnibus) | 0 (agentless) | ~50 MB (Python + deps) | ~3 MB |

## Transport and Security

### Authentication model

Vigo uses **three-layer authentication**: mTLS for the transport, ED25519 signature verification on every agent payload (timestamp + body signed with agent's private key, verified against stored public key), and one-time bcrypt-hashed enrollment tokens. This means even if TLS is somehow compromised, an attacker can't forge agent requests without the private key.

Puppet uses HTTPS with client certificates — solid but single-layer. No per-request payload signing. Chef uses HTTPS with API tokens (RSA-signed requests) — similar to Vigo but with RSA instead of ED25519. Salt uses AES-encrypted ZeroMQ with a shared master key — any agent that has the master key can impersonate any other agent. Ansible uses SSH — strong authentication but requires key distribution.

CFEngine uses its own TLS implementation with host key verification. Similar trust model to Puppet but without a standard PKI.

**Verdict:** Vigo and Chef have the strongest per-request authentication. Salt's shared-key model is the weakest for multi-tenant environments.

### Secrets management

| | Approach | Secrets in DB? | Secrets in config files? | Secrets in logs? |
|---|---|---|---|---|
| Vigo | `secret:` prefix resolves from encrypted local store or external API. Fail-fast if provider unreachable. | Never | Never (prefix only) | Never (redacted) |
| Puppet | Hiera + eyaml (encrypted YAML values) or external lookup | Via Hiera backends | Encrypted inline | Possible without care |
| Chef | Encrypted data bags or Chef Vault | In data bags (encrypted) | In recipes (encrypted) | Possible |
| Ansible | ansible-vault (encrypts whole files) | N/A | Encrypted files on disk | `no_log: true` required |
| Salt | Pillars with GPG encryption | In pillar (encrypted) | In pillar files | Possible |
| CFEngine | No built-in secrets management | N/A | Plaintext or custom | No built-in protection |

Vigo's approach is the cleanest architecturally — secrets are never stored alongside config, never touch the database, and the resolution is a first-class server-side operation rather than an encryption layer bolted onto the config format.

## Performance and Scalability

### Check-in hot path

Vigo's check-in path is designed for zero database operations. The FleetIndex (in-memory envoy index) provides O(1) lookups. Config matching is pre-computed at load time. The only I/O on the check-in path is the gRPC round-trip itself.

Puppet's agent check-in triggers catalog compilation on the server — the Puppet master evaluates the node's manifest, resolves Hiera data, compiles a resource graph, and serializes it. This involves Ruby interpretation, PuppetDB queries, and potentially Hiera backend lookups. At scale, compile masters are needed.

Chef's check-in downloads cookbooks and converges locally — the server load is lighter (just serving cookbook files), but the client does significant work resolving recipes.

Salt's check-in is lightweight (ZeroMQ message) but state compilation happens on the master.

CFEngine's agent evaluates promises locally from cached policy — the server interaction is minimal (just policy distribution).

**Verdict:** Vigo and CFEngine have the lightest agent-side check-in paths. Puppet's is the heaviest. On the server side, Vigo holds fleet state in process — the fixed `EnvoyState` struct is **416 bytes** (`unsafe.Sizeof`, `server/fleet/index.go`), though the resident cost per envoy is dominated by the per-envoy trait map (`Traits map[string]string` — dozens to low-hundreds of keys on a typical host) and the bounded recent-run history, so the real figure is materially larger and fleet-dependent. By contrast, CFEngine Enterprise's reporting hub documents ~8 MB per node in PostgreSQL. No measured single-server node ceiling exists for Vigo — see the disclaimer above.

### Agent resource consumption

| | Memory (idle) | CPU (convergence) | Disk |
|---|---|---|---|
| Vigo | ~20–25 MB RSS¹ | Minimal (compiled Rust) | ~8 MiB binary (x86_64) / ~6 MiB (arm64) + LMDB state |
| Puppet | ~200 MB RSS (Ruby VM) | High (Ruby interpretation) | ~200 MB + YAML cache |
| Chef | ~150 MB RSS (Ruby VM) | High (Ruby interpretation) | ~100 MB + cookbook cache |
| Salt | ~80 MB RSS (Python) | Moderate | ~50 MB + state cache |
| CFEngine | ~5 MB RSS | Minimal (compiled C) | ~3 MB binary + BDB |

¹ Single observed sample from a long-running home-lab envoy (`agent_self.rss_bytes`); not yet characterized across operating systems, uptimes, or policy weights — treat as indicative, not a spec. The static binary is ~8 MiB on disk.

Vigo is close to CFEngine for resource consumption and well under the Ruby/Python-based tools (Puppet, Chef, Salt). This matters for edge computing, IoT, containers, and cost-conscious cloud deployments.

### Offline operation

| | Cached convergence | Pending results | Automatic reconnect |
|---|---|---|---|
| Vigo | Yes (signed bundles in LMDB, configurable TTL) | Yes (queued in LMDB, drained on reconnect) | Yes (exponential backoff) |
| Puppet | Yes (cached catalog) | No (results dropped) | Yes |
| Chef | No | No | Yes |
| Salt | Partial (cached states) | No | Yes |
| CFEngine | Yes (cached promises) | No | Yes (autonomous) |

Vigo's offline convergence is the most complete — signed policy bundles with TTL, local trait evaluation, results queuing, and automatic drain on reconnect. CFEngine's autonomous operation is philosophically similar but lacks the results queue.

## Configuration Expressiveness

### Config language comparison

| Capability | Vigo | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| Language type | YAML | Custom DSL | Ruby | YAML + Jinja2 | YAML + Jinja2 | Promise DSL |
| Turing complete | No | Yes | Yes | Effectively (Jinja2) | Effectively (Jinja2) | Yes |
| Conditionals | `when:` expressions, `case/match`, `conditional_block` | `if/elsif/else`, `case`, selectors | Ruby if/case | `when:` conditions, Jinja2 | Jinja2 if/else | `ifvarclass`, `or`, `and` |
| Iteration | `foreach:` on resources **and modules** (a module instantiated once per list item — the defined-type analog) | `each`, resource collectors | Ruby loops | `loop`, `with_items` | Jinja2 loops | `slist` iteration |
| Functions | Built-in `when:` builtins (+ `tag()`/`host()`/`trait()` in `fleet:` selectors) | 100+ stdlib functions | Ruby methods | 100+ filters + plugins | Jinja2 filters | 50+ body functions |
| Data hierarchy | `common.vgo` directory inheritance (unbounded depth; modules/roles accumulate, vars last-wins) → role `includes:` → match-block vars → `environments.vgo` overrides | Hiera (unlimited depth, pluggable backends, per-key merge strategies) | Attributes (role → env → node) | Variable precedence (22 levels) | Pillar (targeted data) | `def.json` augments |
| Cross-node config | `fleet:` lookup — a var resolved server-side at check-in from the live fleet trait index (LB pools, monitoring targets, cluster member lists) | Exported resources + collectors (via PuppetDB) | `search()` over the Chef Server index | `hostvars`/`groups` within a play | Salt Mine | Hub-served collected data |
| Custom resource types | `type: custom` executor (JSON protocol) | Defined types + custom types (Ruby) | Custom resources (Ruby) | Custom modules (Python) | Custom states (Python) | Custom promise types |
| Template engine | Go templates in `content:` & resource attributes — interpolation only, no `{{ if }}`/`{{ range }}` (the one exception: a `fleet:` `select:` is a full Go template, server-side) | ERB everywhere | ERB everywhere | Jinja2 everywhere | Jinja2 everywhere | Variable expansion |

Vigo intentionally keeps `content:`/attribute templates interpolation-only — no control flow in a config file's body — to prevent the "templates in filenames and commands, `{{ if }}` in every config" anti-pattern that plagues Ansible and Salt deployments. Per-instance shaping is a `when:`-gated companion resource or a narrower `fleet:` query. This is a readability trade-off — less flexible, but configs are auditable without evaluating expressions and `modlint` can statically validate a whole stockpile.

### Composition model

Vigo's six-layer composition — directory inheritance (`common.vgo`) → glob matching → roles with `includes:` → modules (with ordering, and module-level `foreach:`) → resource tools (`foreach:`/`case:`/`conditional_block:`/`when:`/`fleet:`) → templates — covers most real-world cases without a programming language. See [Composition Patterns](../engineering/writing-modules/composition-patterns.md) for detailed examples.

Puppet's class/defined-type system is still more expressive for *deep* abstraction — unlimited class composition vs. Vigo's single-level role `includes:`, typed parameters vs. Vigo's untyped `vars:`/`.Each` items, and arbitrary logic inside a defined-type body vs. Vigo's `{{ .Each.x }}` interpolation + `when:`-gated resources. But the core "instantiate a parameterized resource group N times" pattern that Puppet defined types and Chef custom resources are built on is now a Vigo primitive too: a module referenced with `foreach:` (`{name: vhost, foreach: vhosts, key: server_name}` → `vhost[www.example.com]`, …). Vigo's module system is simpler; the remaining gap is depth, not the abstraction itself.

Ansible's role system is the closest analog to Vigo's roles, but Ansible roles can include tasks, handlers, files, templates, defaults, vars, and meta — a more complex structure. Vigo's roles are just named module lists.

**Verdict:** Puppet and Chef win on raw expressiveness. Vigo and Ansible/Salt win on accessibility. CFEngine's promise DSL is powerful but has the steepest learning curve.

## Idempotency and Resource Model

All six tools enforce idempotency — check current state, act only if drift is detected. The differences are in depth and frequency.

Vigo's idempotency is **enforced continuously**, not just applied once at deploy time. Every resource executor follows a strict check → act → verify cycle on every convergence pass, and the agent runs a full pass every check-in interval. At 15-second intervals, this means drift from manual changes, package upgrades, cron jobs, or external automation is detected and corrected within seconds — not minutes or hours. This is what makes Vigo a continuous enforcement engine rather than a configuration deployment tool.

### Resource type coverage

| Resource type | Vigo | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| File (content, permissions, owner) | Yes | Yes | Yes | Yes | Yes | Yes |
| Package (install, remove, version) | Yes | Yes (20+ providers) | Yes | Yes | Yes | Yes |
| Service (start, stop, enable) | Yes | Yes | Yes | Yes | Yes | Yes |
| User/Group | Yes | Yes | Yes | Yes | Yes | Yes |
| Cron | Yes | Yes | Yes | Yes | Yes | Yes |
| Repository (apt, yum) | Yes | Yes | Yes | Yes | Yes | Via commands promises |
| Source package (download, extract) | Yes | Via puppet-archive | Yes | Yes | Yes | Via commands promises |
| Non-repo package (.deb/.rpm/.msi from URL) | Yes | Via package with source | Via remote_file + dpkg | Via apt deb:/dnf URL | Via pkg.installed sources | Via commands promises |
| Exec (command, guards) | Yes | Yes | Yes | Yes | Yes | Yes (commands promises) |
| Mount | Yes | Yes | Yes | Yes | Yes | Yes (storage promises) |
| SELinux contexts | Yes | Yes | Yes | Yes | Yes | No |
| Firewall (iptables, pf) | Yes (UFW, pf, Windows Firewall) | Via puppetlabs-firewall | No (use exec) | Yes | No (use exec) | No |
| Systemd dropin | Yes | Yes | Yes | Partial | No | No |
| File line editing | Yes (file_line, blockinfile, replace, field_edit, ini, json_file, stream_edit) | Via file_line/augeas | Via line cookbook | lineinfile/blockinfile | file.line | Yes (edit_line bundles) |
| Windows registry | Yes | Yes | Yes | Yes | Yes | No |
| Windows service | Yes | Yes | Yes | Yes | Yes | Yes (basic) |
| Network devices | Yes (SSH transport) | Via device modules | No | Yes (network modules) | Via proxy minions | No |
| Kubernetes resources | No | Via puppetlabs-kubernetes | No (use Habitat) | Yes (k8s modules) | No | No |
| Cloud resources | No | Via cloud modules | No | Yes (300+ cloud modules) | Via cloud modules | No |
| LVM/RAID | No | Yes | No | Yes | No | No |
| Docker containers | Yes (compose, container, image) | Via puppetlabs-docker | Via docker cookbook | Yes (docker modules) | Via dockerng | No |

Vigo registers **69 built-in resource types** (`agent/src/runner/mod.rs::build_executors`, excluding the test-only `mock`), counting platform variants (`reboot` vs `reboot_windows`, etc.) once each as the operator writes them. Puppet has the deepest built-in resource coverage. Ansible has the widest (cloud, network, containers) through its module ecosystem. Vigo's custom executor JSON protocol allows extending coverage without modifying the agent binary.

### Package management depth

Puppet's `package` resource is a benchmark — it supports 20+ providers (apt, yum, dnf, brew, chocolatey, pip, gem, npm, etc.), version pinning, holding, purging, install options, and source packages. Vigo's package executor handles 14 package managers — apt, dnf, yum, zypper, pacman, apk, brew, pkg, pkg_add, pkgin, IPS, Chocolatey, winget, and Scoop — with install/remove, exact version pinning, and `allow_downgrade` for safe version changes. Setting `version: "1.4.9-1"` ensures that exact version is installed on every convergence — if the package drifts (manual upgrade, `apt upgrade`), the agent corrects it within the next check-in interval. This is functionally equivalent to `apt-mark hold` for CM-managed systems, since the agent continuously enforces the desired version. The remaining gaps are source package installs and the long tail of provider-specific flags (e.g., `--enablerepo`, `install_options`).

## Scalability

> **Note on methodology:** All tools are compared on the same reference hardware: 4 vCPU, 8 GB RAM, SSD storage, 5-minute check-in interval. The non-Vigo rows are derived from each vendor's documented requirements. Vigo's rows carry only what is directly measurable in the codebase (microbenchmarks, struct sizes, binary size, registry counts) — no production deployment exists, and this section deliberately stops short of a node-count estimate (see the top-of-document disclaimer). These comparisons illustrate architectural differences, not absolute performance.

### Vendor-documented requirements

| | Documented capacity at vendor-recommended hardware | Vendor source |
|---|---|---|
| **Puppet** | Up to 2,500 nodes on standard architecture (12 cores, 24 GB RAM). Large architecture (2,500-20,000) requires 16 cores, 32 GB primary + dedicated compilers (6 cores, 12 GB each, adding 1,500-3,000 nodes each). Extra-large (20,000+) adds a dedicated PE-PostgreSQL node (16 cores, 128 GB). | [PE 2025 Hardware Requirements](https://www.puppet.com/docs/pe/2025.0/hardware_requirements.html) |
| **Chef** | Minimum 4 GB RAM (under 33 client runs/min). Disk allocation of ~2 MB per node. Chef Infra Server is deprecated (EOL November 2026), replaced by Chef 360 Platform. | [Chef System Requirements](https://docs.chef.io/chef_system_requirements/) |
| **Ansible** | ~1 GB per 10 forks + 2 GB base. 4 forks per CPU core baseline. 400 forks requires ~42 GB RAM. Each fork ~100 MB. | [AAP 2.5 System Requirements](https://docs.redhat.com/en/documentation/red_hat_ansible_automation_platform/2.5/html/rpm_installation/platform-system-requirements) |
| **Salt** | 5,000-50,000 minions per master depending on hardware. 8 GB and 8 cores sufficient for ~1,200 minions. Documentation notes 50,000 minions possible on 64-bit OS without modification. | [Salt at Scale](https://docs.saltproject.io/en/latest/topics/tutorials/intro_scale.html) |
| **CFEngine** | Minimum 8 MB RAM per bootstrapped agent on the Enterprise hub. 5,000 hosts requires 40 GB RAM, 500 GB disk. Community edition without reporting hub is lighter. | [CFEngine 3.26 Installation](https://docs.cfengine.com/docs/3.26/getting-started-installation-general-installation-installation-enterprise.html) |
| **Vigo** | No vendor-documented capacity (no production deployment, no end-to-end load test). Measured microbenchmarks (`go test -bench`, dev hardware): ED25519 payload verify **~53 µs** (the dominant per-check-in cost — `BenchmarkVerifyPayload`); the rest of the cache-hit check-in path totals **<1 µs** (timestamp check ~32 ns, pubkey lookup ~143 ns, policy-cache hit ~19 ns, per-envoy stub customization ~300 ns, in-memory mark-seen ~170 ns); policy-bundle rebuild on a cache miss **~5.5 µs** on a small test config (`BenchmarkBuildPolicyBundle`, config-size dependent); `EnvoyState` struct **416 B**. A node-count ceiling is not derived here. | [Performance Analysis](performance-1m.md) |

### Estimated capacity on reference hardware (4 vCPU, 8 GB RAM, 5-min check-in)

| | Estimated nodes | Primary constraint | Notes |
|---|---|---|---|
| **Vigo** | Not estimated here* | — | *No production deployment, no end-to-end load test; deriving a ceiling from the microbenchmarks above would be extrapolation, which this document avoids. Projection scenarios (with stated assumptions) are in [Performance Analysis](performance-1m.md). |
| **Puppet** | ~100-500 | Below vendor minimum. PE requires 10 GB RAM for 100 nodes, 24 GB for 2,500. | 8 GB is below the documented minimum for any production tier. |
| **Chef** | ~2,000-4,000 | 8 GB meets minimum (4 GB base + node overhead at ~2 MB disk/node). | Chef's check-in is cookbook serving, lighter than catalog compilation. |
| **Ansible** | ~200-400 | ~50 concurrent forks at 100 MB each. Push model: 50 forks × 5 min / 30s = ~500 hosts/cycle. | Requires Tower/AWX for scheduled convergence. |
| **Salt** | ~1,200-5,000 | Salt docs indicate 8 GB + 8 cores handles ~1,200 minions. ZeroMQ transport is efficient. | Python state compilation is the CPU bottleneck. |
| **CFEngine** | ~1,000 | Enterprise hub: 8 MB/node × 1,000 = 8 GB. Community edition without reporting DB handles more. | Enterprise reporting requires PostgreSQL; community edition serves flat policy files only. |

*The other rows are derived from each vendor's documented requirements scaled to the reference hardware; Vigo has no vendor doc and no production deployment, so it carries no estimate here. The factors that would have to be folded into any such estimate — GC pauses, policy-cache misses after config changes, network I/O contention, SQLite WAL checkpoint overhead — are exactly why this document does not publish one. See [Performance Analysis](performance-1m.md), where projections are made explicitly and labelled theoretical.

### Server memory per node

| | Per-node server memory | Source |
|---|---|---|
| Vigo | 416 B fixed struct; larger in practice (trait map + run history dominate) | `unsafe.Sizeof(EnvoyState{})`, `server/fleet/index.go` |
| Puppet | Varies by facts/catalog size | PuppetDB stores full fact sets + catalogs in PostgreSQL |
| Chef | ~2 MB disk per node | Chef docs: "allocate 2 MB for each node" |
| Ansible | ~100 MB per concurrent fork | Red Hat docs: "1 GB per 10 forks" |
| Salt | Varies by pillar/state complexity | No per-envoy figure documented |
| CFEngine | ~8 MB per node (Enterprise hub) | CFEngine docs: "not lower than 8MB per bootstrapped agent" |

### Check-in characteristics

| | What happens per check-in | Server-side computation | Practical minimum interval |
|---|---|---|---|
| Vigo | In-memory FleetIndex lookup, pre-built policy cache copy, async batched DB write | ED25519 verify **~53 µs** (benchmarked — the dominant cost); rest of the cache-hit path **<1 µs**; end-to-end including gRPC/protobuf/TLS framing not separately benchmarked | **15 s** documented ([15-second analysis](performance-15s.md)); on a non-trivial policy the agent's own convergence-cycle time, not the configured interval, is the practical floor |
| Puppet | Parse manifests, resolve Hiera data, query PuppetDB, compile catalog graph, serialize | Significant — Ruby/JRuby interpretation, multiple DB queries | ~5 minutes |
| Chef | Resolve run list, serve cookbook files, update node object | Moderate — cookbook file serving + PostgreSQL writes | ~5 minutes |
| Ansible | (Push model — no agent check-in. Controller compiles and pushes via SSH.) | N/A for check-in; compilation happens on the controller | N/A |
| Salt | Compile state tree, resolve pillar data, serialize | Moderate — Python state compilation per minion | ~1 minute |
| CFEngine | Serve flat policy files; Enterprise hub ingests run reports | Minimal for policy serving; moderate for report ingestion | ~1 minute |

The check-in path is cheap — its dominant cost is the ~53 µs ED25519 verify, with everything else on the cache-hit path under a microsecond — which is what makes sub-minute intervals plausible at all. The [15-second](performance-15s.md) and [5-second](performance-5s.md) analyses work through the *projected* CPU, memory, and network at those cadences; those numbers are theoretical (no production deployment) and the pages say so.

### Scaling architecture

| | Horizontal approach | Automatic failover | Config sync | Enrollment routing |
|---|---|---|---|---|
| Vigo | Hub-spoke spanner | Yes (auto-drain at configurable threshold) | Yes (tar.gz push on publish) | Yes (hostname pattern matching) |
| Puppet | Compile masters + PuppetDB replication | Manual | Via code manager / r10k | Via ENC or Hiera |
| Chef | Multi-org, Chef HA (deprecated), Automate | Manual | Via Chef Server replication | N/A |
| Ansible | Tower/AWX clusters | Via HA proxy | Via SCM (git) | N/A (push model) |
| Salt | Multi-master (syndic) | Manual failover | Via gitfs | Via top.sls targeting |
| CFEngine | Hub-spoke (Mission Portal) | Manual | Via policy hub push | Via `classes` |

## Operational Characteristics

### Bootstrap and enrollment

| | First-time agent setup | Time to first convergence |
|---|---|---|
| Vigo | `curl \| bash` — one binary, generates keys, enrolls, starts service | ~30 seconds |
| Puppet | Install puppet-release package, install puppet-agent, configure server, sign cert | ~5 minutes |
| Chef | Download omnibus installer, configure client.rb, bootstrap with knife | ~3 minutes |
| Ansible | Install SSH key on target (or use password) | Immediate (push) |
| Salt | Install salt-minion package, configure master, accept key | ~2 minutes |
| CFEngine | Install cfengine-community package, bootstrap to hub | ~1 minute |

Vigo and CFEngine have the fastest bootstrap. Puppet's certificate signing workflow is the most involved.

### Observability

| | Built-in metrics | Compliance tracking | Web UI | Event webhooks |
|---|---|---|---|---|
| Vigo | Prometheus — 56 metric definitions (`server/metrics/metrics.go`; more time series at runtime via labels) | Per-envoy status, sparklines, SIEM/CMDB export | HTMX dashboard + security posture + admin section | Yes (HMAC-signed) |
| Puppet | Via PuppetDB queries | Puppet Enterprise reports | Puppet Enterprise Console | Via report processors |
| Chef | Via Chef Automate | Chef Automate compliance | Chef Automate UI | Via handlers |
| Ansible | Via Tower/AWX | Tower compliance reports | Tower/AWX UI | Via callback plugins |
| Salt | Via Salt Enterprise | Via Salt Enterprise | Salt Enterprise UI | Via returners/reactors |
| CFEngine | Mission Portal metrics | Mission Portal compliance | Mission Portal | Via custom reports |

The open-source versions of Puppet, Chef, Salt, and CFEngine have limited or no web UI — their dashboards are part of the commercial/enterprise products. Vigo's web UI, compliance tracking, and admin section are included in the base product.

## When to Choose Vigo

- **Free for up to 100 nodes** — all features, no time limit, no credit card
- When you value **simplicity** — YAML configs anyone can read, one server, one binary, one publish command. No DSL to learn — not true of Puppet manifests, Chef recipes, or CFEngine promises
- When you value **security** — mTLS everywhere, ED25519 per-request signing on every payload (no other CM tool does this), and a first-class `secret:` resolver that never lets secrets touch config files, databases, logs, or wire payloads
- When you value **ease of operation** — single process, embedded SQLite, bootstrap in 30 seconds with a single curl command. No package manager, no runtime dependencies, no external database
- When you value **performance** — the per-check-in path's dominant cost is a ~53 µs ED25519 verify (benchmarked); everything else on the cache-hit path totals under a microsecond; zero database queries on the hot path (the state write is batched and asynchronous). Check-in intervals scale down to 15 seconds on commodity hardware per the [performance analyses](performance-15s.md) (theoretical)
- When you need **continuous enforcement** — every resource is idempotent and re-evaluated every check-in. At 15-second intervals, drift is corrected before anyone notices it happened. This enables provable continuous compliance, self-healing infrastructure, and sub-minute incident response — capabilities that require 5-30 minute intervals with any other tool
- When you need **scalability** — no compile masters, no database clusters, no worker pools; the spanner hub-spoke topology adds horizontal scale on top. (No production-measured node ceiling — see the disclaimer at the top.)
- When **agent footprint** matters — ~8 MiB static musl binary (x86_64; ~6 MiB on arm64) vs 50–200 MB for Ruby/Python-based agents. Deploys anywhere — edge, IoT, containers, resource-constrained hosts
- When you need **offline resilience** — signed policy bundles with local convergence and results queuing for air-gapped, shipboard, or unreliable network environments. Most complete offline story of any agent-based CM tool
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
