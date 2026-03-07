---
phase: 06-custom-traits
verified: 2026-03-07T15:10:00Z
status: passed
score: 10/10 must-haves verified
---

# Phase 6: Custom Traits Verification Report

**Phase Goal:** Admins can drop executable scripts into a server directory and have them automatically distributed to agents, executed with a timeout, and their output merged into the traits map under `custom.*`
**Verified:** 2026-03-07T15:10:00Z
**Status:** passed
**Re-verification:** No -- initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | CheckInResponse contains a manifest of custom trait executables with name and SHA256 checksum | VERIFIED | Proto has `repeated CustomTraitEntry custom_traits = 4` on CheckInResponse (line 220 of isogov.proto). CheckIn handler in checkin.go (lines 91-103) calls scanner.ScanDir() and maps entries to proto CustomTraitEntry. |
| 2 | Only files with the executable bit set in the traits directory appear in the manifest | VERIFIED | scanner.go line 63: `info.Mode()&0111 == 0` filters non-executables. TestScanDir_FiltersExecutables passes (2 exec files included, README.md excluded). |
| 3 | Agent can download a custom trait executable via a dedicated gRPC call and receive the file bytes | VERIFIED | DownloadCustomTrait RPC in proto, handler in server/grpc/traits.go serves bytes via traitsScanner.ReadFile, client method in agent/checkin/client.go (lines 153-162). |
| 4 | Non-executable files in traits/ are silently skipped | VERIFIED | scanner.go line 63 skips, no error logged. Test confirms only executables returned. |
| 5 | Agent compares manifest checksums against local cache and downloads only new or changed executables | VERIFIED | executor.go lines 71-88 compare SHA256 checksums, skip if matching. TestSyncAndRun_ChangedChecksum_DownloadsOnlyChanged confirms: changed=1 download, unchanged=0 downloads. |
| 6 | Custom trait executables are cached locally with correct permissions | VERIFIED | executor.go line 84 writes to cacheDir with 0755 permissions. agent/main.go line 217 passes `filepath.Join(dataDir, "traits")` to Loop. |
| 7 | Custom trait output appears under custom.\<name\> in the traits map | VERIFIED | executor.go lines 116-121 builds `customMap[entry.Name]` under `result["custom"]`. loop.go lines 178-182 merges into factsMap. Tests confirm nested `result["custom"]["app-version"]["version"]` structure. |
| 8 | A failing custom trait logs a warning and is skipped | VERIFIED | executor.go line 113 logs warning via slog.Warn and continues. TestSyncAndRun_NonZeroExit_SkippedWithWarning: bad-trait absent, good-trait present. |
| 9 | A timed-out custom trait (>10s) is killed and skipped with a warning | VERIFIED | executor.go lines 130-139 use context.WithTimeout + process group kill (Setpgid + SIGKILL to -pid). TestSyncAndRun_Timeout_KilledAndSkipped passes in 0.50s with 500ms timeout. |
| 10 | Custom traits are available in the same cycle's templates and when: expressions | VERIFIED | loop.go lines 154-167 sync traits BEFORE fact collection (lines 170-175). Merge at lines 178-182 happens BEFORE RunOnce (line 191), which sends factsMap for template rendering on the server side. |

**Score:** 10/10 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `proto/isogov.proto` | CustomTraitEntry message, custom_traits field, DownloadCustomTrait RPC | VERIFIED | All three additions present (lines 220, 224-227, 229-237, 28) |
| `server/traits/scanner.go` | Directory scanner with SHA256 checksums and executable-bit filtering | VERIFIED | 122 lines. Exports Scanner, ScanDir, ReadFile, Entry. All substantive. |
| `server/traits/scanner_test.go` | Tests for scanner behaviors | VERIFIED | 8 tests, all passing. Covers filtering, name derivation, missing dir, ReadFile. |
| `server/grpc/traits.go` | DownloadCustomTrait RPC handler | VERIFIED | 27 lines. Validates input, reads file via scanner, returns bytes. |
| `server/grpc/checkin.go` | Manifest injection into CheckInResponse | VERIFIED | Lines 91-103 scan dir and populate CustomTraits field on response. |
| `server/grpc/services.go` | traitsScanner field on AgentService | VERIFIED | Line 21: `traitsScanner *traits.Scanner` |
| `server/grpc/server.go` | TraitsDir on ServerConfig, scanner wiring | VERIFIED | Line 68: TraitsDir field. Line 92: traits.NewScanner(cfg.TraitsDir). |
| `server/main.go` | ISOGOV_TRAITS_DIR env var wiring | VERIFIED | Line 46: `traitsDir := envOrDefault("ISOGOV_TRAITS_DIR", "traits")`. Line 138: passed to ServerConfig. |
| `agent/traits/executor.go` | Custom trait sync, cache, execution, JSON parsing | VERIFIED | 185 lines. Exports Executor, SyncAndRun, Downloader interface. Full implementation with SHA256 comparison, download, cleanup, timeout execution, JSON unmarshal. |
| `agent/traits/executor_test.go` | Tests for all executor behaviors | VERIFIED | 10 tests, all passing. Covers sync, cache, changed checksums, JSON parsing, errors, timeout, cleanup. |
| `agent/checkin/client.go` | DownloadCustomTrait client method | VERIFIED | Lines 153-162. Satisfies traits.Downloader interface. |
| `agent/checkin/loop.go` | Custom trait sync integrated before fact collection | VERIFIED | Lines 154-167 pre-check-in for manifest, sync/run. Lines 178-182 merge into factsMap. |
| `agent/main.go` | Traits cache dir passed to Loop | VERIFIED | Line 217: `traitsCacheDir := filepath.Join(dataDir, "traits")`. Line 218: passed to Loop. |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|-----|--------|---------|
| server/grpc/checkin.go | server/traits/scanner.go | scanner.ScanDir called during CheckIn | WIRED | Line 92: `s.traitsScanner.ScanDir()` |
| server/grpc/traits.go | traits directory on disk | os.ReadFile via scanner.ReadFile | WIRED | Line 19: `s.traitsScanner.ReadFile(req.GetName())` |
| agent/checkin/loop.go | agent/traits/executor.go | SyncAndRun called before facts.CollectAll | WIRED | Line 162: `traitsExec.SyncAndRun(ctx, preResp.CustomTraits)` |
| agent/traits/executor.go | agent/checkin/client.go | DownloadCustomTrait called for each changed trait | WIRED | Line 77: `e.downloader.DownloadCustomTrait(ctx, entry.Name)` -- Client satisfies Downloader interface |
| agent/checkin/loop.go | facts map merging | custom trait results merged into factsMap | WIRED | Lines 178-182: `for k, v := range customTraits { factsMap[k] = v }` |

### Requirements Coverage

| Requirement | Source Plan | Description | Status | Evidence |
|-------------|------------|-------------|--------|----------|
| CUST-01 | 06-01 | Server scans a configurable directory for custom trait executables and serves a manifest (name + SHA256 checksum) during check-in | SATISFIED | scanner.go filters executables, computes SHA256; checkin.go embeds manifest in CheckInResponse |
| CUST-02 | 06-01, 06-02 | Agent compares manifest against local cache and downloads new/changed trait executables via a dedicated gRPC call | SATISFIED | executor.go compares checksums, downloads only changed; client.go has DownloadCustomTrait method |
| CUST-03 | 06-02 | Agent executes each custom trait executable with a timeout, parses JSON stdout, and merges output under `custom.<name>` in the traits map | SATISFIED | executor.go runOne() with context.WithTimeout, json.Unmarshal, result nested under "custom" key |
| CUST-04 | 06-02 | Failed or timed-out custom trait executables are logged as warnings and skipped without affecting other traits or convergence | SATISFIED | executor.go logs slog.Warn and continues; process group kill ensures clean timeout; 10 tests confirm all failure modes |

No orphaned requirements found. All 4 CUST requirements mapped to phase 6 in REQUIREMENTS.md are covered by plans 06-01 and 06-02.

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| (none) | - | - | - | No anti-patterns detected |

No TODO/FIXME/PLACEHOLDER comments, no empty implementations, no stub returns in any phase 6 files.

### Human Verification Required

### 1. End-to-end custom trait distribution

**Test:** Place a shell script (e.g., `#!/bin/sh; echo '{"version":"1.0"}'`) in the server's `traits/` directory with executable bit set. Start the agent daemon and observe one check-in cycle.
**Expected:** Agent downloads the trait, executes it, and reports facts including `custom.app-version.version: "1.0"`. Visible via `gvctl clients traits <hostname> custom`.
**Why human:** Requires running server + agent infrastructure together with real gRPC/mTLS.

### 2. Timeout behavior with real slow script

**Test:** Place a script that sleeps for 30 seconds in `traits/`. Observe agent behavior.
**Expected:** Agent kills the script after 10 seconds, logs a warning, and continues with convergence.
**Why human:** Process group kill behavior may vary across OS configurations.

### Gaps Summary

No gaps found. All 10 observable truths verified. All 4 requirements satisfied. All artifacts exist, are substantive, and are properly wired. All 18 tests pass (8 server-side, 10 agent-side). All 3 binaries compile cleanly. No anti-patterns detected.

Minor note: The 06-02-PLAN mentions cache dir at `/var/lib/isogov/traits/` but agent/main.go uses `filepath.Join(dataDir, "traits")` where dataDir defaults to `/etc/isogov`. This is functionally correct -- the path is configurable via `--data-dir` flag, and the plan's Task 2 explicitly chose to make the cache dir a parameter. Not a gap.

---

_Verified: 2026-03-07T15:10:00Z_
_Verifier: Claude (gsd-verifier)_
