# Technical Comparison

An honest comparison of Hobnob against five established configuration management systems: Puppet, CFEngine, Chef, Ansible, and SaltStack. This evaluates architecture, security, performance, expressiveness, and operational characteristics.

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

**Verdict:** Hobnob and CFEngine have the lightest server-side check-in paths. Puppet's is the heaviest. This matters at scale — Hobnob's design target is 50K-200K thralls on a single server.

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

| Resource type | Hobnob | Puppet | Chef | Ansible | Salt |
|---|---|---|---|---|---|
| File (content, permissions, owner) | Yes | Yes | Yes | Yes | Yes |
| Package (install, remove, version) | Yes | Yes (20+ providers) | Yes | Yes | Yes |
| Service (start, stop, enable) | Yes | Yes | Yes | Yes | Yes |
| User/Group | Yes | Yes | Yes | Yes | Yes |
| Cron | Yes | Yes | Yes | Yes | Yes |
| Repository (apt, yum) | Yes | Yes | Yes | Yes | Yes |
| Archive (download, extract) | Yes | Via puppet-archive | Yes | Yes | Yes |
| Exec (command, guards) | Yes | Yes | Yes | Yes | Yes |
| Mount | Yes | Yes | Yes | Yes | Yes |
| SELinux contexts | Yes | Yes | Yes | Yes | Yes |
| Firewall (iptables, pf) | Yes (pf) | Via puppetlabs-firewall | No (use exec) | Yes | No (use exec) |
| Systemd dropin | Yes | Yes | Yes | Partial | No |
| File line editing | Via stream_edit | Via file_line/augeas | Via line cookbook | lineinfile/blockinfile | file.line |
| Windows registry | Yes | Yes | Yes | Yes | Yes |
| Windows service | Yes | Yes | Yes | Yes | Yes |
| Network devices | Yes (SSH transport) | Via device modules | No | Yes (network modules) | Via proxy minions |
| Kubernetes resources | No | Via puppetlabs-kubernetes | No (use Habitat) | Yes (k8s modules) | No |
| Cloud resources | No | Via cloud modules | No | Yes (300+ cloud modules) | Via cloud modules |
| LVM/RAID | No | Yes | No | Yes | No |
| Docker containers | No | Via puppetlabs-docker | Via docker cookbook | Yes (docker modules) | Via dockerng |

Hobnob has 98 built-in executors covering the core system administration needs. Puppet has the deepest built-in resource coverage. Ansible has the widest (cloud, network, containers) through its module ecosystem. Hobnob's custom executor JSON protocol allows extending coverage without modifying the agent binary.

### Package management depth

Puppet's `package` resource is a benchmark — it supports 20+ providers (apt, yum, dnf, brew, chocolatey, pip, gem, npm, etc.), version pinning, holding, purging, install options, and source packages. Hobnob's package executor handles 8 package managers (apt, yum, dnf, apk, brew, pkg, chocolatey, IPS) with install/remove, exact version pinning, and `allow_downgrade` for safe version changes. Setting `version: "1.4.9-1"` ensures that exact version is installed on every convergence — if the package drifts (manual upgrade, `apt upgrade`), the agent corrects it within the next check-in interval. This is functionally equivalent to `apt-mark hold` for CM-managed systems, since the agent continuously enforces the desired version. The remaining gaps are source package installs and the long tail of provider-specific flags (e.g., `--enablerepo`, `install_options`).

## Scalability

### Single-server capacity

| | Tested/documented capacity | Architecture | Server language overhead |
|---|---|---|---|
| **Hobnob** | **50K-200K thralls (theoretical*)** | In-memory FleetIndex, zero-DB check-in, async batched writes | Go — compiled, low GC pressure with tuning |
| **Puppet** | 4,000 nodes per compile master (PE docs) | PuppetDB (PostgreSQL), catalog compilation per node | Ruby/JRuby — high memory, slow compilation |
| **Chef** | 10,000 nodes per Chef Server (documented) | PostgreSQL + Bookshelf (cookbook storage), Erlang front-end | Erlang + Ruby — moderate |
| **Ansible** | 500-1,000 concurrent forks (practical) | No server state — limited by SSH parallelism and controller memory | Python — single controller bottleneck |
| **Salt** | 10,000+ nodes per master (documented) | ZeroMQ pub/sub, state compilation on master | Python — moderate memory |
| **CFEngine** | 50,000+ nodes per hub (documented, proven) | Minimal server — agents pull flat policy files, no compilation | C — minimal overhead |

*Hobnob's figures are theoretical — zero production deployments exist. The 50K-200K range is deliberately conservative. The benchmarks suggest much higher raw capacity:

- FleetIndex operations measure ~3,300 check-ins/sec on a **single core** at ~300µs per cache-hit check-in
- A 4-core server at that rate handles ~10,000 check-ins/sec, which serves **3 million thralls** at a 5-minute check-in interval
- Per-thrall memory is ~388 bytes — 100K thralls uses ~40 MB of RAM
- SQLite write batching (10s flush interval) is the first bottleneck, hitting limits around 50K-100K thralls

The conservative 50K-200K estimate accounts for: real-world GC pauses under sustained load, policy cache misses after config changes (10-50ms each vs 300µs cache hit), network I/O contention, SQLite WAL checkpoint stalls, trait batch processing overhead, and the general principle that benchmarks on empty data structures don't predict production behavior. The upper bound (200K) assumes PostgreSQL for the write path and Go runtime tuning (`GOGC`, `GOMEMLIMIT`). See [Scalability Analysis](../../devdocs/scalability.md) for full methodology and benchmark data.

### Scaling architecture

| | Horizontal approach | Automatic failover | Config sync | Enrollment routing |
|---|---|---|---|---|
| Hobnob | Hub-spoke spanner | Yes (auto-drain at configurable threshold) | Yes (tar.gz push on publish) | Yes (hostname pattern matching) |
| Puppet | Compile masters + PuppetDB replication | Manual | Via code manager / r10k | Via ENC or Hiera |
| Chef | Multi-org, Chef HA (deprecated), Automate | Manual | Via Chef Server replication | N/A |
| Ansible | Tower/AWX clusters | Via HA proxy | Via SCM (git) | N/A (push model) |
| Salt | Multi-master (syndic) | Manual failover | Via gitfs | Via top.sls targeting |
| CFEngine | Hub-spoke (Mission Portal) | Manual | Via policy hub push | Via `classes` |

### Check-in cost comparison

| | Per-check-in server cost | DB ops on check-in | Hot path |
|---|---|---|---|
| Hobnob | ~300 µs (cache hit) | Zero (async batch flush) | In-memory index + pre-built policy cache |
| Puppet | 500ms-5s (catalog compile) | PuppetDB queries for facts + exported resources | Ruby interpretation + Hiera lookups + catalog build |
| Chef | ~50-200ms (cookbook serve) | PostgreSQL queries for node data | Cookbook file serving + search index |
| Salt | ~10-50ms (state compile) | State cache lookup | Python state compilation |
| CFEngine | ~1ms (policy file serve) | None | Static file serving |

Puppet's catalog compilation is the most expensive per-node operation. CFEngine and Hobnob have the cheapest. The practical difference: Puppet needs compile masters at 4,000 nodes. Hobnob and CFEngine handle 50,000+ on a single server.

### Memory efficiency

| | Per-node server memory | 10K nodes | 100K nodes |
|---|---|---|---|
| Hobnob | ~388 bytes (FleetIndex) | ~4.5 MB | ~40 MB |
| Puppet | ~50-100 KB (PuppetDB + facts) | ~500 MB - 1 GB | 5-10 GB (requires tuning) |
| Chef | ~20-50 KB (node object + search index) | ~200-500 MB | 2-5 GB |
| Salt | ~10-30 KB (minion cache + pillar) | ~100-300 MB | 1-3 GB |
| CFEngine | ~1-5 KB (host record) | ~10-50 MB | ~100-500 MB |

Hobnob and CFEngine are in the same class — orders of magnitude more memory-efficient than Ruby/Python-based tools. This is a direct consequence of compiled languages with intentional data structure design vs. dynamic language object overhead.

Hobnob's spanner is simpler to configure than Puppet's compile master infrastructure or Salt's syndic topology. The auto-failover and config sync features are unusual — most CM tools require manual intervention when a spoke/compile-master goes down.

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

## What Hobnob Does Better

1. **Binary footprint.** 5 MB static binary vs 50-200 MB for Ruby/Python-based agents. Deploys anywhere — IoT, edge, containers, embedded systems.

2. **Transport security.** Per-request ED25519 signatures on top of mTLS. No other CM tool signs individual payloads.

3. **Secrets architecture.** First-class `secret:` resolver that never lets secrets touch config files, databases, logs, or wire payloads. Cleaner than Hiera-eyaml, data bags, vault, or pillars.

4. **Check-in performance.** Zero-DB hot path. O(1) thrall lookup via in-memory FleetIndex. Designed for 50K+ thralls on a single server.

5. **Offline resilience.** Signed bundles with TTL, local convergence, results queue with drain-on-reconnect. Most complete offline story of any agent-based CM tool.

6. **Config readability.** Any sysadmin can read a `.hnb` file without learning a DSL. Not true of Puppet manifests, Chef recipes, or CFEngine promises.

7. **Bootstrap speed.** Single curl command → enrolled and converging in 30 seconds. No package manager, no runtime dependencies.

## What Others Do Better

1. **Ecosystem.** Puppet Forge has 6000+ modules. Ansible Galaxy has 30K+ roles. Hobnob has 98 built-in executors and zero community modules. For common infrastructure (Kubernetes, cloud providers, databases), you'll write modules from scratch.

2. **Expressiveness ceiling.** When you need complex logic (dynamic resource generation, cross-node dependency resolution, inheritance hierarchies), Puppet's DSL and Chef's Ruby give you a real programming language. Hobnob's YAML composition handles 90% of cases but hits a wall on the other 10%.

3. **Testing tools.** Puppet has rspec-puppet, PDK, Litmus. Chef has Test Kitchen, ChefSpec, InSpec. Ansible has Molecule. Hobnob has `validate`, `preview`, and `test` (YAML-based assertions against resolved policy) — functional but less mature than the Ruby/Python test ecosystems.

4. **Compliance frameworks.** Chef InSpec provides CIS benchmark libraries. Puppet has compliance modules. Hobnob has compliance status tracking but no pre-built benchmark content.

5. **Windows depth.** Puppet has deep Windows support built up over a decade of modules. Hobnob now has 22 Windows executors covering services, packages, users, firewall, scheduled tasks, hostname, timezone, registry, ACLs, Windows Features, IIS (sites + app pools), certificates, environment variables, domain membership, local security policy, Windows Update, PowerShell scripts, reboots, Windows Defender, SMB shares, and service recovery actions. The core Windows surface area is well covered; Puppet's advantage is in the long tail of community-contributed modules.

6. **Agentless option.** Ansible's SSH-based push model means zero agent installation. Useful for one-off tasks, heterogeneous environments, and network devices. Hobnob requires an agent on every managed node.

7. **Battle-testing.** Puppet has 15+ years of production use at 40K+ organizations. Every edge case in file permissions, package manager quirks, and OS-specific behavior has been found and fixed. Hobnob has zero production deployments. The 1,900+ tests catch a lot, but real-world deployment finds things tests don't.

## When to Choose Hobnob

- Greenfield deployments where ecosystem depth isn't needed
- Environments where agent footprint matters (edge, IoT, containers)
- Security-conscious organizations that want strong transport auth and secrets handling
- Teams that value config readability over DSL power
- Large fleets where check-in performance is critical
- Air-gapped or unreliable network environments needing offline convergence

## When to Choose Something Else

- **Puppet:** Deep Windows management, established compliance frameworks, massive module ecosystem
- **Ansible:** Agentless requirement, cloud provisioning, ad-hoc tasks across heterogeneous infrastructure
- **Chef:** Application-centric infrastructure (Habitat), Ruby shop with existing Chef investment
- **Salt:** Event-driven infrastructure, real-time remote execution at scale, reactor system
- **CFEngine:** Extreme scale (100K+ nodes), minimal resource footprint with proven C agent, promise theory purity
