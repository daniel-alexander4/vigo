# Code Review Report — 2026-04-01

Full codebase review across all server packages (34 packages), agent, connwaer, CLI, and validators.

## Part 1: Server (server/)

### Critical

- **security — server/ai/tools_dispatch.go:490-532** Path traversal in `getModuleDefinition()` — untrusted `name` parameter used in `filepath.Join()` without validation. Can read arbitrary files via `../../../etc/passwd`.
- **security — server/scrier/websocket.go:466** SSH connections use `ssh.InsecureIgnoreHostKey()` — no host key verification. MITM possible on Scrier SSH sessions.
- **security — server/bootstrap/bootstrap.go:215** Bootstrap script uses `curl -sSfk` — the `-k` flag disables TLS verification during agent download. MITM during bootstrap.
- **performance — server/auth/apitoken.go:91** `ValidateAndResolve` fetches ALL API tokens and does O(n) bcrypt comparisons per request. Will degrade with token count.
- **error-handling — server/grpc/taskdispatch.go:485** Callbacks use `context.Background()` for DB operations. Loses cancellation signals, can hang indefinitely.

### Warning

- **security — server/webhook/dispatcher.go:232-246** SSRF check skips loopback validation for DNS names. DNS rebinding can resolve to internal IPs at delivery time.
- **security — server/secrets/vault.go:28, isopass.go:55** `InsecureSkipVerify=true` available in production secret providers. Should require explicit configuration or refuse.
- **security — server/auth/session.go:39** Session cookie `Secure` flag always false. Sessions transmittable over HTTP if TLS misconfigured.
- **error-handling — server/fleet/index.go:943,950** `json.Marshal` errors silently ignored in `FlattenJSON()`. Corrupts trait index on marshal failure.
- **error-handling — server/publish/rollback.go:42-45** `gzip.Writer` and `tar.Writer` Close() errors silently discarded. Can produce corrupt archives.
- **concurrency — server/scrier/websocket.go:411-424** 60s cleanup timeout races with relay goroutines. Session can be cleaned up while relay is active.
- **concurrency — server/grpc/tunnel.go:76** Race between `ClearTunnelRequested()` and concurrent tunnel lookups can orphan tunnels.
- **logic — server/publish/publish.go:165** `opts.Diff || true` — dead condition, always true. Comment says intentional but misleading.
- **error-handling — server/auth/basic.go:310** Recovery code update errors silently ignored. Old recovery codes remain usable on failure.
- **concurrency — server/peer/service.go:245** `Demote()` ignores context parameter. Race between lock acquisition and role state check.
- **error-handling — server/grpc/taskdispatch.go:405** Rolling execution uses `context.Background()` for background goroutine. Task run management can hang indefinitely.
- **error-handling — server/grpc/agentstream.go:227** AgentStream waits only for first goroutine error. Second goroutine continues unsupervised, potential leak.
- **timing — server/grpc/tunnelmgr.go:349** Tunnel handshake timeout is 60s but comment claims 10s. Resources held too long on hang.
- **error-handling — server/peer/service.go:280-296** `demotePeer()` never actually calls demote RPC. Relies on remote self-detecting, unreliable during split-brain.
- **security — server/sandgorgon/redfish.go:20-45** Redfish BMC credentials stored in plain struct. Exposed in process dump.
- **security — server/scrier/websocket.go:664** No CSRF token validation on WebSocket upgrade. Browser-based attacker can initiate sessions.
- **security — server/ai/assistant.go:409-412** `stripSecrets()` only redacts `secret:` prefix. Secrets in JSON values or nested structures leak to LLM context.
- **concurrency — server/webhook/dispatcher.go:112-119** Unbounded goroutine creation before semaphore acquisition. Event flood creates thousands of goroutines.
- **error-handling — server/watcher/watcher.go:224-231** DB errors in `checkRotations()` logged but not returned. Caller can't distinguish no-rotations from DB failure.

### Info

- **naming — server/grpc/register.go:124** Uses "node_id" instead of canonical "envoy_id" in logs.
- **performance — server/fleet/index.go:272-306** `PaginatedEnvoys()` full-scans entire fleet per API call. O(n) at 100k+ envoys.
- **naming — server/convergence/convergence.go:49-67** `computeInlineStatus()` returns "unknown" but `ComputeConvergence()` maps unrecognized to "no data". Inconsistent status enum.
- **error-handling — server/db/retentionpruner.go:75-78** Individual table pruning errors don't stop vacuum. Should fail fast or aggregate.
- **error-handling — server/api/remediation.go:77** JSON decode error ignored. Creates broken remediation runs on bad input.

## Part 2: Validators (server/validate/, server/modlint/, server/conditionals/, server/config/conditionals.go)

*Reviewed and fixed in this session:*

- ~~**critical — server/conditionals/when.go:414-416** TristateEval VarRefExpr returns false instead of nil (unknown).~~ FIXED
- ~~**critical — cli/configpublish.go:204-217** buildGenericFixPrompt reads arbitrary file paths from error messages.~~ FIXED
- ~~**warning — server/validate/validate.go:698-707** URL hostname validation logic error (AND should be OR).~~ FIXED
- ~~**warning — server/validate/validate.go:195-220** Missing self-reference checks for before/notify/subscribes.~~ FIXED
- ~~**warning — agent/src/guardrails.rs:265-267** Service param name mismatch in guardrails.~~ FIXED
- ~~**warning — agent/src/policy/eval.rs:259** Mutex lock().unwrap() can panic on poisoned mutex.~~ FIXED
- ~~**warning — server/validate/ vs server/modlint/** Duplicate validation logic with inconsistent rules.~~ FIXED (merged)

## Part 3: Agent (agent/)

*Pending review*

## Part 4: Connwaer (connwaer/)

*Pending review*

## Part 5: CLI (cli/)

*Pending review*

## Part 6: UI (ui/)

*Pending review*

## Part 7: License Manager (license-manager/)

*Pending review*

## Part 8: Website (website/)

*Pending review*

## Logging Issues (fixed in this session)

- ~~**critical — server/grpc/tunnel.go:170-177, 209-216** Per-message INFO logging in Scrier tunnel relay.~~ FIXED
- ~~**critical — server/grpc/tunnelmgr.go:163-169, 204-210** Per-message INFO logging in guacd proxy relay.~~ FIXED
- ~~**warning — server/grpc/agentstream.go:396-403** DEBUG comment left in production, logs on every check-in.~~ FIXED
- ~~**warning — server/grpc/agentstream.go:405-408** Warn level for informational nil traitsMap state.~~ FIXED
- ~~**warning — server/grpc/agentstream.go:420-434** Per-check-in policy path logs at INFO.~~ FIXED
- ~~**info — connwaer/src/scheduler/mod.rs:55, main.rs:231** Inconsistent warn vs info for unknown capability.~~ FIXED
