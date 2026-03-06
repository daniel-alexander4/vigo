# Pitfalls Research

**Domain:** Pull-based configuration management system (Puppet/Chef-style)
**Researched:** 2026-03-05
**Confidence:** HIGH

## Critical Pitfalls

### Pitfall 1: Non-Atomic File Writes Corrupt Managed Files

**What goes wrong:**
The file module executor writes content directly to the target path. If the agent process is killed mid-write (OOM, SIGKILL, power loss), the managed file is left in a partially-written, corrupt state. The next convergence run sees the SHA256 mismatch and overwrites again, but the service reading that file may have already crashed or loaded garbage. This is especially dangerous for files like `/etc/nginx/nginx.conf` or database configs where a partial write means a service outage.

**Why it happens:**
Developers implement `os.WriteFile(path, content, mode)` as the obvious approach. It works in testing but is not crash-safe. The write and the metadata update (owner, group, mode) are separate syscalls, creating a window where the file has wrong permissions.

**How to avoid:**
Write to a temporary file in the same directory (same filesystem), set owner/group/mode on the temp file, then `os.Rename()` to the target path. `rename(2)` is atomic on POSIX systems when source and destination are on the same filesystem. The sequence is: write temp -> fsync temp -> chmod/chown temp -> rename temp to target -> fsync parent directory.

**Warning signs:**
- File module uses `os.WriteFile` or `ioutil.WriteFile` directly to the target path
- No temporary file pattern in the file executor code
- Owner/mode set after content write (two-step non-atomic update)
- Integration tests that never test interrupted writes

**Phase to address:**
Agent module executors phase -- this must be correct from the first implementation of the file resource type. Retrofitting atomic writes after building on top of direct writes is straightforward but easy to forget.

---

### Pitfall 2: Thundering Herd on Server After Restart or Force-Push-All

**What goes wrong:**
When the server restarts or an admin runs `gvctl push --all`, all agents check in simultaneously. With hundreds of nodes, the server gets hammered with concurrent gRPC CheckIn calls, each requiring config file reads, YAML parsing, secret resolution via isopass, template rendering, DAG computation, and a database write. The server either OOMs, exhausts its database connection pool, or overwhelms isopass with concurrent secret fetches. The irony: the tool meant to ensure uptime causes an outage.

**Why it happens:**
The jitter mechanism only helps during steady-state polling. Force-push sets a DB flag that every agent picks up on its next check-in, but if the server was down for a while, all agents have been backing off and will retry around the same time when it comes back. Even with jitter on the base interval, a server restart creates a synchronization point.

**How to avoid:**
1. Server-side admission control: limit concurrent CheckIn processing with a semaphore (e.g., `make(chan struct{}, maxConcurrent)`). Agents that exceed the limit get a gRPC `RESOURCE_EXHAUSTED` status and retry with their own backoff.
2. Staggered force-push: `gvctl push --all` should set force-run flags with randomized `not_before` timestamps spread over a configurable window (e.g., 5 minutes), not all at once.
3. Agent startup jitter: on boot or after reconnecting to a previously-unavailable server, the agent should sleep for a random duration (0 to base_interval) before its first check-in.
4. Connection pooling for isopass: ensure the secrets provider uses a connection pool or caches resolved secrets with a short TTL, so N concurrent check-ins don't create N separate isopass connections.

**Warning signs:**
- `gvctl push --all` sets all force flags with the same timestamp
- No concurrency limit on the gRPC server's CheckIn handler
- Server CPU/memory spikes correlate with agent fleet size
- isopass becomes a bottleneck under concurrent secret resolution

**Phase to address:**
Server skeleton and agent run loop phases. The server needs admission control from the start. The agent needs startup jitter from its first daemon implementation. Force-push staggering can be added when implementing `gvctl push`.

---

### Pitfall 3: Secrets Leaking into Logs, Error Messages, Run Results, and gRPC Payloads

**What goes wrong:**
A `secret:isogov/db/dsn` value is resolved to `postgres://user:P@ssw0rd@host/db` and that resolved string ends up in a log line, a database `run_results.error` column, a gRPC error detail, a webhook payload, a Prometheus label, or a panic stack trace. Once a secret is in a log file or database row, it is effectively compromised -- log aggregators, backups, and monitoring systems all become secret stores.

**Why it happens:**
This is death by a thousand cuts. Every code path that touches resolved config must be audited. Common leak vectors:
- `fmt.Errorf("failed to connect: %v", dsn)` where dsn contains the resolved secret
- Structured logging that logs the entire config struct
- gRPC interceptors that log request/response payloads at debug level
- Webhook payloads that include full run context
- Template rendering errors that include the template data context in the error message
- Panic recovery that dumps the stack with local variables

**How to avoid:**
1. Create a `SecretString` type that implements `fmt.Stringer` and `encoding.TextMarshaler` to return `[REDACTED]`. Store resolved secrets in this type, not raw strings. Any accidental logging produces `[REDACTED]`.
2. Never pass resolved secret values through gRPC messages. The server resolves secrets and sends the resolved content to the agent, but the content should be opaque bytes in the protobuf, and the agent should never include file content in its `RunReport` -- only the SHA256 hash.
3. Wrap the config struct so `%+v` formatting redacts secret fields.
4. Add a log scrubbing layer that detects and redacts patterns matching known secret paths.
5. Run results in the database should store "drift detected on /etc/myapp/db.conf" not "content changed from X to Y".

**Warning signs:**
- Resolved secrets stored as plain `string` type
- `%v` or `%+v` formatting of config structs in log statements
- Debug-level logging that includes gRPC message contents
- Error messages that embed connection strings or file contents
- No grep for secret values in test log output

**Phase to address:**
Secrets provider phase (first implementation). The `SecretString` type must exist before any code resolves secrets. This is nearly impossible to retrofit once secret values flow through the codebase as plain strings.

---

### Pitfall 4: Notify/Subscribe Creates Hidden Ordering Dependencies and Infinite Loops

**What goes wrong:**
A resource with `notify: [reload-nginx]` triggers `reload-nginx` to re-apply. But if `reload-nginx` itself causes a state change that triggers another resource via its own `notify`, you get a cascade. In the worst case, two resources notify each other, creating an infinite convergence loop. Even without loops, the notify chain creates implicit ordering dependencies that the DAG doesn't model, causing resources to execute in unexpected order.

The other failure mode: a notify is declared but the target resource name is misspelled. The notification silently does nothing. The sysadmin changes a config file, expects nginx to reload, and it doesn't -- with no error anywhere.

**Why it happens:**
Notify/subscribe is syntactic sugar for "run this again if that changed." It creates a second dependency graph (the notification graph) layered on top of the explicit DAG. These two graphs can conflict. Puppet's history is littered with bugs in this area, particularly around the interaction between `require`, `notify`, and `subscribe` creating dependency cycles (you can't require something and notify it simultaneously without creating a cycle).

**How to avoid:**
1. Validate notification targets at config load time: if a resource declares `notify: [foo]` and no resource named `foo` exists, reject the config with a clear error.
2. Build a combined graph that merges explicit `depends_on` edges and implicit `notify/subscribe` edges, then run cycle detection on the combined graph. Reject configs with cycles.
3. Cap notification depth: if a notify triggers a resource that itself notifies, allow at most one level of cascading (configurable). Log a warning if the cap is hit.
4. Record notification chains in run results so the admin can see "reload-nginx ran because write-config changed."
5. Never allow a resource to notify itself.

**Warning signs:**
- Notify targets not validated against resource names at load time
- Notification graph separate from dependency DAG with no cross-validation
- No depth limit on notification cascading
- Run results don't explain why a resource re-applied
- No test for misspelled notify targets

**Phase to address:**
Module dispatch and DAG ordering phase. The combined graph validation must be built when the notify/subscribe feature is implemented. Adding it later means existing configs may have latent bugs.

---

### Pitfall 5: Partial Convergence Leaves System in Worse State Than Before

**What goes wrong:**
A run applies resources in DAG order. Resource 3 of 8 fails (e.g., package install fails because a repo is unreachable). Resources 1-2 succeeded and made changes. Resources 4-8 never run, but some of them depend on resources 1-2 having run. The system is now in a hybrid state: partially converged, not matching the old state, not matching the desired state. Example: base users were created (resource 1-2), nginx config was written referencing those users (resource 3), but nginx service wasn't started (resource 4) -- and the old nginx config that referenced different users is now gone.

**Why it happens:**
Configuration management is inherently non-transactional. Unlike a database transaction, you can't roll back "user was created" or "package was installed" atomically. Each resource is an independent side effect on the system.

**How to avoid:**
1. Fail-fast vs. continue: make the behavior configurable per module. Default to fail-fast (stop on first error) for modules with tight dependencies. Support `on_failure: continue` for independent resources.
2. File snapshots (already spec'd) are the partial mitigation for files. But packages and users have no automatic rollback -- document this clearly.
3. Dry-run before apply: encourage the pattern of `gvctl preview` before `gvctl push`. The dry-run catches most failures before they happen.
4. Record exactly which resources succeeded and which were skipped due to upstream failure. The run report should distinguish "skipped (dependency failed)" from "skipped (already converged)" from "skipped (when condition false)."
5. For critical modules, implement a "pre-check" phase that validates all preconditions (repo reachable, disk space available, dependencies installed) before applying any changes.

**Warning signs:**
- All resources treated as independent with no failure propagation
- Run report doesn't distinguish failure causes for skipped resources
- No pre-check or dry-run mechanism
- File snapshots not taken before writes
- No documentation about non-transactional nature of convergence

**Phase to address:**
Module executors and run loop phases. Failure semantics must be defined before building individual executors, because each executor needs to know whether to continue or stop on failure.

---

### Pitfall 6: File Watcher Misses Config Changes Due to Atomic Editor Saves

**What goes wrong:**
The server watches `config/clients/` for changes to auto-reload client configs. Most text editors (vim, emacs, VS Code) save files by writing to a temp file then renaming it to the target. This generates a RENAME event on the old file, not a WRITE event on the new file. If the watcher is watching individual files (not the directory), it loses the watch entirely because the inode it was watching was unlinked. Even watching the directory, the event sequence (CREATE temp, WRITE temp, RENAME temp to target) can be confusing, and naive event handlers may process the CREATE of the temp file instead of waiting for the RENAME.

On Linux, inotify has a per-user watch limit (default 8192). On NFS or network filesystems, inotify doesn't work at all.

**Why it happens:**
Developers test with `echo "content" > file` which generates a simple WRITE event. Real-world editing tools don't work this way. The fsnotify library documentation explicitly warns against watching individual files for this reason.

**How to avoid:**
1. Watch the directory, not individual files. Filter events by filename.
2. Debounce events: after any event in the config directory, wait 500ms-1s for more events, then reload all configs. This handles the multi-event sequences from atomic saves.
3. On reload, diff the loaded config against the in-memory config to determine what actually changed. Don't rely on which file triggered the event.
4. Fall back to periodic polling (e.g., every 30s stat the directory for mtime changes) as a safety net if fsnotify events are missed.
5. Provide a `gvctl reload` command as a manual trigger for environments where file watching is unreliable (NFS, containers with mounted volumes).

**Warning signs:**
- Config watcher watches individual files instead of directories
- No debouncing of file events
- Testing only with direct file writes, not with editors that use atomic saves
- No fallback mechanism if fsnotify fails
- No manual reload command

**Phase to address:**
Server config loading phase. The file watcher is a core server capability that must be reliable from day one.

---

### Pitfall 7: Go Template Injection via Untrusted YAML Values

**What goes wrong:**
Module YAML files use Go templates with `.Vars` and `.Facts` for variable substitution. If a fact value or variable contains Go template syntax (e.g., a hostname like `{{ .Env.HOME }}`), and the template engine processes it, an attacker who controls a fact value (via a compromised agent) can inject template directives that execute arbitrary template functions. Depending on the template function map, this could read files, call functions, or cause panics.

Even without malicious intent, a user variable containing `{{` will cause template parse errors that are confusing to debug.

**Why it happens:**
Go's `text/template` package processes all `{{` delimiters. If user-supplied data flows into a template as a raw string and the template is re-parsed (a common anti-pattern: building the template string by concatenation then parsing), the user data becomes part of the template.

**How to avoid:**
1. Parse templates from the YAML `content` field first, then execute them with the data context. Never concatenate user data into the template string before parsing.
2. Use a restricted template function map: only expose safe functions (string manipulation, conditionals, loops). Do not expose `call`, OS-level functions, or file reading.
3. Validate that `.Vars` and `.Facts` values don't contain `{{` sequences. If they do, treat them as literal text (which Go templates do correctly when data is passed via the context, not concatenated into the template source).
4. Consider using `html/template` for escaping if template output goes into security-sensitive contexts, though for config files `text/template` with proper data separation is sufficient.
5. Treat agent-reported facts as untrusted input. Validate fact values on the server before using them in template rendering.

**Warning signs:**
- Template strings built by string concatenation with user data
- Unrestricted template function map (includes `call` or OS functions)
- No validation of fact values from agents
- Template parse errors when usernames or hostnames contain special characters

**Phase to address:**
CheckIn and module dispatch phase, when template rendering is first implemented. The template execution model (parse template, then execute with data context) must be correct from the start.

---

### Pitfall 8: mTLS Certificate Rotation Breaks All Agent Connections Simultaneously

**What goes wrong:**
The server's TLS certificate is rotated (via isopass). The server picks up the new cert via `GetCertificate`. But if the CA certificate also changes (e.g., CA rotation or re-signing), every agent still has the old CA cert in `/etc/isogov/ca.crt`. On their next check-in, TLS handshake fails with "certificate signed by unknown authority." Every agent in the fleet simultaneously stops converging. There is no out-of-band mechanism to push the new CA cert to agents because the only communication channel (gRPC over mTLS) is broken.

**Why it happens:**
CA rotation is rare but catastrophic when it happens. The bootstrap process bakes the CA cert into the agent config. Nobody tests CA rotation because it "never happens" -- until it does.

**How to avoid:**
1. Separate server cert rotation from CA rotation. Server certs should be rotated frequently (via isopass hot-reload). CA certs should be long-lived and rotation should be a planned migration.
2. Support CA cert bundles: the agent's `ca_file` should accept multiple PEM certificates. During CA rotation, both old and new CA certs are valid simultaneously. The timeline is: add new CA to bundle -> rotate server cert to be signed by new CA -> remove old CA from bundle after all agents have the new bundle.
3. The agent self-update mechanism can distribute a new CA cert alongside a new binary. Build CA cert distribution into the agent update flow.
4. Implement a health check endpoint that works without mTLS (e.g., plain HTTPS on a separate port) so agents can at least report "I'm alive but can't authenticate."
5. Document the CA rotation procedure explicitly. This is an operational runbook item, not just code.

**Warning signs:**
- CA cert is a single file, not a bundle
- No tested procedure for CA rotation
- Agent has no way to update its CA cert without manual intervention
- No health check that works independently of mTLS
- Server cert and CA cert stored/rotated through the same mechanism with no distinction

**Phase to address:**
TLS setup and secrets integration phases. The CA bundle support must be designed into the agent's TLS config from the start. The rotation procedure should be documented when the bootstrap mechanism is built.

---

### Pitfall 9: Package Module Hits dpkg/apt Locks or Concurrent Package Operations

**What goes wrong:**
The package module executor runs `apt-get install nginx` but another process (unattended-upgrades, another Isogov run, a sysadmin running apt manually) holds the dpkg lock. The executor gets "Could not get lock /var/lib/dpkg/lock" and the entire module fails. On the next convergence, it tries again and may hit the same lock. Meanwhile, the agent reports persistent failures that trigger webhooks and compliance degradation for what is essentially a transient lock contention issue.

On some systems, `apt-get` prompts for confirmation (e.g., "Configuration file '/etc/foo' has been modified. What would you like to do?"). If the executor doesn't handle this, the process hangs indefinitely.

**Why it happens:**
Package managers use system-wide locks. Configuration management is not the only thing that touches packages. Developers test on clean VMs where nothing else runs apt, so they never encounter lock contention.

**How to avoid:**
1. Retry with backoff: if the lock is held, wait and retry (3 attempts, 10s/30s/60s delays). This handles transient locks from unattended-upgrades.
2. Always use non-interactive flags: `DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::="--force-confold" install nginx`. Equivalent flags for other package managers.
3. Set a command timeout: if `apt-get install` doesn't complete within 5 minutes, kill it and report failure rather than hanging forever.
4. Detect stale locks: if the lock file exists but no process holds it (the PID in the lock file is dead), clean up the lock before proceeding.
5. For all package managers, verify the command exits cleanly (check both exit code and stderr for known error patterns).

**Warning signs:**
- Package commands run without non-interactive flags
- No timeout on package manager commands
- No retry logic for lock contention
- Lock errors treated as permanent failures rather than transient
- Testing only on clean systems with no competing package operations

**Phase to address:**
Agent module executors phase, specifically the package executor implementation. The non-interactive flags and retry logic must be part of the initial package manager backend implementations.

---

### Pitfall 10: Agent Self-Update Bricks the Agent

**What goes wrong:**
The agent downloads a new binary, replaces itself, and restarts via systemd. But: the new binary has a bug and crashes on startup. Systemd's restart limit kicks in after 5 rapid restarts and the service enters "failed" state. The node is now unmanaged with no way to recover remotely. Alternatively: the binary download was corrupted (network issue between checksum verification and write), or the new binary doesn't work on this OS/architecture, or the replacement race-conditions with the running process.

**Why it happens:**
Self-update is a unique operation: the agent is replacing itself while running. A bug in the new version can't be fixed by the old version because it no longer exists. Most update mechanisms are tested on the happy path (new version works) and not the sad path (new version crashes, new version is wrong architecture, download is interrupted).

**How to avoid:**
1. Keep the previous binary: rename the old binary to `isogov-agent.previous` before replacing. If the new binary fails to start (detected by a watchdog or health check), automatically roll back to the previous binary.
2. Two-phase update: download new binary -> verify checksum -> run `./isogov-agent-new --self-test` (a flag that performs a basic sanity check and exits) -> only then replace the running binary.
3. Systemd watchdog: configure `WatchdogSec=` in the unit file. The agent must send watchdog notifications periodically. If the new version crashes before sending the first notification, systemd can trigger a rollback script.
4. Version pinning: allow the server to specify minimum and maximum agent versions per client. Don't auto-update to a version that hasn't been tested.
5. Staged rollout: update a small percentage of agents first, monitor for failures, then proceed. The server should support update policies (canary, rolling percentage).

**Warning signs:**
- Old binary deleted before new binary verified
- No `--self-test` or health check for the new binary
- No rollback mechanism if the new binary crashes
- All agents update simultaneously
- No version pinning or staged rollout support

**Phase to address:**
Agent self-update phase. This is a late-phase feature and should not be rushed. Get basic agent functionality solid first, then add self-update with all safety mechanisms.

---

## Technical Debt Patterns

Shortcuts that seem reasonable but create long-term problems.

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Storing resolved secrets as plain `string` | Simpler code, no wrapper types | Secret leaks become inevitable as codebase grows; every log statement is a potential leak | Never -- use `SecretString` type from day one |
| Single goroutine for all CheckIn processing | Simple sequential logic, easy to reason about | Server throughput bottleneck; 100 agents checking in simultaneously wait in a queue | Only for initial prototype; add concurrency control before any real fleet testing |
| Hardcoding `apt-get` in the package module | Works on Ubuntu dev machines immediately | Unusable on RHEL, Alpine, Arch, macOS; must rewrite entire package module | Only if all managed nodes are guaranteed Ubuntu (document the assumption explicitly) |
| Skipping dry-run infrastructure | Faster to ship the "apply" path first | Operators won't trust the tool without preview capability; changes go live untested | MVP only -- dry-run should be implemented immediately after basic apply works |
| Loading all client configs into memory on startup | Simple, fast access, no disk I/O during check-ins | Memory grows linearly with fleet size; 10,000 clients with complex configs = significant memory | Acceptable for fleets under 500 nodes. Above that, load on-demand with an LRU cache |
| No config validation beyond YAML parsing | Ship faster, let runtime errors surface problems | Typos in resource types, invalid modes, non-existent notify targets discovered only when the agent runs | Never -- validate at config load time, not at apply time |
| Polling isopass for every secret on every check-in | Simplest implementation, always fresh | N agents x M secrets = N*M isopass calls per interval; isopass becomes the bottleneck | Never at scale -- cache secrets with TTL, invalidate on rotation event |

## Integration Gotchas

Common mistakes when connecting to external services.

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| isopass | Treating isopass as always-available; no timeout on secret fetch | Set aggressive timeouts (5s). Cache resolved secrets with TTL. If isopass is slow, use last-known-good cached values for non-startup secrets. Startup secrets (DB DSN, TLS certs) must fail-fast. |
| PostgreSQL | Using a single database connection for all operations | Use a connection pool (`pgxpool`) with bounded max connections. Set `max_conns` based on expected concurrent check-ins, not fleet size. Monitor pool exhaustion. |
| PostgreSQL migrations | Running migrations automatically on server startup in multi-replica deployments | Use golang-migrate's advisory lock (`x-lock-table` option) to prevent concurrent migration runs. Better: run migrations as a separate step before starting the server (e.g., init container or Makefile target). |
| systemctl | Assuming systemctl is always available and the service name matches | Check that systemd is the init system (not all Linux systems use it). Verify the service unit file exists before trying to manage it. Handle the case where a service was installed by a package in the same run but systemd hasn't reloaded its units yet (`systemctl daemon-reload`). |
| Package managers (apt/dnf/etc.) | Running package commands without holding the appropriate lock or without non-interactive mode | Always use `DEBIAN_FRONTEND=noninteractive` for apt. Always pass `-y` for confirmation. Set `--no-install-recommends` to avoid surprise dependencies. For dnf/yum, use `-y --setopt=install_weak_deps=False`. |
| fsnotify (file watcher) | Watching individual files instead of directories; not debouncing events | Watch the `config/clients/` directory. Debounce events (500ms). On any event, re-read and diff the entire directory contents. Fall back to periodic polling as safety net. |
| gRPC client connections | Agent creates a new gRPC connection for every check-in | Create the connection once at agent startup, reuse it across check-ins. Use gRPC keepalive to detect dead connections. Reconnect with exponential backoff on connection loss. |

## Performance Traps

Patterns that work at small scale but fail as usage grows.

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Re-resolving all secrets on every check-in | Check-in latency grows linearly with number of secrets in client config | Cache resolved secrets with TTL (e.g., 5 min). Invalidate cache on rotation event from isopass watcher. | >50 secrets per client or >100 clients |
| Reading and parsing all YAML config files on every check-in | Server CPU spikes during check-in bursts; high disk I/O | Parse configs on load/change (via file watcher). Serve from in-memory cache. Only re-parse when file mtime changes. | >200 client config files or frequent check-ins (<5 min interval) |
| Storing full facts JSONB on every check-in | Database grows rapidly; facts table becomes the largest table | Store facts diff (only changed facts). Keep only last N snapshots per client (spec says 10, consider 5). Add a retention policy. | >500 clients checking in every 30 min = ~24K fact rows/day |
| Unbounded `run_results` table growth | Query performance degrades; backup size grows indefinitely | Partition by month. Add retention policy (e.g., keep 90 days). Archive to cold storage if needed. | >1000 clients after 6 months of operation |
| DAG computation on every check-in | CPU overhead for topological sort per client per interval | Cache the computed DAG per client config. Invalidate only when the config file changes. DAG computation is deterministic given the same config. | >50 resources per client with complex dependency chains |
| Template rendering without caching | Same template parsed and compiled on every check-in | Cache compiled `template.Template` objects keyed by template content hash. Invalidate when source file changes. | >20 templates per client config |
| Compliance status computed by scanning all runs | Compliance API latency grows with fleet size | Maintain a `client_compliance` materialized view or summary table updated on each run completion. Don't re-scan history on every API call. | >200 clients |

## Security Mistakes

Domain-specific security issues beyond general web security.

| Mistake | Risk | Prevention |
|---------|------|------------|
| Agent-reported facts used directly in template rendering without validation | Template injection: a compromised agent sends a hostname containing `{{ .Env.HOME }}` and the server renders it as a template directive, potentially leaking server-side data into client configs | Validate fact values: reject or escape values containing `{{`. Treat all agent-reported data as untrusted. Parse templates before injecting data (don't concatenate). |
| One-time registration tokens with insufficient entropy | Brute-force token guessing: if tokens are short or predictable, an attacker can enroll a rogue agent | Use `crypto/rand` to generate tokens with at least 256 bits of entropy (e.g., 32 bytes hex-encoded). Rate-limit registration attempts. Log and alert on failed registration attempts. |
| Replay attacks on signed gRPC payloads | An attacker captures a signed CheckIn request and replays it to get the client's config (which may contain resolved secrets) | Include a UTC timestamp in every signed payload. Server rejects requests with timestamps older than 5 minutes (already spec'd). Also include a nonce and track recent nonces to prevent replay within the 5-minute window. |
| Bootstrap script served over unauthenticated endpoint | Man-in-the-middle modifies the bootstrap script to install a backdoored agent binary | The `/bootstrap` endpoint should serve over HTTPS with a valid cert. The script should verify the agent binary checksum against a value embedded in the script itself (signed by the server's key). Consider TOFU (Trust On First Use) model with cert pinning. |
| Webhook HMAC secrets stored in the webhooks DB table | Database breach exposes all webhook signing secrets, allowing an attacker to forge webhook payloads | Store webhook HMAC secrets in isopass, not in the database. The `webhooks` table stores the isopass path, not the secret value. Resolve at dispatch time. |
| Agent private key readable by non-root users | Any process on the managed node can impersonate the agent | Set `/etc/isogov/agent.key` to mode `0600` owned by root. The agent runs as root (required for package/user management anyway). Verify permissions on startup. |

## UX Pitfalls

Common user experience mistakes in this domain.

| Pitfall | User Impact | Better Approach |
|---------|-------------|-----------------|
| Opaque error messages from module executors ("exit status 1") | Sysadmin can't tell why a package install failed without SSHing into the node and checking logs | Capture both stdout and stderr from executed commands. Include the full error output in the run result. Show it in `gvctl runs show` and the web UI. |
| No way to see what a client's effective config is after variable merging and env overrides | Sysadmin writes config but can't verify what the agent actually receives after all the merging and template rendering | Add `gvctl preview <hostname>` that shows the fully-rendered config (with secrets redacted) as the agent would receive it. |
| Compliance status "drifted" without explaining what drifted | Dashboard shows 3 nodes drifted but doesn't say whether it's a file, package, or service. Sysadmin must dig through run history. | Compliance summary should include the specific resources that are drifted, not just the status. Show "web01: /etc/nginx/nginx.conf drifted" not just "web01: drifted." |
| YAML syntax errors in client configs discovered only when the agent checks in | Minutes to hours of delay between config edit and error discovery, depending on check-in interval | Validate YAML on file watcher reload. Log validation errors immediately. Expose invalid configs via API/CLI. Never serve an invalid config to an agent -- serve the last-known-good config and alert. |
| No diff in dry-run output | Dry-run says "would update /etc/nginx/nginx.conf" but doesn't show what would change | Include a unified diff in the plan results for file resources. Show current content vs. desired content. Redact secrets in the diff output. |
| Auto-classification rule matches are invisible | Sysadmin can't tell which classification rule matched a node, or why a node isn't getting the expected modules | `gvctl clients show` should display whether the client is explicitly configured or auto-classified, and which classification rule matched (or "no match" if unconfigured). |

## "Looks Done But Isn't" Checklist

Things that appear complete but are missing critical pieces.

- [ ] **File module:** Often missing atomic write (write-to-temp + rename). Verify the file executor uses `os.Rename` on same-filesystem temp file, not `os.WriteFile` to the target path directly.
- [ ] **Package module:** Often missing non-interactive flags. Verify every package manager backend passes `-y`, `DEBIAN_FRONTEND=noninteractive`, and equivalent flags. Test with a package that prompts for configuration.
- [ ] **Service module:** Often missing `daemon-reload` after a package installs a new unit file in the same run. Verify that if a package resource installs a systemd unit and a service resource manages it, `systemctl daemon-reload` runs between them.
- [ ] **DAG ordering:** Often missing cycle detection for combined dependency + notification graphs. Verify that `depends_on` + `notify` edges are checked together, not separately.
- [ ] **Secret resolution:** Often missing the case where isopass returns an empty string (secret exists but has empty value). Verify the system distinguishes "secret not found" from "secret is empty string" and handles both.
- [ ] **Registration:** Often missing rate limiting on the Register RPC. Verify that brute-force token guessing is rate-limited (e.g., 5 attempts per IP per minute).
- [ ] **Config reload:** Often missing validation before applying reloaded config. Verify that a syntax error in one client YAML file doesn't blow away the in-memory config for all clients.
- [ ] **Rollback:** Often missing the case where the snapshot directory doesn't exist or is full (disk space). Verify the agent handles missing/corrupt snapshots gracefully and reports the issue.
- [ ] **Webhook dispatch:** Often missing timeout and retry circuit-breaking. Verify that a slow/dead webhook endpoint doesn't block run reporting or cause goroutine leaks.
- [ ] **Agent backoff:** Often missing backoff cap (maximum interval). Verify that exponential backoff has a maximum (e.g., 30 minutes) so agents don't drift into hour-long intervals after extended server outages.

## Recovery Strategies

When pitfalls occur despite prevention, how to recover.

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Secrets leaked in logs | HIGH | Rotate all potentially-leaked secrets immediately via isopass. Purge log files and database rows containing secrets. Audit log aggregator/backup systems. Add `SecretString` type to prevent recurrence. |
| Partial convergence left system in bad state | MEDIUM | Use `gvctl rollback` for file resources. For packages/users, manually revert on the node or write a "rollback module" that declares the previous desired state. Run with `--dry-run` first to verify. |
| Thundering herd overwhelmed server | LOW | Server recovers automatically once the burst passes. Add admission control (semaphore) to prevent recurrence. Stagger force-push timestamps. Review agent startup jitter. |
| Agent self-update bricked nodes | HIGH | SSH into affected nodes. Replace binary with `isogov-agent.previous`. Restart systemd service. Pin agent version on server to prevent re-update. Fix the bug in the new version before re-deploying. |
| CA cert rotation broke all agents | HIGH | Distribute new CA cert via out-of-band mechanism (Ansible, SSH, manual copy). Update agent configs. Plan future CA rotations with bundle approach (old + new CA simultaneously). |
| YAML config error served to agents | MEDIUM | Fix the YAML file. Agents will pick up the corrected config on next check-in. For critical errors, use `gvctl push` to force immediate re-convergence. Add config validation on reload to prevent recurrence. |
| Notification loop caused repeated service restarts | MEDIUM | Fix the notify cycle in the YAML config. Service may need manual restart if it's in a failed state from too many restarts. Add cascade depth limits to prevent recurrence. |
| Database migration failed and left dirty state | MEDIUM | Fix the migration SQL. Use `golang-migrate force <version>` to clear the dirty flag. Apply the fixed migration. Never edit applied migrations -- create a new one. |

## Pitfall-to-Phase Mapping

How roadmap phases should address these pitfalls.

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Non-atomic file writes | Module executors | Code review: file executor uses write-temp-rename pattern. Integration test: kill agent mid-write, verify file integrity. |
| Thundering herd | Server skeleton + Agent run loop | Load test: simulate 500 agents checking in simultaneously. Verify server stays responsive. Measure p99 latency. |
| Secret leakage | Secrets provider (earliest phase) | Grep codebase for `fmt.Sprintf` or `log.` calls that reference resolved secret variables. Verify `SecretString` type exists and is used everywhere. |
| Notify/subscribe loops | Module dispatch / DAG ordering | Unit test: config with circular notify chain rejected at load time. Integration test: notify triggers exactly one cascade level. |
| Partial convergence | Module executors + Run loop | Integration test: simulate package repo failure mid-run. Verify run report accurately reflects which resources succeeded, failed, and were skipped. |
| File watcher misses | Server config loading | Test: edit config with vim (atomic save). Verify server picks up the change within 2 seconds. Test: NFS-mounted config dir with fsnotify disabled, verify polling fallback works. |
| Template injection | CheckIn / Module dispatch | Unit test: fact value containing `{{ }}` rendered as literal text, not executed. Verify template function map has no dangerous functions. |
| mTLS cert rotation breakage | TLS setup + Secrets integration | Integration test: rotate server cert while agents are connected. Verify zero dropped connections. Document CA rotation procedure. |
| Package manager lock contention | Module executors (package) | Integration test: hold dpkg lock, run package module, verify retry and eventual success after lock release. |
| Agent self-update bricking | Agent self-update (late phase) | Integration test: deploy a deliberately-crashing binary. Verify rollback to previous version occurs automatically. |

## Sources

- [Puppet documentation on relationships and ordering](https://www.puppet.com/docs/puppet/7/lang_relationships.html)
- [Detecting Missing Dependencies and Notifiers in Puppet Programs (academic paper)](https://arxiv.org/pdf/1905.11070)
- [Red Hat: How to Avoid Puppet Dependency Nightmares](https://www.redhat.com/en/blog/how-to-avoid-puppet-dependency-nightmares-with-defines)
- [fsnotify documentation and known issues](https://pkg.go.dev/github.com/fsnotify/fsnotify)
- [Hitless TLS Certificate Rotation in Go](https://blog.diogomonica.com/2017/01/11/hitless-tls-certificate-rotation-in-go/)
- [PayPal Thundering Herd Problem resolution](https://medium.com/paypal-tech/thundering-herd-jitter-63a57b38919d)
- [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html)
- [golang-migrate PostgreSQL tutorial and known issues](https://github.com/golang-migrate/migrate/blob/master/database/postgres/TUTORIAL.md)
- [Go SSTI / template injection risks](https://www.oligo.security/blog/safe-by-default-or-vulnerable-by-design-golang-server-side-template-injection)
- [DevOps.com: 6 Big Bad Mistakes in Configuration Management](https://devops.com/6-big-bad-mistakes-configuration-management-part-1/)
- [CMCrossroads: Lessons Learned in Configuration Management](https://www.cmcrossroads.com/article/lessons-learned-configuration-management-consulting)
- [Puppet: Configuration Drift causes and solutions](https://www.puppet.com/blog/configuration-drift)
- [gRPC Security OWASP Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/gRPC_Security_Cheat_Sheet.html)

---
*Pitfalls research for: Pull-based configuration management system (Isogov)*
*Researched: 2026-03-05*
