# Testing

Hobnob ships with two automated test suites that verify server health, CLI functionality, and internal correctness. Both scripts are installed to `/srv/hobnob/scripts/` and can be run by administrators at any time.

## CLI Integration Tests

`test-cli.sh` exercises every `hobnobcli` subcommand against the running server. It verifies that enrollment, config publishing, push dispatch, compliance export, secrets management, and all other operations work end-to-end.

### Running

```bash
# From the server host (requires the server to be running):
sudo /srv/hobnob/scripts/test-cli.sh

# Or specify a different server:
sudo /srv/hobnob/scripts/test-cli.sh --server https://hobnob.example.com:8443
```

Run as root to include tests that require filesystem access (config publish, backup create/restore).

### What It Tests

The script runs 107 test calls across 30 sections:

| Section | Tests | What It Verifies |
|---------|-------|-----------------|
| doctor | 1 | Server health check |
| audit | 2 | Audit trail list and chain verification |
| license | 2 | License status and machine fingerprint |
| db | 6 | Stats, tables, integrity, checkpoint, vacuum, raw query |
| thralls | 10 | List, show, traits, preview, tag/untag, packages, maintenance, push |
| runs | 2 | Run history list and detail |
| approved + tokens | 4 | Enrollment pattern lifecycle (add/revoke) with one-time tokens |
| config | 8 | History, publish (dry-run), trace, tree, pending, search, rollback |
| config freeze | 2 | Freeze and unfreeze config publishes |
| inventory | 2 | Fleet search by trait values |
| push | 3 | Push list, dispatch echo command, status check |
| packages | 1 | Package version drift detection |
| query | 1 | Live trait query to enrolled thrall |
| export | 3 | SIEM, CMDB, and OSCAL compliance export |
| webusers | 3 | Web user create/delete lifecycle |
| auth | 4 | Auth status, API token create/list/revoke |
| webhooks | 3 | Webhook add/delete lifecycle |
| secrets | 5 | Local secrets init, set, reveal, list, delete |
| spanner | 2 | Spanner status and spoke list |
| workflow | 3 | Workflow list, status/abort error handling |
| backup | 3 | Create, inspect, restore (dry-run) |
| test | 1 | Config validation |
| server | 2 | Peer replication status, TLS cert generation |
| validate | 1 | Single-file .hnb validation |
| env | 1 | Environment promote error handling |
| ask | 1 | AI assistant (skipped if AI not enabled) |
| rollback | 1 | Rollback error handling |
| error handling | 7 | Invalid IDs, missing args, bad inputs |

### Expected Output

A healthy server produces output like:

```
hobnobcli test suite
Server: https://localhost:8443

[preflight]
  Server reachable

[doctor]
  PASS  doctor

[audit]
  PASS  audit list
  PASS  audit verify

...

========================================
  Total: 80  Pass: 77  Fail: 0  Skip: 3
========================================
```

**Normal skips** (not failures):

| Test | Reason |
|------|--------|
| config publish --dry-run | Requires root |
| server status | Only relevant with peer replication configured |
| ask | Only runs when AI is enabled in server.yaml |

Any `FAIL` result indicates a real problem. Failed tests show the exit code and first few lines of output to aid debugging.

### Requirements

- `hobnobcli` in PATH (installed automatically by the Docker entrypoint)
- `curl` and `jq` available on the host
- Server running and reachable at the specified URL
- At least one enrolled thrall for full coverage (thrall-dependent tests skip gracefully otherwise)

## Unit Tests

`test-unit.sh` runs the Go server tests and Rust agent tests. These are self-contained — they use in-memory SQLite and do not require a running server.

### Running

```bash
# Run all unit tests (Go + Rust):
/srv/hobnob/scripts/test-unit.sh

# Go tests only:
/srv/hobnob/scripts/test-unit.sh --go

# Rust tests only:
/srv/hobnob/scripts/test-unit.sh --rust

# Include Windows cross-compile check:
/srv/hobnob/scripts/test-unit.sh --windows
```

### What It Tests

**Go server** (35 packages, 900+ test functions):

- API handler correctness (all REST endpoints)
- gRPC signature verification (ED25519, timestamp validation)
- Config loading, parsing, and validation
- FleetIndex operations and concurrency
- Workflow engine execution and branching
- Push dispatch and timeout reaper
- Compliance calculations
- License enforcement stages
- Authentication, RBAC, and API tokens
- Secrets encryption/decryption
- Audit chain integrity
- Module linting and repair

**Rust agent** (960+ test functions):

- All 73 built-in executors (parameter validation, idempotency)
- Runner convergence, dependency ordering, notify/subscribes triggers
- Stream protocol handling
- Bundle verification (ED25519 signatures)
- State store operations (LMDB)
- `when:` expression parsing and evaluation
- Trait collectors
- Platform abstractions

### Expected Output

```
hobnob unit test suite

[go tests]
  PASS  github.com/hobnob/hobnob/server/api 0.296s
  PASS  github.com/hobnob/hobnob/server/grpc 1.978s
  ...

  Go: 35 packages pass, 0 fail (900 test functions)

[rust tests]
  PASS  960 tests passed, 0 ignored

========================================
  Go:     35 packages pass, 0 fail
  Rust:   960 tests pass, 0 fail
========================================
```

### Requirements

- Go 1.25+ toolchain (for Go tests)
- Rust toolchain with cargo (for Rust tests)
- Source code available (tests run against the source tree, not installed binaries)

Unit tests are primarily useful during development or when verifying a build from source. The CLI integration tests are more practical for production servers.

## When to Run Tests

| Scenario | Which Test | Why |
|----------|-----------|-----|
| After initial install | `test-cli.sh` | Verify server is healthy and all features work |
| After upgrade | `test-cli.sh` | Catch regressions before resuming normal operations |
| Investigating issues | `test-cli.sh` | Isolate which subsystem is broken |
| After config changes | `test-cli.sh` | Verify config publish pipeline and thrall resolution |
| Building from source | `test-unit.sh` | Verify compilation and internal correctness |
| Before submitting patches | `test-unit.sh` | Ensure no regressions in core logic |
