
# Overview

Connwaer is a standalone Rust binary that performs active compliance verification on managed nodes. Unlike passive scanning or configuration management, Connwaer proves that compliance controls are working by testing them -- attempting unauthorized writes to WORM storage, measuring NTP drift against multiple sources, running NIST statistical tests on RNG output, and executing actual backup restore procedures.

## Why is Connwaer not included as part of Vigo?

Vigo enforces desired state -- it configures systems and reports whether they converged. That work is passive and predictable: check a file hash, ensure a service is running, apply a package version.

Connwaer does something fundamentally different. It actively tests controls by performing potentially disruptive operations: writing to storage that should reject writes, running multi-hour backup restores, executing CPU and disk benchmarks, attempting network connections that should be blocked, and shelling out to platform-specific tools like `ausearch`, `cryptsetup`, and `manage-bde`. These operations carry real resource cost and real risk if misconfigured.

Bundling this into the Vigo agent would mean every node runs verification logic whether it needs it or not, every check-in cycle becomes heavier, and a bug in a verification capability could affect convergence. Keeping them separate means:

- **Vigo stays fast and safe.** The agent's check-in loop remains a lightweight check-apply-report cycle with no benchmarks, no shell-outs to audit tools, and no multi-hour restore tests.
- **Connwaer runs on its own schedule.** Backup restores run weekly at 2 AM, not every 15-second convergence cycle. Each capability has its own cron schedule and timeout.
- **Failure isolation.** A PKCS#11 library crash or a hung `aide --check` does not affect state enforcement.
- **Deploy only where needed.** Most nodes need Vigo. Only nodes subject to compliance verification need Connwaer.

## Architecture

Connwaer runs as a daemon (`connwaer run`) alongside the Vigo agent. It is a completely separate binary with no shared code or runtime dependencies on the Vigo agent. The two communicate through the filesystem:

1. Connwaer writes JSON result files to `/var/lib/connwaer/results/`
2. The Vigo agent's `connwaer` trait collector reads those files
3. Results appear as `.Traits.connwaer.*` on the Vigo server
4. The server upgrades compliance control statuses based on fleet-wide verification coverage

Connwaer requires a Vigo installation. The Vigo server enforces node count licensing and provides fleet-wide compliance scoring from Connwaer results.

## How It Works

Connwaer schedules 18 verification capabilities via cron expressions or fixed intervals. Each capability:

1. Reads its parameters and thresholds from the config file
2. Executes platform-specific verification logic (checking services, running benchmarks, attempting blocked operations)
3. Produces a `CapabilityResult` with structured evidence
4. Writes the result as `<capability>.json` to the results directory
5. Records metrics to a local SQLite trends database for historical analysis

## Result Structure

Every capability produces a JSON result file:

```json
{
  "capability": "time_sync",
  "status": "pass",
  "summary": "NTP offset 12ms (threshold: 100ms)",
  "evidence": [
    {"key": "ntp_service", "value": "chronyd", "detail": "chronyd is running"},
    {"key": "offset_ms", "value": 12, "detail": "Current offset: 12ms"}
  ],
  "controls": ["PCI-DSS-10.4", "NIST-AU-8"],
  "started_at": "2026-03-28T10:00:00Z",
  "completed_at": "2026-03-28T10:00:01Z",
  "duration_ms": 1042,
  "platform": "linux",
  "hostname": "web-01.example.com",
  "version": "0.1.1"
}
```

## Verification Statuses

| Status | Meaning |
|--------|---------|
| `pass` | Control verified -- all checks passed |
| `fail` | Control violated -- one or more checks failed |
| `warn` | Partial compliance -- some checks passed, others need attention |
| `error` | Verification could not complete (tool missing, permission denied, etc.) |
| `skipped` | Capability not applicable on this platform or not configured |

## Platform Support

Connwaer supports Linux and Windows. Most capabilities have platform-specific implementations -- for example, `encryption_at_rest` checks LUKS on Linux and BitLocker on Windows. Capabilities that cannot run on the current platform return `skipped`.

## Default Paths

| Path | Linux | Windows |
|------|-------|---------|
| Config file | `/etc/connwaer/connwaer.yaml` | `C:\ProgramData\Connwaer\connwaer.yaml` |
| Results directory | `/var/lib/connwaer/results/` | `C:\ProgramData\Connwaer\results\` |
| Trends database | `/var/lib/connwaer/trends.db` | `C:\ProgramData\Connwaer\trends.db` |
| Binary | `/usr/local/sbin/connwaer` | `C:\Program Files\Connwaer\connwaer.exe` |
| License directory | `/srv/vigo/license/` | `C:\ProgramData\Vigo\license\` |
