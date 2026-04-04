# Handoff: Executor Redesign (package, source_package, nonrepo_package, repository, exec)

## Context

Version 0.10.0 shipped a package executor resilience system: blocked resource outcome, per-resource circuit breaker, error classification, disk pre-flight, network retry, and GPG key handling. The next phase is an executor redesign that adds new executors and enhances existing ones. This document tells you what already exists so you can integrate correctly.

## What was just built (v0.10.0)

### Blocked resource outcome

New `Action::Blocked` variant in `agent/src/executors/mod.rs`. Displays as "blocked" in proto. Means "infrastructure prevents execution, module config may be correct." Server maps blocked runs to diverged convergence (not failed). Webhook fires `run.blocked`.

Server flow: `server/grpc/report.go` counts `blockedCount` separately from `failedCount`. Run status: failures > 0 → "failure", blocked > 0 → "blocked", else → "success". `server/convergence/convergence.go` and `server/fleet/index.go` both handle "blocked" → "diverged".

### Error classification (agent/src/executors/pkgmgr/errors.rs)

Classifies stderr from apt/dnf into 10 categories:

```rust
pub enum ErrorClass {
    RepoError,          // 404, no Release file
    GpgError,           // EXPKEYSIG, NO_PUBKEY
    DiskFull,           // no space left on device
    NetworkTransient,   // DNS failure, connection timeout
    DependencyConflict, // unmet deps, held packages
    PackageNotFound,    // unable to locate package
    ArchMismatch,       // wrong architecture
    Permission,         // not root
    CacheCorruption,    // hash sum mismatch
    LockContention,     // dpkg lock held
    Other,
}
```

Key methods:
- `classify_apt_error(stderr) -> ErrorClass`
- `classify_dnf_error(stderr) -> ErrorClass`
- `classify_error(pkg_mgr_cmd, stderr) -> ErrorClass` — dispatcher, falls back to trying both
- `format_error(operation, package, class, raw_stderr) -> String` — clear actionable message
- `ErrorClass::is_infrastructure()` — returns true for RepoError, GpgError, DiskFull (these trigger the circuit breaker)

### Circuit breaker (agent/src/runner/pkg_breaker.rs)

Per-resource breaker that persists across convergence cycles. Owned by the check-in loop, passed as `&mut PackageBreaker` through `converge()`.

- `PackageBreaker::new()` — created once in the daemon loop
- `check(resource_key) -> BreakerCheck::Allow | BreakerCheck::Blocked(msg)`
- `record_success(resource_key)` — resets the breaker
- `record_failure(resource_key, &ErrorClass, msg) -> bool` — returns true if breaker just tripped
- Trips after 3 consecutive infrastructure failures, cooldown is 1 hour
- Non-infrastructure errors (dependency conflict, not found, etc.) reset the counter

### Where it's wired in (agent/src/runner/mod.rs)

The integration happens in the `converge()` function's main loop, NOT inside the executors themselves. Executors remain stateless. The runner does pre-flight and post-processing:

Before executing package/repository resources:
1. Circuit breaker check → if blocked, skip with `Action::Blocked`
2. Disk space check on `/var` → if < 100MB, try `apt-get clean`/`dnf clean packages` → if still low, `Action::Blocked`

After executing package/repository resources:
1. If error: classify it, format a clear message, overwrite `result.error`
2. Update circuit breaker → if tripped, change `result.action` to Blocked
3. If success: `record_success()` to reset breaker

The detection of which resources get this treatment uses `is_package_type()`:
```rust
fn is_package_type(resource_type: &str) -> bool {
    matches!(resource_type, "package" | "repository")
}
```

### Network retry (inside agent/src/executors/pkgmgr/apt.rs)

`apt_get_with_retry()` retries both lock contention (3x, 3s/6s/12s) and network transient errors (3x, 5s/10s/20s). Returns raw stderr on final failure for classification by the runner.

### GPG handling (inside agent/src/executors/repository.rs)

`AptRepoManager::fetch_gpg_key()` — curl with wget fallback, dearmor to `/etc/apt/keyrings/{name}.gpg`. After `apt-get update`, parses stderr for EXPKEYSIG (auto re-fetches) and NO_PUBKEY (reports fingerprint).

## What to do for each new/modified executor

### package (existing, enhanced)

Already has all the resilience infrastructure. New params (`purge`, `update_cache`, `flags`, `state: latest`, `timeout`, `retries`/`retry_delay`, `lock_version`) are purely additive to `PackageExecutorWrapper` in `agent/src/runner/mod.rs` (lines ~930-1070) and the `PackageManager` trait + backends.

- `lock_version`: after install, run `apt-mark hold` / `dnf versionlock add`. On remove, run `apt-mark unhold` / `dnf versionlock delete`.
- `state: latest`: use `apt-get install --only-upgrade` or `dnf upgrade` instead of regular install.
- `purge`: use `apt-get purge` instead of `apt-get remove`.
- `update_cache`: run `apt-get update` before install. Be aware this can fail with the same errors the classification system handles — let it flow through.
- `flags`: append to the apt-get/dnf command args.
- `timeout`/`retries`/`retry_delay`: the executor already has a 300s timeout from the runner. retries would be a higher-level retry around the whole install, distinct from the network retry inside `apt_get_with_retry`.

### source_package (rename of archive)

This is NOT a package manager executor. It downloads and extracts archives. It should NOT be added to `is_package_type()` — it doesn't interact with apt/dnf/repos.

What it SHOULD get:
- Network retry for URL downloads — same pattern as `apt_get_with_retry` but for curl/wget. Consider extracting a shared `download_with_retry()` helper.
- Disk pre-flight before extraction — check target filesystem, not `/var`.
- Clear error messages — "source_package download failed: connection timeout for https://..."

What it should NOT get:
- Circuit breaker — a 404 URL is a broken module, not broken infrastructure
- Error classification — the error categories are package-manager-specific

### nonrepo_package (new)

Installs .deb/.rpm/.pkg/.msi from URLs. This IS a package installer (dpkg/rpm) but is NOT repo-based.

What it SHOULD get:
- Add to `is_package_type()` — change to `matches!(resource_type, "package" | "repository" | "nonrepo_package")`. This gives it disk pre-flight and error classification for free.
- Network retry for URL downloads — same as source_package.
- Error classification — dpkg/rpm install errors produce the same stderr as package installs (dependency conflicts, disk full, permission). The existing `classify_apt_error`/`classify_dnf_error` will catch these.
- Disk pre-flight — yes, it installs packages.
- Clear error messages — follow the same pattern.

What it should NOT get:
- Circuit breaker for download failures — a 404 URL is a broken module. But dpkg/rpm install failures (disk full, dependency conflict) should flow through the existing classification naturally.

### repository (existing, fixes + enhancements)

Already covered by `is_package_type()`. GPG handling already improved.

New work:
- Content drift detection — compare file content, not just existence. Currently `is_present()` only checks if the file exists.
- DNF .repo file generation — build proper INI format from structured params instead of raw `repo:` string.
- `enabled` param — set `enabled=0` in the repo file instead of removing it.
- `key_fingerprint` — verify downloaded GPG key fingerprint matches expected value.
- New backends (APK, Pacman, Zypper) — implement `RepoManager` trait for each. These don't have GPG key ceremony like APT.
- DEB822 `.sources` format — write to `/etc/apt/sources.list.d/{name}.sources` instead of `.list` on modern Ubuntu (24.04+).

All of these work within the existing resilience framework — errors from `add()` flow to the runner's post-processing and get classified/breaker'd automatically.

### exec (existing, enhanced)

Does NOT need any of the package resilience infrastructure. exec is a generic command runner.

New params:
- `timeout` — already handled by runner (300s default for exec). This would be a user-configurable override via the `timeout_secs` param that already exists in the runner's execution wrapper.
- `retries`/`retry_delay` — implement inside `execute_exec()` in `agent/src/executors/exec.rs`. Loop with sleep between attempts.
- `sensitive` — suppress command string (not just output) from `result.error` and logs. The `logoutput: "false"` flag already suppresses output.
- `shell` — override `sh -c` with e.g. `bash -c`. Change the command construction in `execute_exec()`.

## Key files

| File | What's there |
|------|-------------|
| `agent/src/executors/mod.rs` | Action enum (incl. Blocked), ResourceResult struct, Executor trait |
| `agent/src/executors/pkgmgr/mod.rs` | PackageManager trait, detect(), GenericPkgMgr |
| `agent/src/executors/pkgmgr/apt.rs` | AptManager with network retry, lock retry |
| `agent/src/executors/pkgmgr/errors.rs` | ErrorClass enum, classify_*, format_error |
| `agent/src/executors/repository.rs` | RepoManager trait, AptRepoManager (GPG), DnfRepoManager, RepositoryExecutor |
| `agent/src/executors/exec.rs` | ExecExecutor with guards, background, stdin, secret_file |
| `agent/src/executors/archive.rs` | Current archive executor (to be renamed source_package) |
| `agent/src/runner/mod.rs` | converge(), PackageExecutorWrapper, is_package_type(), disk checks |
| `agent/src/runner/pkg_breaker.rs` | PackageBreaker circuit breaker |
| `agent/src/runner/cache.rs` | ResultCache (package and repository are cacheable types) |
| `agent/src/checkin/loop_impl.rs` | Daemon loop, owns ResultCache + PackageBreaker |
| `server/grpc/report.go` | ReportResult handler, blocked counting |
| `server/convergence/convergence.go` | ComputeConvergence, "blocked" → "diverged" |

## Testing

- `cargo test pkgmgr` — 25 tests (classification, APT lock/network detection)
- `cargo test pkg_breaker` — 5 tests (threshold, reset, independence)
- `cargo test runner::` — 50 tests (convergence loop, DAG, triggers, circuit breaker)
- `cargo test repository` — 6 tests (mock repo manager, CRUD)
- `go test ./server/convergence/...` — 12 tests (includes "blocked maps to diverged")

## Version

Current: 0.10.0. Bump minor for bug fixes to these executors, major if adding new executor types (nonrepo_package would be a major bump).
