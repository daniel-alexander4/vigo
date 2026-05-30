#!/usr/bin/env python3
"""report.py — turn a burstwave run directory into a plain-language report.

Reads the artifacts written by test/burstwave (results.csv, server.csv,
resources.csv, manifest.json, run.log) and emits a standardized report aimed
at a human reader rather than someone fluent in gRPC status codes:

  * raw gRPC status codes are renamed to plain-language outcomes;
  * the measured window is split into a "Startup surge" (the thundering-herd
    period right after every envoy connects at once) and a "Steady state"
    measured after the surge clears, so a transient cold-start spike is never
    mistaken for the system's sustained behavior;
  * a HARD FAILURE verdict (true/false) is derived from whether vigosrv stops
    making forward progress / leaks / drops connections — distinct from merely
    degraded-but-recovering — followed by the factors behind the verdict.

Usage:
    test/burstwave/report.py <run-dir> [<run-dir> ...] [--surge-sec N]

Pass several run dirs to get one comparison table across sizes.
"""
import csv
import json
import os
import sys

# --- plain-language names for the raw gRPC status codes the harness records.
# The left side is what status.Code().String() emits; the right is what a
# human reading the report should understand the outcome to mean.
OUTCOME_LABEL = {
    "Unavailable":      "Server busy — bounced, auto-retried next cycle",
    "ResourceExhausted":"Write shed by backpressure (by design)",
    "DeadlineExceeded": "Timed out — RPC exceeded its deadline",
    "Canceled":         "Canceled (window ended / shutdown)",
    "Internal":         "Server-side error",
    "other":            "Other",
}

SURGE_SEC_DEFAULT = 60  # first N seconds of the measured window = startup surge


def _read_csv(path):
    if not os.path.exists(path):
        return []
    with open(path) as f:
        return list(csv.DictReader(f))


def _host_port_range():
    """Usable ephemeral source ports on this host. Fallback for pre-A2 runs
    whose manifest lacks ephemeral_port_range; assumes this host matches the
    one the run was captured on. Defaults to the Linux 32768-60999 range."""
    try:
        lo, hi = open("/proc/sys/net/ipv4/ip_local_port_range").read().split()
        return int(hi) - int(lo) + 1
    except (OSError, ValueError):
        return 60999 - 32768 + 1


def _f(row, key, default=0.0):
    v = row.get(key, "")
    if v is None or v == "":
        return default
    try:
        return float(v)
    except ValueError:
        return default


def _failures_from_runlog(run_dir):
    """Parse the cumulative 'failures by code' block from run.log."""
    out = {}
    path = os.path.join(run_dir, "run.log")
    if not os.path.exists(path):
        return out
    in_block = False
    with open(path) as f:
        for line in f:
            if "failures by code" in line:
                in_block = True
                continue
            if in_block:
                parts = line.split()
                if len(parts) == 2 and parts[1].isdigit():
                    out[parts[0]] = int(parts[1])
                elif line.strip() and not line.startswith(" "):
                    break
    return out


def _window_rate(rows, lo, hi, field):
    """Mean per-second count of `field` over elapsed_sec in [lo, hi)."""
    vals = [_f(r, field) for r in rows
            if lo <= _f(r, "elapsed_sec") < hi]
    return sum(vals) / len(vals) if vals else 0.0


def _window_pctl_peak(rows, lo, hi, field):
    """Worst (max) per-second percentile seen in the window — a stand-in for
    'how bad did tail latency get' since we only have per-second p50/p99."""
    vals = [_f(r, field) for r in rows
            if lo <= _f(r, "elapsed_sec") < hi and _f(r, field) > 0]
    return max(vals) if vals else 0.0


def analyze(run_dir, surge_sec):
    manifest = {}
    mpath = os.path.join(run_dir, "manifest.json")
    if os.path.exists(mpath):
        manifest = json.load(open(mpath))

    results = _read_csv(os.path.join(run_dir, "results.csv"))
    server = _read_csv(os.path.join(run_dir, "server.csv"))
    resources = _read_csv(os.path.join(run_dir, "resources.csv"))
    failures = _failures_from_runlog(run_dir)

    offered = int(manifest.get("envoy_count", 0)) + int(manifest.get("second_count", 0) or 0)
    interval = manifest.get("interval", "?")

    window_end = max((_f(r, "elapsed_sec") for r in results), default=0.0)

    # Surge vs steady-state split.
    surge = {
        "checkin_ok_s": _window_rate(results, 0, surge_sec, "checkin_ok"),
        "report_ok_s":  _window_rate(results, 0, surge_sec, "report_ok"),
        "shed_s":       _window_rate(results, 0, surge_sec, "report_shed"),
        "checkin_p99_worst": _window_pctl_peak(results, 0, surge_sec, "checkin_p99_ms"),
    }
    steady = {
        "checkin_ok_s": _window_rate(results, surge_sec, 1e12, "checkin_ok"),
        "report_ok_s":  _window_rate(results, surge_sec, 1e12, "report_ok"),
        "shed_s":       _window_rate(results, surge_sec, 1e12, "report_shed"),
        "checkin_p50_worst": _window_pctl_peak(results, surge_sec, 1e12, "checkin_p50_ms"),
        "checkin_p99_worst": _window_pctl_peak(results, surge_sec, 1e12, "checkin_p99_ms"),
    }

    # Resource ceilings.
    peak_goroutines = max((_f(r, "goroutines") for r in server), default=0.0)
    peak_rss_mb = max((_f(r, "rss_mb") for r in server), default=0.0)
    max_fds = max((_f(r, "open_fds") for r in resources), default=0.0)

    # Harness port-exhaustion ceiling. Every synthetic envoy dials the SAME
    # destination, so connections from one source IP share that IP's ~28k
    # ephemeral-port pool. If the harness offered more connections than the
    # source IPs can hold, the run measured the harness's own port wall, not
    # the server — which would otherwise read as a false server "hard failure".
    src_ips = int(manifest.get("source_ips", 1) or 1)
    port_range = int(manifest.get("ephemeral_port_range", 0) or 0)
    if port_range <= 0:  # pre-A2 runs lack the field; read this host's range.
        port_range = _host_port_range()
    port_ceiling = src_ips * port_range
    port_exhausted = (
        offered > port_ceiling and max_fds >= 0.95 * port_ceiling)
    # Did goroutines plateau (healthy) or keep climbing (leak)? Compare the
    # mean of the last third of the series to the mean of the middle third.
    goro_series = [_f(r, "goroutines") for r in server if _f(r, "goroutines") > 0]
    leak_ratio = None
    if len(goro_series) >= 6:
        third = len(goro_series) // 3
        mid = sum(goro_series[third:2 * third]) / max(third, 1)
        last = sum(goro_series[2 * third:]) / max(len(goro_series) - 2 * third, 1)
        leak_ratio = (last / mid) if mid else None

    # --- Verdict evaluation -----------------------------------------------
    # If the harness hit its own port wall, the run never cleanly loaded the
    # server, so neither "hard failure" nor "healthy" can be claimed — the
    # result is INCONCLUSIVE. Short-circuit before the server-failure detectors
    # so a harness artifact is never re-misdiagnosed as a vigosrv failure.
    if port_exhausted:
        return {
            "run_dir": run_dir,
            "offered": offered,
            "interval": interval,
            "version": manifest.get("vigo_version", "?"),
            "window_end": window_end,
            "surge_sec": surge_sec,
            "surge": surge,
            "steady": steady,
            "peak_goroutines": peak_goroutines,
            "peak_rss_mb": peak_rss_mb,
            "max_fds": max_fds,
            "ceiling_hit": True,
            "failures": failures,
            "verdict": "INCONCLUSIVE",
            "factors": [
                f"Harness port-exhaustion ceiling, NOT a server limit: held "
                f"{max_fds:,.0f} conns against a {port_ceiling:,} cap "
                f"({src_ips} source IP{'s' if src_ips != 1 else ''} × {port_range:,} "
                f"ephemeral ports) while {offered:,} were offered. Every envoy dials the "
                f"same destination, so each source IP exhausts its port pool. "
                f"Re-run with more --source-ips (≥{(offered + port_range - 1) // port_range}) "
                f"to actually load the server."],
        }

    # --- HARD FAILURE evaluation ------------------------------------------
    # Degraded (conns reestablish, server recovers) is NOT hard failure.
    # Hard failure = vigosrv stops making forward progress, leaks without
    # bound, or sheds its connections — a state it does not recover from
    # inside the run.
    factors = []
    # 1. Forward progress: is the steady-state read path still serving a
    #    meaningful fraction of the offered load, or has it wedged near zero?
    progress_floor = 0.01 * offered  # <1% of offered conns served/s = wedged
    wedged = offered > 0 and steady["checkin_ok_s"] < progress_floor
    if wedged:
        factors.append(
            f"Read path wedged: steady-state {steady['checkin_ok_s']:.0f} successful "
            f"check-ins/s is <1% of the {offered} offered — server never digests the load")
    # 2. Latency pegged: steady-state p50 in multi-second territory means even
    #    the requests that succeed are effectively stalled.
    latency_pegged = steady["checkin_p50_worst"] >= 5000
    if latency_pegged:
        factors.append(
            f"Latency pegged: steady-state check-in p50 reached "
            f"{steady['checkin_p50_worst']/1000:.1f}s (healthy is sub-second)")
    # 3. Timeouts dominate bounces: when DeadlineExceeded outweighs Unavailable,
    #    requests are blocking until they time out rather than fast-bouncing —
    #    the queue is backed up, not shedding.
    de = failures.get("DeadlineExceeded", 0)
    un = failures.get("Unavailable", 0)
    timeouts_dominate = de > 0 and de >= un
    if timeouts_dominate and (wedged or latency_pegged):
        factors.append(
            f"Timeouts dominate: {de:,} timed-out vs {un:,} bounced — requests "
            f"block until deadline instead of fast-retrying (queue backed up)")
    # 4. Goroutine leak: series climbing with no plateau (>25% growth late-run).
    leak = leak_ratio is not None and leak_ratio > 1.25
    if leak:
        factors.append(
            f"Goroutine leak: late-run goroutines {leak_ratio:.2f}x the mid-run "
            f"level — not plateauing")
    # 5. Connection collapse: never held a meaningful fraction of offered conns.
    conn_collapse = offered > 0 and max_fds < 0.5 * offered
    # Note: a *ceiling* (holds a fixed max below offered) is not collapse;
    # collapse is failing to hold even half. We surface the ceiling separately.

    hard_failure = bool(
        (wedged and latency_pegged) or leak or conn_collapse)

    if not factors:
        factors.append("None — degraded under load but recovering; "
                       "connections held and the server kept making progress")

    return {
        "run_dir": run_dir,
        "offered": offered,
        "interval": interval,
        "version": manifest.get("vigo_version", "?"),
        "window_end": window_end,
        "surge_sec": surge_sec,
        "surge": surge,
        "steady": steady,
        "peak_goroutines": peak_goroutines,
        "peak_rss_mb": peak_rss_mb,
        "max_fds": max_fds,
        "ceiling_hit": offered > 0 and max_fds < 0.95 * offered,
        "failures": failures,
        "verdict": "HARD FAILURE" if hard_failure else "HEALTHY",
        "factors": factors,
    }


def _fmt_failures(failures):
    if not failures:
        return "  (none recorded)"
    lines = []
    total = sum(failures.values())
    for code, n in sorted(failures.items(), key=lambda kv: -kv[1]):
        label = OUTCOME_LABEL.get(code, code)
        lines.append(f"  {label:<48} {n:>12,}")
    lines.append(f"  {'TOTAL non-OK RPC attempts':<48} {total:>12,}")
    return "\n".join(lines)


def render(reports):
    out = []
    v = reports[0]["version"] if reports else "?"
    out.append(f"# Burstwave report — {v}\n")
    out.append(f"_Startup surge = first {reports[0]['surge_sec']}s of the measured "
               f"window (the thundering herd as every envoy connects at once). "
               f"Steady state = everything after, i.e. sustained behavior._\n")

    for r in reports:
        out.append(f"\n## {r['offered']:,} envoys @ {r['interval']}\n")
        out.append("| Measure | Startup surge | Steady state |")
        out.append("|---|---:|---:|")
        out.append(f"| Successful check-ins/s (read) | {r['surge']['checkin_ok_s']:,.0f} | "
                   f"{r['steady']['checkin_ok_s']:,.0f} |")
        out.append(f"| Successful reports/s (write)  | {r['surge']['report_ok_s']:,.0f} | "
                   f"{r['steady']['report_ok_s']:,.0f} |")
        out.append(f"| Writes shed/s (backpressure)  | {r['surge']['shed_s']:,.0f} | "
                   f"{r['steady']['shed_s']:,.0f} |")
        out.append(f"| Check-in p99 (worst, ms)      | {r['surge']['checkin_p99_worst']:,.0f} | "
                   f"{r['steady']['checkin_p99_worst']:,.0f} |")

        out.append("\n**Resource ceilings (whole run):**")
        out.append(f"- Connections held (max FDs): **{r['max_fds']:,.0f}** of {r['offered']:,} offered"
                   + ("  ⚠️ connection ceiling hit" if r["ceiling_hit"] else ""))
        out.append(f"- Peak goroutines: {r['peak_goroutines']:,.0f}")
        out.append(f"- Peak RSS: {r['peak_rss_mb']:,.0f} MB")

        out.append("\n**Outcome of non-OK RPC attempts (cumulative):**")
        out.append("```")
        out.append(_fmt_failures(r["failures"]))
        out.append("```")

        badge = {
            "HARD FAILURE": "🔴 HARD FAILURE",
            "HEALTHY":      "🟢 HEALTHY (degraded under load but recovering)",
            "INCONCLUSIVE": "🟡 INCONCLUSIVE — harness limit, server not measured",
        }[r["verdict"]]
        out.append(f"\n**Verdict: {badge}**\n")
        out.append("Factors:")
        for fct in r["factors"]:
            out.append(f"- {fct}")
        out.append("")

    return "\n".join(out)


def main():
    args = sys.argv[1:]
    surge_sec = SURGE_SEC_DEFAULT
    dirs = []
    i = 0
    while i < len(args):
        if args[i] == "--surge-sec":
            surge_sec = int(args[i + 1]); i += 2
        else:
            dirs.append(args[i]); i += 1
    if not dirs:
        print(__doc__)
        sys.exit(2)
    reports = [analyze(d, surge_sec) for d in dirs]
    print(render(reports))


if __name__ == "__main__":
    main()
