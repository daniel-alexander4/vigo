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

### Apples-to-apples: 4 vCPU, 8 GB RAM, 5-minute check-in

All estimates assume the same hardware (4 vCPU, 8 GB RAM, SSD storage) running one server process with a 5-minute agent check-in interval.

| | Nodes on this hardware | Bottleneck | What runs out first |
|---|---|---|---|
| **Hobnob** | **50K-200K (theoretical*)** | SQLite write batching (50K) or PostgreSQL connections (200K) | Neither CPU nor RAM — I/O on the write path |
| **Puppet** | ~1,000-2,000 | JRuby catalog compilation (1-2s per node × 4 threads) | CPU — Ruby interpretation saturates all 4 cores |
| **Chef** | ~3,000-5,000 | PostgreSQL + cookbook serving | RAM — 8 GB is Chef Server's bare minimum |
| **Ansible** | ~300-500 | SSH fork parallelism (~50 forks × 30s per host) | RAM + time — can't push to 500 hosts in 5 minutes |
| **Salt** | ~5,000-8,000 | Python state compilation on master | CPU — Python GIL limits concurrency |
| **CFEngine** | ~50,000+ (proven) | Disk I/O on policy serving | Almost nothing — C binary, flat file serving |

#### How the math works

**Hobnob:** FleetIndex lookup is ~300µs per check-in (cache hit). At 4 cores, throughput is ~10,000 check-ins/sec. A 5-minute interval means 300 seconds to process all check-ins: 10,000 × 300 = 3,000,000 theoretical throughput. RAM for 200K thralls: ~78 MB (200K × 388 bytes). The actual bottleneck is SQLite — flushing 50K+ dirty records every 10 seconds exceeds comfortable write latency. Switching to PostgreSQL pushes the ceiling to ~200K. The conservative 50K-200K range also accounts for GC pauses, policy cache misses (10-50ms each after config changes), network I/O, and the principle that benchmarks don't predict production.

**Puppet:** 8 GB must hold JRuby heap (2-3 GB for 4 instances), PuppetDB's PostgreSQL (2 GB), and the OS (500 MB). That leaves little headroom. Each catalog compilation takes 1-2 seconds of CPU time (Ruby interpreting manifests, querying Hiera, building the resource graph). With 4 JRuby instances: 2-4 compilations/sec. Over 5 minutes: 3 × 300 = 900 nodes (at ~1.5s average). Puppet PE documentation recommends dedicated compile masters at 4,000 nodes, but that assumes 16+ GB RAM with a separate PuppetDB server.

**Chef:** Chef Infra Server's minimum documented requirement is 4 GB RAM. On 8 GB with PostgreSQL, Bookshelf (cookbook object store), and the Erlang front-end, you're at minimum spec. Chef's check-in is lighter than Puppet's (serving pre-built cookbooks, not compiling), so throughput is higher per-core, but RAM is the constraint.

**Ansible:** Push model doesn't map directly to check-in intervals. To converge every 5 minutes, you need `ansible-pull` with cron or Tower/AWX scheduling. Each fork consumes ~100 MB Python process. On 8 GB with 50 forks and 30s average playbook runtime: 50 × (300s / 30s) = 500 hosts per 5-minute cycle. More hosts require Tower clustering.

**Salt:** Salt Master uses ZeroMQ (efficient), but state compilation is Python (single-threaded per minion). Documentation suggests 5,000 minions per master as a baseline. On 4 vCPU with 8 GB, the Python GIL and state compilation overhead limit throughput.

**CFEngine:** The C hub serves flat policy files with minimal computation. Memory overhead is ~1-5 KB per host. 50,000 hosts uses ~250 MB. The bottleneck is disk I/O reading policy files, easily handled by SSD. CFEngine has documented deployments at this scale.

*Hobnob figures are theoretical — zero production deployments exist. See [Scalability Analysis](../../devdocs/scalability.md) for benchmark methodology.

### Memory budget on 8 GB

| | Server base | Per-node cost | 10K nodes | 50K nodes | Fits in 8 GB? |
|---|---|---|---|---|---|
| Hobnob | ~200 MB (Go + SQLite) | 388 bytes | 4 MB | 20 MB | Yes — 7.5 GB free |
| Puppet | ~4.5 GB (JRuby + PuppetDB) | ~80 KB | 800 MB | 4 GB | Barely — 0 headroom at 50K |
| Chef | ~4 GB (Erlang + PostgreSQL) | ~30 KB | 300 MB | 1.5 GB | Yes at 10K, tight at 50K |
| Salt | ~2 GB (Python + ZeroMQ) | ~20 KB | 200 MB | 1 GB | Yes |
| CFEngine | ~100 MB (C binary) | ~3 KB | 30 MB | 150 MB | Yes — 7.5 GB free |

Hobnob and CFEngine use less than 3% of available RAM at 50K nodes. Puppet uses over 100%.

### Check-in cost comparison

| | Per-check-in server cost | DB ops on check-in | What happens per check-in |
|---|---|---|---|
| Hobnob | ~300 µs (cache hit) | Zero | In-memory FleetIndex lookup + pre-built policy cache copy |
| Puppet | 1-2s (catalog compile) | 3-5 PuppetDB queries | Ruby: parse manifests → resolve Hiera → query exported resources → build catalog graph → serialize |
| Chef | ~100ms (cookbook serve) | 2-3 PostgreSQL queries | Resolve run list → serve cookbook files → update node object |
| Salt | ~20ms (state compile) | Cache lookup | Python: compile state tree → resolve pillar → serialize |
| CFEngine | ~1ms (policy serve) | None | Serve flat policy files from disk |

Over a 5-minute window with 10,000 nodes checking in (~33/sec):

| | Server CPU usage | Can it keep up? |
|---|---|---|
| Hobnob | ~1% (33 × 300µs = 10ms/sec) | Yes — 99% idle |
| Puppet | ~400% (33 × 1.5s = 49.5s of CPU per second of wall time) | No — needs 12+ cores or compile masters |
| Chef | ~33% (33 × 100ms = 3.3s/sec) | Yes — but tight |
| Salt | ~66% (33 × 20ms = 660ms/sec) | Yes |
| CFEngine | ~3% (33 × 1ms = 33ms/sec) | Yes — 97% idle |

At 10,000 nodes, Puppet physically cannot keep up on 4 cores — it needs more than 4 seconds of CPU time per second of wall time. Every other tool handles it comfortably. Hobnob and CFEngine are essentially idle.

### Scaling architecture

| | Horizontal approach | Automatic failover | Config sync | Enrollment routing |
|---|---|---|---|---|
| Hobnob | Hub-spoke spanner | Yes (auto-drain at configurable threshold) | Yes (tar.gz push on publish) | Yes (hostname pattern matching) |
| Puppet | Compile masters + PuppetDB replication | Manual | Via code manager / r10k | Via ENC or Hiera |
| Chef | Multi-org, Chef HA (deprecated), Automate | Manual | Via Chef Server replication | N/A |
| Ansible | Tower/AWX clusters | Via HA proxy | Via SCM (git) | N/A (push model) |
| Salt | Multi-master (syndic) | Manual failover | Via gitfs | Via top.sls targeting |
| CFEngine | Hub-spoke (Mission Portal) | Manual | Via policy hub push | Via `classes` |

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
