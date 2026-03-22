
# Vigo Differentiators

What Vigo does in the areas of safety, reliability, and simplicity that other state enforcement and configuration management systems do not.

## Safety

- **Per-request ED25519 signatures.** Every agent request is individually signed and verified against stored public keys. Not just TLS — actual cryptographic proof of identity on every API call. Replay and impersonation require the private key, not just a valid certificate.

- **Templates are restricted to `content:` fields only.** You cannot template a package name, a command string, a file path, or a service name. This eliminates an entire class of injection vulnerabilities that other CMs allow by enabling templating everywhere.

- **Secrets are never materialized.** The `secret:` prefix resolves at config load through a pluggable provider and is stripped before transmission. Secrets never appear in YAML files, environment variables, logs, database rows, gRPC payloads, or run result reports. There is no point in the data flow where a secret exists as plaintext in an inspectable location.

- **One-time enrollment tokens with pattern matching.** Tokens are bcrypt-hashed at rest and bound to hostname glob patterns. A token that matches `*.web.prod` cannot enroll a machine named `db01.staging`. Used once, then permanently invalidated.

- **No plaintext mode.** mTLS is the only transport. There is no `--no-ssl` flag, no plaintext fallback, no "development mode" that skips encryption. The insecure path does not exist in the codebase.

- **Self-protection that cannot be overridden.** The validation pipeline blocks any resource that targets the agent binary, the server binary, the config directory, or the service unit — file writes, package removals, service stops, and exec commands containing dangerous patterns. Even `--dangerous` mode cannot bypass these checks. The system cannot be used to break itself.

- **Staged publish pipeline with idempotency enforcement and AI-assisted fix suggestions.** Configs are edited in a `stage/` directory and must pass a full validation gate before going live. The publish command runs the exact same config loader the server uses — YAML parsing, cross-reference resolution, circular dependency detection, template syntax checking, `when:` expression parsing, and secret format validation. On top of that, a static idempotency analyzer blocks resources that would be destructive on every convergence: an `exec` without `creates`, `onlyif`, or `unless` is rejected; a `service` with `state: restarted` without `subscribes` is rejected; a `reboot` without a guard is rejected; a `cron` without an explicit schedule or user is rejected (the defaults are every minute as root). URL aliveness checks verify that archive and repository URLs are reachable before the config goes live. When validation fails, the CLI automatically sends the errors and the failing module YAML to the built-in AI assistant, which streams back an explanation of what's wrong and the corrected YAML — right in the terminal. If validation passes, the sync is atomic (tmp+rename per subdirectory), permissions are locked down (0750/0640), a timestamped snapshot is archived for rollback, and the server reloads via the REST endpoint. If hot-reload fails, the server keeps the previous working config and optionally sends an email alert. A global config freeze can block all publishes during maintenance windows. Every publish is recorded in the audit trail with operator, changed files, and line-level diffs. No other CM validates idempotency, enforces self-protection, offers AI fix suggestions, and gates deployment at publish time — they discover broken resources when they hit production machines.

- **Gradual license enforcement that never locks out existing nodes.** If you exceed your license, you get 15 days of warnings, then 15 days where only new enrollments are blocked, then shutdown. At no point are already-managed machines cut off from convergence.

- **Tamper-evident audit trail.** Every auditable event (enrollment, revocation, config publish, approval, task dispatch, promotion) is recorded in a SHA-256 hash chain. Any modification, deletion, or reordering of entries is cryptographically detectable. `vigogo audit verify` or the web UI validates the entire chain in seconds. Meets PCI-DSS 10.x, HIPAA audit controls, NIST AU, and SOX requirements. No other CM tool provides cryptographic tamper evidence on its audit trail.

- **Two-person change approval.** When enabled, config publish creates a pending change that a different admin must approve. The approver cannot be the same user as the publisher. Pending changes expire after a configurable timeout. Satisfies SOX segregation of duties and FDA change control requirements.

- **Blast radius protection.** After config publish, the server monitors fleet compliance for a configurable window. If compliance drops below the threshold, the previous config is automatically restored and reloaded. Catches bad configs before they spread across the fleet.

- **Source-available agent.** The code that runs as root on your machines is readable and auditable under the Elastic License 2.0. You do not have to trust a proprietary binary with root access on every machine in your fleet.

- **Agent transparency.** `vigo status` shows the enforced policy, collected traits, and pending results on any managed machine. The machine's local admin can verify exactly what the agent is doing without server access.

## Reliability

- **Peer replication for server redundancy.** The primary server syncs config, secrets, and TLS certs to peers via gRPC. Agents fail over automatically after 3 consecutive failures. A promoted peer rebuilds fleet state from agent check-ins within one check-in cycle. No SQLite replication needed — agents are the source of truth. Integrates with hub-spoke spanner for full topology redundancy.

- **Signed policy bundles with offline convergence.** The server compiles policy into a bundle, signs it with ED25519, and sends it to the agent. The agent verifies the signature, caches the bundle in LMDB, and can continue converging indefinitely if the server disappears. Pending results queue in LMDB, drained automatically on reconnect.

- **Adaptive stream promotion.** Agents default to lightweight stateless polling. The server only promotes an agent to a persistent bidirectional stream when it has work to dispatch (ad-hoc tasks, queries, workflows). When work is done, the stream is released. Idle agents consume zero stream resources. No other CM dynamically adjusts its transport based on workload.

- **Evaluation resilience.** A template render error or a `when:` parse error in one resource skips that resource and continues with the rest of the convergence run. A single bad resource does not abort the entire policy application.

- **Zero database operations on the check-in hot path.** The FleetIndex is a fully in-memory index with async dirty-set flushing to SQLite every 10 seconds. An agent check-in — lookup, policy compilation, and response — touches zero database queries.

- **The agent process never exits on transient failures.** Server unreachable, TLS handshake failure, malformed response, policy evaluation crash — all handled with backoff and retry. The agent binary, once started, runs until explicitly stopped or the machine shuts down.

- **Reload with graceful fallback.** Config publish validates, syncs files, and triggers a server reload via REST endpoint. The server atomically swaps the in-memory config store on success. If the new config fails validation or secret resolution, the server keeps the previous working config — no downtime, no partial state. Optional email notification on reload failure.

## Simplicity

- **One server at any scale.** There are no compile masters, no worker pools, no database clusters, no load balancers between components. One process, one SQLite file, one config directory. The architecture does not change whether you manage 10 nodes or 100,000.

- **SQLite is the database.** No PostgreSQL to install, tune, vacuum, back up, replicate, upgrade, or staff a DBA for. One file. WAL mode. Zero administration.

- **~5 MB static agent binary with zero runtime dependencies.** No Ruby, no Python, no JVM, no package manager ecosystem on managed nodes. Copy the binary, run it.

- **One config format, one config path.** There is exactly one way to configure Vigo: edit YAML files in a directory and publish. There is no API that writes config, no database that stores config, no UI that modifies config, no secondary system that overlays config. One source of truth, one format, one tool to manage it.

- **No DSL.** The configuration language is YAML. Go templates appear only inside `content:` attributes. There is no custom language to learn, no compiler, no parser quirks, no language versioning.

- **First-match-wins node assignment.** Nodes match against patterns in file order. The first match wins. There are no inheritance chains, no classification rules, no hierarchy to debug.

- **Bootstrap is one command.** `curl -sSfk https://server:8443/bootstrap | sudo sh` downloads the binary, generates TLS certificates via CSR signing, registers with the server, installs the system service, and verifies connectivity. Under management in seconds.

- **Reload with no restart.** `vigogo config publish` validates, syncs, and triggers the server to reload — all in one command. No process restart, no HUP signal.

- **Seven OS families from one codebase.** Linux, macOS, FreeBSD, OpenBSD, NetBSD, illumos, and Windows. Write `type: service` and it dispatches to systemd, launchctl, rc.d, rcctl, SMF, or sc.exe. The config author doesn't need to know which OS the target runs.
