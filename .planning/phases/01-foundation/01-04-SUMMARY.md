---
phase: 01-foundation
plan: 04
subsystem: auth
tags: [ed25519, crypto, pem, signing, agent, cobra]

# Dependency graph
requires:
  - phase: 01-foundation-01
    provides: Go module with stub agent/main.go and go.mod
provides:
  - ED25519 keypair generation with PKCS8/PKIX PEM storage
  - Request payload signing with timestamp for replay prevention
  - Agent binary with cobra CLI, register subcommand stub
  - LoadOrGenerate pattern for first-run keypair bootstrap
affects: [01-05, 02-convergence]

# Tech tracking
tech-stack:
  added: [cobra]
  patterns: [ed25519-keypair-management, payload-signing-with-timestamp, load-or-generate-pattern]

key-files:
  created:
    - agent/auth/keypair.go
    - agent/auth/signer.go
    - agent/auth/keypair_test.go
    - agent/auth/signer_test.go
  modified:
    - agent/main.go
    - go.mod
    - go.sum

key-decisions:
  - "Payload signing concatenates payload+timestamp bytes (not a structured format) for simplicity"
  - "Private key stored as PKCS8 PEM, public key exported as PKIX PEM for x509 stdlib compatibility"
  - "LoadOrGenerate creates data directory implicitly via agent PersistentPreRunE"

patterns-established:
  - "agent/auth package: shared by agent (signing) and server (verification) via VerifyPayload"
  - "TDD flow: stub functions returning nil -> failing tests -> real implementation -> all pass"

requirements-completed: [SEC-02, AGNT-11]

# Metrics
duration: 4min
completed: 2026-03-06
---

# Phase 1 Plan 4: Agent Auth Summary

**ED25519 keypair generation with PKCS8/PKIX PEM storage, payload+timestamp signing, and cobra CLI agent stub with register subcommand**

## Performance

- **Duration:** 4 min
- **Started:** 2026-03-06T00:59:53Z
- **Completed:** 2026-03-06T01:03:31Z
- **Tasks:** 2
- **Files modified:** 7

## Accomplishments
- ED25519 keypair generation, save (PKCS8 PEM, 0600 perms), load, and public key export (PKIX PEM)
- Request signing with timestamp concatenation for replay attack prevention
- 12 passing tests covering key generation, PEM roundtrip, signing, verification, tamper detection, wrong-key rejection, and timestamp inclusion
- Agent binary with cobra root command (--data-dir flag) and register subcommand stub (--server, --token flags)

## Task Commits

Each task was committed atomically:

1. **Task 1: ED25519 keypair and signing (TDD RED)** - `c9728db` (test)
2. **Task 1: ED25519 keypair and signing (TDD GREEN)** - `aa4c02e` (feat)
3. **Task 2: Agent main.go stub entrypoint** - `06037b8` (feat)

## Files Created/Modified
- `agent/auth/keypair.go` - ED25519 keypair generation, PKCS8 PEM save/load, PKIX PEM export, LoadOrGenerate
- `agent/auth/signer.go` - SignPayload and VerifyPayload with timestamp concatenation
- `agent/auth/keypair_test.go` - 7 tests for keypair generation, PEM format, save/load roundtrip
- `agent/auth/signer_test.go` - 5 tests for signing, verification, tamper detection, wrong key, timestamp
- `agent/main.go` - Cobra root command with --data-dir, register subcommand with --server/--token
- `go.mod` / `go.sum` - Added cobra dependency

## Decisions Made
- Payload signing uses simple byte concatenation (payload + timestamp) rather than a structured format -- this is sufficient for ED25519 since it signs the full message, not a hash, and the server knows the split point
- Private key stored as PKCS8 PEM (standard "PRIVATE KEY" block type), public key as PKIX PEM ("PUBLIC KEY") for full x509 stdlib compatibility
- Agent root command creates data directory with 0700 permissions if it doesn't exist

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 - Blocking] Added cobra dependency**
- **Found during:** Task 2 (agent main.go)
- **Issue:** cobra was listed as added in plan 01-01 summary but was not actually in go.mod
- **Fix:** Ran `go get github.com/spf13/cobra@latest`
- **Files modified:** go.mod, go.sum
- **Verification:** `go build -o bin/isogov-agent ./agent` compiles successfully

---

**Total deviations:** 1 auto-fixed (1 blocking)
**Impact on plan:** Necessary dependency for CLI framework. No scope creep.

## Issues Encountered
- The RED commit (c9728db) included some unrelated files from plan 01-03 (server/secrets/) that were in the working tree but not yet committed. These files belong to plan 01-03 and were correctly implemented; they were just committed in this task's RED commit due to `git add` including them. This does not affect correctness.

## User Setup Required
None - no external service configuration required.

## Next Phase Readiness
- agent/auth package ready for server-side signature verification in Plan 01-05 (gRPC handlers)
- Agent register subcommand ready for gRPC client implementation connecting to server Register RPC
- Keypair foundation ready for Phase 2 agent convergence loop

## Self-Check: PASSED

All files verified present. All commits verified in git log (c9728db, aa4c02e, 06037b8). 12 tests pass. Agent binary compiles and runs.

---
*Phase: 01-foundation*
*Completed: 2026-03-06*
