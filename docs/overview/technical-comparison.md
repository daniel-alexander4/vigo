# Technical Comparison

A factual comparison of Hobnob against five established configuration management systems — Puppet, CFEngine, Chef, Ansible, and SaltStack — covering architecture, security, performance, expressiveness, scalability, and operational characteristics.

Where capacity and performance are compared, all tools are evaluated against the same reference hardware: **4 vCPU, 8 GB RAM, SSD storage, with a 5-minute agent check-in interval.** Vendor-documented requirements are cited where available. Hobnob has no production deployments — its figures are theoretical estimates derived from internal benchmarks and should be read as projections, not measured results.

## Architecture at a Glance

| | Hobnob | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| Model | Agent pull (gRPC) | Agent pull (HTTPS) | Agent pull (HTTPS) | Agentless push (SSH) | Agent pull+push (ZeroMQ) | Agent pull (custom) |
| Server language | Go | Ruby/JRuby + Clojure (PuppetDB) | Ruby + Erlang (Chef Server) | Python | Python | C |
| Agent language | Rust | Ruby + C (Facter) | Ruby | None (SSH) | Python | C |
| Config language | YAML | Puppet DSL | Ruby DSL | YAML + Jinja2 | YAML + Jinja2 | Promise DSL |
| Transport security | gRPC over mTLS + ED25519 signatures | HTTPS + client certs | HTTPS + API tokens | SSH | ZeroMQ + AES | Custom TLS |
| State store (server) | SQLite | PostgreSQL (PuppetDB) | PostgreSQL | None | SQLite/MySQL/PostgreSQL | None |
| State store (agent) | LMDB | YAML cache | JSON cache | None | Msgpack cache | BerkeleyDB |
| Agent binary size | ~5 MB (static musl) | ~200 MB (Ruby + gems) | ~100 MB (omnibus) | 0 (agentless) | ~50 MB (Python + deps) | ~3 MB |

## Transport and Security

### Authentication model

Hobnob uses **three-layer authentication**: mTLS for the transport, ED25519 signature verification on every agent payload (timestamp + body signed with agent's private key, verified against stored public key), and one-time bcrypt-hashed enrollment tokens. This means even if TLS is somehow compromised, an attacker can't forge agent requests without the private key.

Puppet uses HTTPS with client certificates — solid but single-layer. No per-request payload signing. Chef uses HTTPS with API tokens (RSA-signed requests) — similar to Hobnob but with RSA instead of ED25519. Salt uses AES-encrypted ZeroMQ with a shared master key — any agent that has the master key can impersonate any other agent. Ansible uses SSH — strong authentication but requires key distribution.

CFEngine uses its own TLS implementation with host key verification. Similar trust model to Puppet but without a standard PKI.

**Verdict:** Hobnob and Chef have the strongest per-request authentication. Salt's shared-key model is the weakest for multi-tenant environments.

### Secrets management

| | Approach | Secrets in DB? | Secrets in config files? | Secrets in logs? |
|---|---|---|---|---|
| Hobnob | `secret:` prefix resolves from encrypted local store or external API. Fail-fast if provider unreachable. | Never | Never (prefix only) | Never (redacted) |
| Puppet | Hiera + eyaml (encrypted YAML values) or external lookup | Via Hiera backends | Encrypted inline | Possible without care |
| Chef | Encrypted data bags or Chef Vault | In data bags (encrypted) | In recipes (encrypted) | Possible |
| Ansible | ansible-vault (encrypts whole files) | N/A | Encrypted files on disk | `no_log: true` required |
| Salt | Pillars with GPG encryption | In pillar (encrypted) | In pillar files | Possible |
| CFEngine | No built-in secrets management | N/A | Plaintext or custom | No built-in protection |

Hobnob's approach is the cleanest architecturally — secrets are never stored alongside config, never touch the database, and the resolution is a first-class server-side operation rather than an encryption layer bolted onto the config format.

## Performance and Scalability

### Check-in hot path

Hobnob's check-in path is designed for zero database operations. The FleetIndex (in-memory thrall index) provides O(1) lookups. Config matching is pre-computed at load time. The only I/O on the check-in path is the gRPC round-trip itself.

Puppet's agent check-in triggers catalog compilation on the server — the Puppet master evaluates the node's manifest, resolves Hiera data, compiles a resource graph, and serializes it. This involves Ruby interpretation, PuppetDB queries, and potentially Hiera backend lookups. At scale, compile masters are needed.

Chef's check-in downloads cookbooks and converges locally — the server load is lighter (just serving cookbook files), but the client does significant work resolving recipes.

Salt's check-in is lightweight (ZeroMQ message) but state compilation happens on the master.

CFEngine's agent evaluates promises locally from cached policy — the server interaction is minimal (just policy distribution).

**Verdict:** Hobnob and CFEngine have the lightest agent-side check-in paths. Puppet's is the heaviest. On the server side, Hobnob's FleetIndex keeps fleet state in ~388 bytes per node (in-process memory), while CFEngine Enterprise's reporting hub requires ~8 MB per node in PostgreSQL. Hobnob's design target is 50K-200K thralls on a single commodity server.

### Agent resource consumption

| | Memory (idle) | CPU (convergence) | Disk |
|---|---|---|---|
| Hobnob | ~8 MB RSS | Minimal (compiled Rust) | ~5 MB binary + LMDB state |
| Puppet | ~200 MB RSS (Ruby VM) | High (Ruby interpretation) | ~200 MB + YAML cache |
| Chef | ~150 MB RSS (Ruby VM) | High (Ruby interpretation) | ~100 MB + cookbook cache |
| Salt | ~80 MB RSS (Python) | Moderate | ~50 MB + state cache |
| CFEngine | ~5 MB RSS | Minimal (compiled C) | ~3 MB binary + BDB |

Hobnob and CFEngine are in the same class for resource consumption. Ruby-based tools (Puppet, Chef) are an order of magnitude heavier. This matters for edge computing, IoT, containers, and cost-conscious cloud deployments.

### Offline operation

| | Cached convergence | Pending results | Automatic reconnect |
|---|---|---|---|
| Hobnob | Yes (signed bundles in LMDB, configurable TTL) | Yes (queued in LMDB, drained on reconnect) | Yes (exponential backoff) |
| Puppet | Yes (cached catalog) | No (results dropped) | Yes |
| Chef | No | No | Yes |
| Salt | Partial (cached states) | No | Yes |
| CFEngine | Yes (cached promises) | No | Yes (autonomous) |

Hobnob's offline convergence is the most complete — signed policy bundles with TTL, local trait evaluation, results queuing, and automatic drain on reconnect. CFEngine's autonomous operation is philosophically similar but lacks the results queue.

## Configuration Expressiveness

### Config language comparison

| Capability | Hobnob | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| Language type | YAML | Custom DSL | Ruby | YAML + Jinja2 | YAML + Jinja2 | Promise DSL |
| Turing complete | No | Yes | Yes | Effectively (Jinja2) | Effectively (Jinja2) | Yes |
| Conditionals | `when:` expressions, `case/match`, `conditional_block` | `if/elsif/else`, `case`, selectors | Ruby if/case | `when:` conditions, Jinja2 | Jinja2 if/else | `ifvarclass`, `or`, `and` |
| Iteration | `foreach` on vars | `each`, resource collectors | Ruby loops | `loop`, `with_items` | Jinja2 loops | `slist` iteration |
| Functions | Built-in `when:` builtins | 100+ stdlib functions | Ruby methods | 100+ filters + plugins | Jinja2 filters | 50+ body functions |
| Data hierarchy | Module vars → node vars → env overrides → conditional vars | Hiera (unlimited depth, pluggable backends) | Attributes (role → env → node) | Variable precedence (22 levels) | Pillar (targeted data) | `def.json` augments |
| Custom resource types | Custom executor (JSON protocol) | Defined types + custom types (Ruby) | Custom resources (Ruby) | Custom modules (Python) | Custom states (Python) | Custom promise types |
| Template engine | Go templates in `content:` only | ERB everywhere | ERB everywhere | Jinja2 everywhere | Jinja2 everywhere | Variable expansion |

Hobnob intentionally limits where templates can appear (`content:` attributes only) to prevent the "templates in filenames and commands" anti-pattern that plagues Ansible and Salt deployments. This is a readability trade-off — less flexible but configs are always auditable without evaluating expressions.

### Composition model

Hobnob's five-layer composition (glob matching → roles with includes → modules with ordering → resource tools → templates) covers most real-world cases without a programming language. See [Composition Patterns](composition-patterns.md) for detailed examples.

Puppet's class/defined-type system is more expressive — you can create parameterized resource types that act like new primitives. Chef's custom resources are similarly powerful. Hobnob's module system is simpler but less composable for deep abstraction hierarchies.

Ansible's role system is the closest analog to Hobnob's roles, but Ansible roles can include tasks, handlers, files, templates, defaults, vars, and meta — a more complex structure. Hobnob's roles are just named module lists.

**Verdict:** Puppet and Chef win on raw expressiveness. Hobnob and Ansible/Salt win on accessibility. CFEngine's promise DSL is powerful but has the steepest learning curve.

## Idempotency and Resource Model

All six tools enforce idempotency — check current state, act only if drift is detected. The differences are in depth.

### Resource type coverage

| Resource type | Hobnob | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| File (content, permissions, owner) | Yes | Yes | Yes | Yes | Yes | Yes |
| Package (install, remove, version) | Yes | Yes (20+ providers) | Yes | Yes | Yes | Yes |
| Service (start, stop, enable) | Yes | Yes | Yes | Yes | Yes | Yes |
| User/Group | Yes | Yes | Yes | Yes | Yes | Yes |
| Cron | Yes | Yes | Yes | Yes | Yes | Yes |
| Repository (apt, yum) | Yes | Yes | Yes | Yes | Yes | Via commands promises |
| Archive (download, extract) | Yes | Via puppet-archive | Yes | Yes | Yes | Via commands promises |
| Exec (command, guards) | Yes | Yes | Yes | Yes | Yes | Yes (commands promises) |
| Mount | Yes | Yes | Yes | Yes | Yes | Yes (storage promises) |
| SELinux contexts | Yes | Yes | Yes | Yes | Yes | No |
| Firewall (iptables, pf) | Yes (pf) | Via puppetlabs-firewall | No (use exec) | Yes | No (use exec) | No |
| Systemd dropin | Yes | Yes | Yes | Partial | No | No |
| File line editing | Via stream_edit | Via file_line/augeas | Via line cookbook | lineinfile/blockinfile | file.line | Yes (edit_line bundles) |
| Windows registry | Yes | Yes | Yes | Yes | Yes | No |
| Windows service | Yes | Yes | Yes | Yes | Yes | Yes (basic) |
| Network devices | Yes (SSH transport) | Via device modules | No | Yes (network modules) | Via proxy minions | No |
| Kubernetes resources | No | Via puppetlabs-kubernetes | No (use Habitat) | Yes (k8s modules) | No | No |
| Cloud resources | No | Via cloud modules | No | Yes (300+ cloud modules) | Via cloud modules | No |
| LVM/RAID | No | Yes | No | Yes | No | No |
| Docker containers | No | Via puppetlabs-docker | Via docker cookbook | Yes (docker modules) | Via dockerng | No |

Hobnob has 98 built-in executors covering the core system administration needs. Puppet has the deepest built-in resource coverage. Ansible has the widest (cloud, network, containers) through its module ecosystem. Hobnob's custom executor JSON protocol allows extending coverage without modifying the agent binary.

### Package management depth

Puppet's `package` resource is a benchmark — it supports 20+ providers (apt, yum, dnf, brew, chocolatey, pip, gem, npm, etc.), version pinning, holding, purging, install options, and source packages. Hobnob's package executor handles 8 package managers (apt, yum, dnf, apk, brew, pkg, chocolatey, IPS) with install/remove, exact version pinning, and `allow_downgrade` for safe version changes. Setting `version: "1.4.9-1"` ensures that exact version is installed on every convergence — if the package drifts (manual upgrade, `apt upgrade`), the agent corrects it within the next check-in interval. This is functionally equivalent to `apt-mark hold` for CM-managed systems, since the agent continuously enforces the desired version. The remaining gaps are source package installs and the long tail of provider-specific flags (e.g., `--enablerepo`, `install_options`).

## Scalability

> **Note on methodology:** All tools are compared on the same reference hardware: 4 vCPU, 8 GB RAM, SSD storage, 5-minute check-in interval. Vendor-documented requirements are cited where available. Hobnob's figures are theoretical estimates derived from internal benchmarks — no production deployments exist. These comparisons are intended to illustrate architectural differences, not to make absolute performance claims. Real-world results depend on configuration complexity, network conditions, and workload patterns.

### Vendor-documented requirements

| | Documented capacity at vendor-recommended hardware | Vendor source |
|---|---|---|
| **Puppet** | Up to 2,500 nodes on standard architecture (12 cores, 24 GB RAM). Large architecture (2,500-20,000) requires 16 cores, 32 GB primary + dedicated compilers (6 cores, 12 GB each, adding 1,500-3,000 nodes each). Extra-large (20,000+) adds a dedicated PE-PostgreSQL node (16 cores, 128 GB). | [PE 2025 Hardware Requirements](https://www.puppet.com/docs/pe/2025.0/hardware_requirements.html) |
| **Chef** | Minimum 4 GB RAM (under 33 client runs/min). Disk allocation of ~2 MB per node. Chef Infra Server is deprecated (EOL November 2026), replaced by Chef 360 Platform. | [Chef System Requirements](https://docs.chef.io/chef_system_requirements/) |
| **Ansible** | ~1 GB per 10 forks + 2 GB base. 4 forks per CPU core baseline. 400 forks requires ~42 GB RAM. Each fork ~100 MB. | [AAP 2.5 System Requirements](https://docs.redhat.com/en/documentation/red_hat_ansible_automation_platform/2.5/html/rpm_installation/platform-system-requirements) |
| **Salt** | 5,000-50,000 minions per master depending on hardware. 8 GB and 8 cores sufficient for ~1,200 minions. Documentation notes 50,000 minions possible on 64-bit OS without modification. | [Salt at Scale](https://docs.saltproject.io/en/latest/topics/tutorials/intro_scale.html) |
| **CFEngine** | Minimum 8 MB RAM per bootstrapped agent on the Enterprise hub. 5,000 hosts requires 40 GB RAM, 500 GB disk. Community edition without reporting hub is lighter. | [CFEngine 3.26 Installation](https://docs.cfengine.com/docs/3.26/getting-started-installation-general-installation-installation-enterprise.html) |
| **Hobnob** | No vendor-documented capacity (no production deployments). Internal benchmarks: ~388 bytes per thrall in FleetIndex, ~300µs per check-in on cache hit, ~10,000 check-ins/sec on 4 cores. Conservative estimate: 50K-200K thralls. | [Scalability Analysis](../../devdocs/scalability.md) |

### Estimated capacity on reference hardware (4 vCPU, 8 GB RAM, 5-min check-in)

| | Estimated nodes | Primary constraint | Notes |
|---|---|---|---|
| **Hobnob** | 50K-200K* | SQLite write batching (50K); PostgreSQL extends to 200K | *Theoretical. FleetIndex uses ~388 bytes/node. 200K nodes = ~78 MB fleet memory. |
| **Puppet** | ~100-500 | Below vendor minimum. PE requires 10 GB RAM for 100 nodes, 24 GB for 2,500. | 8 GB is below the documented minimum for any production tier. |
| **Chef** | ~2,000-4,000 | 8 GB meets minimum (4 GB base + node overhead at ~2 MB disk/node). | Chef's check-in is cookbook serving, lighter than catalog compilation. |
| **Ansible** | ~200-400 | ~50 concurrent forks at 100 MB each. Push model: 50 forks × 5 min / 30s = ~500 hosts/cycle. | Requires Tower/AWX for scheduled convergence. |
| **Salt** | ~1,200-5,000 | Salt docs indicate 8 GB + 8 cores handles ~1,200 minions. ZeroMQ transport is efficient. | Python state compilation is the CPU bottleneck. |
| **CFEngine** | ~1,000 | Enterprise hub: 8 MB/node × 1,000 = 8 GB. Community edition without reporting DB handles more. | Enterprise reporting requires PostgreSQL; community edition serves flat policy files only. |

*Hobnob's estimate is based on benchmark extrapolation, not production measurements. The conservative range accounts for GC pauses, policy cache misses after config changes (10-50ms vs 300µs cache hit), network I/O contention, and SQLite WAL checkpoint overhead. See [Scalability Analysis](../../devdocs/scalability.md) for methodology.

### Server memory per node

| | Per-node server memory | Source |
|---|---|---|
| Hobnob | ~388 bytes | FleetIndex struct size (measured) |
| Puppet | Varies by facts/catalog size | PuppetDB stores full fact sets + catalogs in PostgreSQL |
| Chef | ~2 MB disk per node | Chef docs: "allocate 2 MB for each node" |
| Ansible | ~100 MB per concurrent fork | Red Hat docs: "1 GB per 10 forks" |
| Salt | Varies by pillar/state complexity | No per-node figure documented |
| CFEngine | ~8 MB per node (Enterprise hub) | CFEngine docs: "not lower than 8MB per bootstrapped agent" |

### Check-in characteristics

| | What happens per check-in | Server-side computation |
|---|---|---|
| Hobnob | In-memory FleetIndex lookup, pre-built policy cache copy, async batched DB writes | Minimal — ~300µs per check-in (benchmarked, cache hit) |
| Puppet | Parse manifests, resolve Hiera data, query PuppetDB, compile catalog graph, serialize | Significant — Ruby/JRuby interpretation, multiple DB queries |
| Chef | Resolve run list, serve cookbook files, update node object | Moderate — cookbook file serving + PostgreSQL writes |
| Ansible | (Push model — no agent check-in. Controller compiles and pushes via SSH.) | N/A for check-in; compilation happens on the controller |
| Salt | Compile state tree, resolve pillar data, serialize | Moderate — Python state compilation per minion |
| CFEngine | Serve flat policy files; Enterprise hub ingests run reports | Minimal for policy serving; moderate for report ingestion |

### Scaling architecture

| | Horizontal approach | Automatic failover | Config sync | Enrollment routing |
|---|---|---|---|---|
| Hobnob | Hub-spoke spanner | Yes (auto-drain at configurable threshold) | Yes (tar.gz push on publish) | Yes (hostname pattern matching) |
| Puppet | Compile masters + PuppetDB replication | Manual | Via code manager / r10k | Via ENC or Hiera |
| Chef | Multi-org, Chef HA (deprecated), Automate | Manual | Via Chef Server replication | N/A |
| Ansible | Tower/AWX clusters | Via HA proxy | Via SCM (git) | N/A (push model) |
| Salt | Multi-master (syndic) | Manual failover | Via gitfs | Via top.sls targeting |
| CFEngine | Hub-spoke (Mission Portal) | Manual | Via policy hub push | Via `classes` |

## Total Cost of Ownership

> **Methodology:** TCO estimates below cover license, server infrastructure, external databases, agent fleet overhead, and operational staffing for a **1,000-node fleet** and a **10,000-node fleet**, both on cloud infrastructure. Infrastructure costs assume commodity cloud VMs (e.g., AWS, Hetzner, DigitalOcean). License figures are included where publicly available — most CM vendors do not publish per-node pricing. Agent fleet overhead is the aggregate RAM consumed by agents across all managed nodes, valued at cloud memory cost (~$5/GB/month). Staffing estimates represent the prorated portion of a full-time employee's salary ($150K/year baseline for a mid-level infrastructure engineer) dedicated to CM operations — maintaining the server infrastructure, writing and reviewing configs, debugging convergence failures, managing upgrades, and tuning performance. These are rough estimates and depend heavily on organization size and automation maturity.

### TCO at 1,000 nodes (annual estimate)

| Cost component | Hobnob | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| **License** | ~$77K | ~$112K (est. ~$112/node)&dagger; | Not published | Not published | Open source or not published | Community free; Enterprise not published |
| **Server infrastructure** | 1 server (4 vCPU, 8 GB) ~$1.2K | Primary (12 cores, 24 GB) + PuppetDB ~$6K | Chef Server + PostgreSQL ~$4.8K | Controller + Tower/AWX ~$2.4K | Salt master (8 cores, 8 GB) ~$2.4K | Enterprise hub (8+ GB) ~$2.4K |
| **External database** | None (SQLite) $0 | PostgreSQL (PuppetDB) ~$3.6K | PostgreSQL ~$3.6K | PostgreSQL (Tower) ~$2.4K | Optional $0–2.4K | PostgreSQL (Enterprise) ~$3.6K |
| **Agent fleet overhead** | 1,000 × 8 MB = 8 GB ~$0.5K | 1,000 × 200 MB = 200 GB ~$12K | 1,000 × 150 MB = 150 GB ~$9K | No agent $0 | 1,000 × 80 MB = 80 GB ~$4.8K | 1,000 × 5 MB = 5 GB ~$0.3K |
| **Dedicated staffing** | Sysadmin (~0.25 FTE) ~$38K | Puppet engineer (~1.0 FTE) ~$150K | Chef engineer (~1.0 FTE) ~$150K | Ansible engineer (~0.5 FTE) ~$75K | Salt engineer (~0.5 FTE) ~$75K | CFEngine engineer (~1.0 FTE) ~$150K |
| **Estimated total** | **~$117K** | **~$284K** | ~$167K + license | ~$80K + license | ~$82K + license | ~$156K + license |

### TCO at 10,000 nodes (annual estimate)

| Cost component | Hobnob | Puppet | Chef | Ansible | Salt | CFEngine |
|---|---|---|---|---|---|---|
| **License** | ~$620K | ~$1.12M (est. ~$112/node)&dagger; | Not published | Not published | Open source or not published | Community free; Enterprise not published |
| **Server infrastructure** | 1 server (4 vCPU, 8 GB) ~$1.2K | Primary + 3–4 compile masters (6 cores, 12 GB each) + PuppetDB ~$24K | Chef Server cluster + PostgreSQL HA ~$18K | Controller cluster + Automation Hub ~$9.6K | 2–3 Salt masters (syndic) ~$7.2K | Enterprise hub (80+ GB) ~$12K |
| **External database** | None (SQLite) $0 | PostgreSQL HA (PuppetDB) ~$12K | PostgreSQL HA ~$12K | PostgreSQL HA (Tower) ~$8.4K | Optional $0–8.4K | PostgreSQL (80+ GB for reporting) ~$12K |
| **Agent fleet overhead** | 10,000 × 8 MB = 80 GB ~$4.8K | 10,000 × 200 MB = 2 TB ~$120K | 10,000 × 150 MB = 1.5 TB ~$90K | No agent $0 | 10,000 × 80 MB = 800 GB ~$48K | 10,000 × 5 MB = 50 GB ~$3K |
| **Dedicated staffing** | Sysadmin (~0.5 FTE) ~$75K | 2.0 FTE Puppet engineers ~$300K | 2.0 FTE Chef engineers ~$300K | 1.5 FTE Ansible engineers ~$225K | 1.5 FTE Salt engineers ~$225K | 1.5 FTE CFEngine engineers ~$225K |
| **Estimated total** | **~$701K** | **~$1.58M** | ~$420K + license | ~$243K + license | ~$280K + license | ~$255K + license |

### Reading the TCO tables

**&dagger; Puppet pricing note.** Puppet does not publish current per-node pricing — the ~$112/node/year figure is based on Puppet Enterprise Standard list pricing that was widely cited in industry analyses and partner price sheets prior to Puppet's acquisition by Perforce in 2022. Actual Puppet pricing may vary by contract. Puppet also offers Premium (~$199/node) and volume discounts. Contact Puppet for current quotes.

**Agent fleet overhead is real money at scale.** At 10,000 nodes, the 200 MB Puppet agent consumes 2 TB of aggregate RAM across the fleet. At cloud memory prices, that's ~$120K/year in RAM that could be used for application workloads. Hobnob's 8 MB agent and CFEngine's 5 MB agent are an order of magnitude cheaper on the managed nodes.

**Staffing is the hidden cost.** Puppet and Chef require specialized knowledge (Puppet DSL, Ruby cookbooks, Hiera hierarchies, r10k workflows, compile master tuning). Hobnob's YAML-only config path means any sysadmin can operate it without dedicated training. This is the hardest cost to quantify and the easiest to underestimate.

**What's not included:** Network bandwidth, training costs, migration effort, downtime risk, security incident costs, and the opportunity cost of engineer time spent on CM infrastructure instead of product work.

**Where figures are unavailable:** Chef, Ansible (Red Hat AAP), Salt (VMware), and CFEngine Enterprise do not publish per-node pricing. Their license costs could range from $0 (open-source editions) to $50–200/node/year (enterprise editions). Contact vendors for current quotes. The "Estimated total" rows for these tools show infrastructure + staffing only — add their license to get true TCO.

## Operational Characteristics

### Bootstrap and enrollment

| | First-time agent setup | Time to first convergence |
|---|---|---|
| Hobnob | `curl \| bash` — one binary, generates keys, enrolls, starts service | ~30 seconds |
| Puppet | Install puppet-release package, install puppet-agent, configure server, sign cert | ~5 minutes |
| Chef | Download omnibus installer, configure client.rb, bootstrap with knife | ~3 minutes |
| Ansible | Install SSH key on target (or use password) | Immediate (push) |
| Salt | Install salt-minion package, configure master, accept key | ~2 minutes |
| CFEngine | Install cfengine-community package, bootstrap to hub | ~1 minute |

Hobnob and CFEngine have the fastest bootstrap. Puppet's certificate signing workflow is the most involved.

### Observability

| | Built-in metrics | Compliance tracking | Web UI | Event webhooks |
|---|---|---|---|---|
| Hobnob | Prometheus (36 metrics) | Per-thrall status, sparklines, SIEM/CMDB export | HTMX dashboard + admin section | Yes (HMAC-signed) |
| Puppet | Via PuppetDB queries | Puppet Enterprise reports | Puppet Enterprise Console | Via report processors |
| Chef | Via Chef Automate | Chef Automate compliance | Chef Automate UI | Via handlers |
| Ansible | Via Tower/AWX | Tower compliance reports | Tower/AWX UI | Via callback plugins |
| Salt | Via Salt Enterprise | Via Salt Enterprise | Salt Enterprise UI | Via returners/reactors |
| CFEngine | Mission Portal metrics | Mission Portal compliance | Mission Portal | Via custom reports |

The open-source versions of Puppet, Chef, Salt, and CFEngine have limited or no web UI — their dashboards are part of the commercial/enterprise products. Hobnob's web UI, compliance tracking, and admin section are included in the base product.

## When to Choose Hobnob

- **Free for up to 25 nodes** — all features, no time limit, no credit card
- When you value **simplicity** — YAML configs anyone can read, one server, one binary, one publish command. No DSL to learn — not true of Puppet manifests, Chef recipes, or CFEngine promises
- When you value **security** — mTLS everywhere, ED25519 per-request signing on every payload (no other CM tool does this), and a first-class `secret:` resolver that never lets secrets touch config files, databases, logs, or wire payloads
- When you value **ease of operation** — single process, embedded SQLite, bootstrap in 30 seconds with a single curl command. No package manager, no runtime dependencies, no external database
- When you value **performance** — ~300 µs check-ins, zero database queries on the hot path, 3,300 check-ins per second on a single core
- When you need **scalability** — 50,000+ nodes on a single server, no compile masters, no database clusters, no worker pools
- When **agent footprint** matters — 5 MB static binary vs 50–200 MB for Ruby/Python-based agents. Deploys anywhere — edge, IoT, containers, resource-constrained hosts
- When you need **offline resilience** — signed policy bundles with local convergence and results queuing for air-gapped, shipboard, or unreliable network environments. Most complete offline story of any agent-based CM tool
- When **total cost of ownership** matters — less than half the TCO of Puppet at 1,000 nodes, with infrastructure and staffing costs near zero

