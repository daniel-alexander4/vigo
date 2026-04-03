# Code Review Report — 2026-04-01

Full codebase review across all server packages (34 packages), agent, connwaer, CLI, and validators.

## Part 1: Server (server/)

### Critical

- ~~**security — server/ai/tools_dispatch.go:490-532** Path traversal in `getModuleDefinition()`.~~ FIXED — rejects `..`, `/`, `\` in module names.
- **security — server/scrier/websocket.go:466** SSH connections use `ssh.InsecureIgnoreHostKey()` — no host key verification. MITM possible on Scrier SSH sessions.
- **security — server/bootstrap/bootstrap.go:215** Bootstrap script uses `curl -sSfk` — the `-k` flag disables TLS verification during agent download. MITM during bootstrap.
- **performance — server/auth/apitoken.go:91** `ValidateAndResolve` fetches ALL API tokens and does O(n) bcrypt comparisons per request. Will degrade with token count.
- **error-handling — server/grpc/taskdispatch.go:485** Callbacks use `context.Background()` for DB operations. Loses cancellation signals, can hang indefinitely.

### Warning

- **security — server/webhook/dispatcher.go:232-246** SSRF check skips loopback validation for DNS names. DNS rebinding can resolve to internal IPs at delivery time.
- **security — server/secrets/vault.go:28, isopass.go:55** `InsecureSkipVerify=true` available in production secret providers. Should require explicit configuration or refuse.
- ~~**security — server/auth/session.go:39** Session cookie `Secure` flag always false.~~ FIXED — set to true (server always runs behind TLS).
- ~~**error-handling — server/fleet/index.go:943,950** `json.Marshal` errors silently ignored in `FlattenJSON()`.~~ FIXED — falls back to fmt.Sprintf on error.
- **error-handling — server/publish/rollback.go:42-45** `gzip.Writer` and `tar.Writer` Close() errors silently discarded. Can produce corrupt archives.
- **concurrency — server/scrier/websocket.go:411-424** 60s cleanup timeout races with relay goroutines. Session can be cleaned up while relay is active.
- **concurrency — server/grpc/tunnel.go:76** Race between `ClearTunnelRequested()` and concurrent tunnel lookups can orphan tunnels.
- ~~**logic — server/publish/publish.go:165** `opts.Diff || true` — dead condition.~~ FIXED — replaced with unconditional block.
- ~~**error-handling — server/auth/basic.go:310** Recovery code update errors silently ignored.~~ FIXED — logs error on failure.
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

- ~~**naming — server/grpc/register.go:124** Uses "node_id" instead of canonical "envoy_id".~~ FIXED.
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

### Critical

- **panic — agent/src/checkin/loop_impl.rs:276** `.clone().unwrap()` on `accepted_bundle` — despite `is_some()` guard, pattern match would be safer.
- ~~**panic — agent/src/traits/cached.rs:53,71,144** `.lock().unwrap()` on mutex.~~ FIXED — uses `.unwrap_or_else(|e| e.into_inner())`.
- **data-loss — agent/src/checkin/loop_impl.rs:1042** `next_seq().unwrap_or(1)` — LMDB error silently resets sequence to 1, offline deltas lost or overwritten.
- ~~**panic — agent/src/executors/exec.rs:395,402** `.unwrap()` on `params.get("stdin")` and `params.get("secret_file")`.~~ FIXED — uses `.ok_or_else()` with error context.
- ~~**panic — agent/src/executors/user.rs:673** `.unwrap()` on `params.get("groups")`.~~ FIXED — uses match with error return.
- ~~**panic — agent/src/auth/signer.rs:30** `sig_bytes.try_into().unwrap()`.~~ FIXED — uses match with false return.
- **security — agent/src/bootstrap/client.rs:42** TLS verification disabled during bootstrap. MITM window before certs available.
- **process-leak — agent/src/tunnel/relay.rs:205,239** `tokio::select!` on two spawned tasks — first completion cancels second but may leave partial state.

### Warning

- ~~**error-handling — agent/src/checkin/loop_impl.rs:1046** `let _ = wtxn.commit()` — offline delta commit silently ignored.~~ FIXED — logs warning on failure.
- **error-handling — agent/src/executors/file.rs:349** `.unwrap_or_else(|| Path::new("."))` — writes temp files to cwd if path has no parent instead of erroring.
- **error-handling — agent/src/executors/file.rs:398** Temp file cleanup error silently discarded — orphaned temp files.
- **concurrency — agent/src/tunnel/vnc.rs:243** `wait_for_ready()` doesn't check for x11vnc error output — can hang 10s on startup failure.
- **signal — agent/src/main.rs:805-809** Signal handler spawned but never joined. Silent failure if task panics.
- **naming — agent/src/executors/cron.rs:46** Supports both `"job"` and `"command"` params but error only mentions `"job"`.
- **error-handling — agent/src/tunnel/relay.rs:222-223** `let _ =` on send/write operations — silent failures in tunnel relay.

### Info

- **fire-and-forget — agent/src/checkin/loop_impl.rs:473** Tunnel relay task spawned but never tracked. No graceful shutdown support.
- **panic — agent/src/checkin/stream.rs:262** `.unwrap()` on channel recv in spawned task — panics without context if channel closes.
- **platform — agent/src/runner/mod.rs:2246** `disk_free_percent()` returns None on Windows — disk space safety check incomplete.
- **unsafe — agent/src/state/mod.rs:51** LMDB `unsafe` block missing `// SAFETY:` comment.
- **logging — agent/src/executors/file.rs:313** Backup failure warning missing destination path and file size.

## Part 4: Connwaer (connwaer/)

### Critical

- **mutex — connwaer/src/trends/mod.rs:35,49,150** Mutex poisoning converts to error but no recovery or fatal log. Subsequent operations fail silently.
- **validation — connwaer/src/capability/kill_switch.rs:173** Socket address parse failure silently defaults to 0.0.0.0:0. Masks config errors.

### Warning

- **security — connwaer/src/capability/backup_restore.rs:220-241** `restore_command` and `verify_command` from config passed to `sh -c` without validation. Document shell injection risk.
- **platform — connwaer/src/platform/resource.rs:55-62** Raw `SYS_ioprio_set` syscall with minimal error checking. Silent failure on unsupported platforms.
- **license — connwaer/src/main.rs:159-178** Invalid/missing license allows full operation in "evaluation mode". Fine for now but needs enforcement gate.
- **security — connwaer/src/license/mod.rs:15** Ed25519 public key hardcoded in source. Expected but needs documentation re: key rotation.

### Info

- **platform — connwaer/src/capability/capacity_monitor.rs:189-194** Windows disk usage returns 0.0 — not implemented. Should return Skipped.
- **logic — connwaer/src/trends/mod.rs:131** No NaN check on threshold/slope division. Produces "NaN" string in exhaustion date.
- **logging — connwaer/src/alerting/webhook.rs:40-46** Retry loop doesn't distinguish transient vs permanent errors.
- **validation — connwaer/src/capability/password_policy.rs:195** Parse failure and "no output" both map to score -1. Should distinguish.

## Part 5: CLI (cli/)

### Critical

- **dead-code — cli/cve.go** `cveCmd` defined but not registered in main.go. Command unreachable.
- **dead-code — cli/env.go** `envCmd` and `envPromoteCmd` defined but not registered. Unreachable.

### Warning

- **error-handling — cli/backup.go:189-191** Hash verification failure logged as warning but restore proceeds. Invalid archives silently restored.
- **error-handling — cli/license.go:118-120** Process signal failure for license reload doesn't return error code. Command appears successful when reload failed.
- **hardcoded — cli/edit.go:18** Config root `/srv/vigo/stack` hardcoded. No env var or flag override.

### Info

- **security — cli/tokens.go:30** Bootstrap token displayed in full shell command string. Could leak to shell history.

## Part 6: UI (ui/)

### Warning

- **orphaned-template — ui/templates/pages/sandgorgon.html** Template never rendered by any handler.
- **orphaned-template — ui/templates/pages/sandgorgon-asset.html** Never rendered.
- **orphaned-template — ui/templates/pages/sandgorgon-certificates.html** Never rendered.
- **orphaned-js — ui/static/js/guacamole.min.js** Not referenced in any template (used by Scrier inline templates in Go strings).
- **orphaned-js — ui/static/js/isowebauth.js** Not referenced in any template.
- **dom — ui/templates/pages/ask.html:281** Substring matching in DOM selector `onclick*=` is fragile. Should use data attributes.

Note: The guacamole.min.js, xterm.min.js, and xterm-addon-fit.min.js files are referenced from Go string templates in server/scrier/template.go, not from HTML template files. They are NOT dead code — the review tool missed this because it only searched HTML files.

## Part 7: License Manager (license-manager/)

### Critical

- ~~**error-handling — license-manager/license.go:271,328** `db.Exec()` UPDATE errors ignored.~~ FIXED — returns error on failure.

### Warning

- **dead-code — license-manager/keys.go:225-236** `contains()` and `searchString()` duplicate `strings.Contains()`.

## Part 8: Website (website/)

### Critical

- **error-handling — website/stats.go:180,238,250,294,327** Multiple `rows.Scan()` calls without error checks. Corrupted data silently used.
- **error-handling — website/stats.go:233,244,317** Three queries use `rows, _ := db.Query()`. Database errors silently swallowed.
- **security — website/main.go:357,368** `pem.Encode()` errors not checked. Invalid PEM data could be written to cert files.

### Warning

- **error-handling — website/main.go:195** `time.Parse()` error ignored. Malformed cutoff becomes zero time.
- **error-handling — website/main.go:207-284** Multiple `ExecuteTemplate()` calls without error checks. Incomplete HTML served on template errors.
- **concurrency — website/main.go:530** Stats recording in fire-and-forget goroutine. DB write failures invisible.

## Logging Issues (fixed in this session)

- ~~**critical — server/grpc/tunnel.go:170-177, 209-216** Per-message INFO logging in Scrier tunnel relay.~~ FIXED
- ~~**critical — server/grpc/tunnelmgr.go:163-169, 204-210** Per-message INFO logging in guacd proxy relay.~~ FIXED
- ~~**warning — server/grpc/agentstream.go:396-403** DEBUG comment left in production, logs on every check-in.~~ FIXED
- ~~**warning — server/grpc/agentstream.go:405-408** Warn level for informational nil traitsMap state.~~ FIXED
- ~~**warning — server/grpc/agentstream.go:420-434** Per-check-in policy path logs at INFO.~~ FIXED
- ~~**info — connwaer/src/scheduler/mod.rs:55, main.rs:231** Inconsistent warn vs info for unknown capability.~~ FIXED
