---
phase: 02-convergence
plan: 02
subsystem: agent
tags: [package-manager, systemctl, idempotent, exec, user, service, apt, dnf, yum, apk, pacman, brew, zypper]

# Dependency graph
requires:
  - phase: 02-convergence
    provides: "Executor interface, ActionTaken, ResourceResult, CommandRunner from plan 01"
provides:
  - "PackageManager interface with 7 OS-specific backends"
  - "Detect() auto-detection of package manager from OS family/distro"
  - "PackageExecutor for idempotent package install/remove/upgrade"
  - "ServiceExecutor for systemctl start/stop/restart/reload/enable/disable"
  - "UserExecutor for /etc/passwd parsing and useradd/usermod/userdel"
  - "ExecExecutor for command execution with creates/onlyif/unless guards"
affects: [02-convergence, 03-operations]

# Tech tracking
tech-stack:
  added: []
  patterns: [CommandRunner interface for testable exec.Command, /etc/passwd parsing for user state, mock-based testing without root]

key-files:
  created:
    - agent/modules/pkgmgr/interface.go
    - agent/modules/pkgmgr/detect.go
    - agent/modules/pkgmgr/apt.go
    - agent/modules/pkgmgr/dnf.go
    - agent/modules/pkgmgr/yum.go
    - agent/modules/pkgmgr/apk.go
    - agent/modules/pkgmgr/pacman.go
    - agent/modules/pkgmgr/brew.go
    - agent/modules/pkgmgr/zypper.go
    - agent/modules/package.go
    - agent/modules/service.go
    - agent/modules/user.go
    - agent/modules/exec.go
    - agent/modules/pkgmgr/detect_test.go
    - agent/modules/package_test.go
    - agent/modules/service_test.go
    - agent/modules/user_test.go
    - agent/modules/exec_test.go
    - agent/modules/mocks_test.go
  modified: []

key-decisions:
  - "Shared mockCmdRunnerGeneric in mocks_test.go to avoid duplicating test mocks across executor tests"
  - "Guard evaluation order for ExecExecutor: creates first, then onlyif, then unless"
  - "Exec guards (onlyif/unless) skipped during dry-run to avoid side effects; creates guard still evaluated since it is a file stat"

patterns-established:
  - "CommandRunner pattern: all executors accept injectable CommandRunner for test isolation"
  - "Executor idempotency pattern: check current state, determine action, return early if no change needed"
  - "Passwd parsing: direct /etc/passwd parsing via bufio.Scanner with overridable path for tests"

requirements-completed: [CONV-03, CONV-04, CONV-05, CONV-06, CONV-11, INFRA-06]

# Metrics
duration: 9min
completed: 2026-03-06
---

# Phase 2 Plan 2: Resource Executors Summary

**PackageManager interface with 7 OS backends (apt/dnf/yum/apk/pacman/brew/zypper), auto-detection, and 4 idempotent resource executors (package, service, user, exec)**

## Performance

- **Duration:** 9 min
- **Started:** 2026-03-06T02:05:21Z
- **Completed:** 2026-03-06T02:14:56Z
- **Tasks:** 2
- **Files modified:** 19

## Accomplishments
- PackageManager interface with 7 backends covering Debian, RHEL, CentOS, Alpine, Arch, macOS, and SUSE families
- Auto-detection mapping OS family+distro to correct package manager backend
- Package executor with install/remove/upgrade, version pinning, and dry-run
- Service executor with systemctl is-active/is-enabled checks and 4 states (running, stopped, restarted, reloaded)
- User executor with /etc/passwd parsing for existence and attribute comparison
- Exec executor with creates/onlyif/unless idempotency guards in correct precedence order
- All 90 tests pass with -race flag using mocked dependencies (no root required)

## Task Commits

Each task was committed atomically:

1. **Task 1: PackageManager interface, 7 backends, and auto-detection** - `e2091f7` (feat)
2. **Task 2: Package, service, user, and exec resource executors** - `7caa241` (feat)

_Both tasks followed TDD: RED (failing tests) -> GREEN (implementation) -> commit_

## Files Created/Modified
- `agent/modules/pkgmgr/interface.go` - PackageManager and CommandRunner interfaces
- `agent/modules/pkgmgr/detect.go` - Detect() maps OS family+distro to backend
- `agent/modules/pkgmgr/apt.go` - Apt backend (dpkg -s, apt-get install/remove)
- `agent/modules/pkgmgr/dnf.go` - Dnf backend (rpm -q, dnf install/remove)
- `agent/modules/pkgmgr/yum.go` - Yum backend (rpm -q, yum install/remove)
- `agent/modules/pkgmgr/apk.go` - Apk backend (apk info -e, apk add/del)
- `agent/modules/pkgmgr/pacman.go` - Pacman backend (pacman -Q, pacman -S/-R)
- `agent/modules/pkgmgr/brew.go` - Brew backend (brew list, brew install/uninstall)
- `agent/modules/pkgmgr/zypper.go` - Zypper backend (zypper se -i, zypper install/remove)
- `agent/modules/package.go` - PackageExecutor with install/remove/upgrade
- `agent/modules/service.go` - ServiceExecutor with systemctl state management
- `agent/modules/user.go` - UserExecutor with passwd parsing and useradd/usermod/userdel
- `agent/modules/exec.go` - ExecExecutor with creates/onlyif/unless guards
- `agent/modules/mocks_test.go` - Shared test mocks (mockCmdResult, mockCmdRunnerGeneric)
- `agent/modules/pkgmgr/detect_test.go` - 36 tests for detection and backend operations
- `agent/modules/package_test.go` - 10 tests for PackageExecutor
- `agent/modules/service_test.go` - 10 tests for ServiceExecutor
- `agent/modules/user_test.go` - 10 tests for UserExecutor
- `agent/modules/exec_test.go` - 13 tests for ExecExecutor

## Decisions Made
- Shared mockCmdRunnerGeneric in mocks_test.go avoids duplicating mock infrastructure across 3 executor test files
- ExecExecutor guard order: creates (file stat) evaluated first, then onlyif, then unless -- creates has highest precedence
- Exec guards (onlyif/unless) are not evaluated during dry-run to avoid side effects; only creates guard (a file stat) runs during dry-run
- Used partial-match pattern in mock runner for flexible test setup

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered
- Plan 01 (parallel wave) had already completed when this plan started, so executor.go with CommandRunner/DefaultRunner already existed -- no conflict, used existing types directly

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness
- All 5 resource executor types (file, package, service, user, exec) now implement the Executor interface
- Ready for convergence engine integration in Plan 04 (DAG runner)
- Package manager auto-detection will integrate with fact collectors from Plan 03

## Self-Check: PASSED

All 19 created files verified present. Both task commits (e2091f7, 7caa241) verified in git log.

---
*Phase: 02-convergence*
*Completed: 2026-03-06*
