use anyhow::Result;
use std::time::Instant;

use super::{Capability, CapabilityContext, CapabilityResult, Evidence, VerificationStatus};

pub struct CapacityMonitor;

impl Capability for CapacityMonitor {
    fn name(&self) -> &'static str { "capacity_monitor" }
    fn controls(&self) -> &'static [&'static str] { &["RTS7-Art11-1", "RTS7-Art11-2"] }

    fn verify(&self, ctx: &CapabilityContext) -> Result<CapabilityResult> {
        let start = Instant::now();
        let started_at = chrono::Utc::now().to_rfc3339();

        let (status, summary, evidence) = run_benchmarks(ctx)?;

        let elapsed = start.elapsed();
        Ok(CapabilityResult {
            capability: self.name().to_string(),
            status,
            summary,
            evidence,
            controls: self.controls().iter().map(|s| s.to_string()).collect(),
            started_at,
            completed_at: chrono::Utc::now().to_rfc3339(),
            duration_ms: elapsed.as_millis() as u64,
            platform: ctx.platform.clone(),
            hostname: ctx.hostname.clone(),
            version: ctx.version.clone(),
            error: None,
        })
    }
}

fn run_benchmarks(
    ctx: &CapabilityContext,
) -> Result<(VerificationStatus, String, Vec<Evidence>)> {
    let mut evidence = Vec::new();
    let mut issues = Vec::new();

    // --- Disk usage ---
    let disk_pct = get_disk_usage_pct("/");
    evidence.push(Evidence {
        key: "disk_usage_pct".to_string(),
        value: serde_json::json!(disk_pct),
        detail: format!("Root filesystem usage: {:.1}%", disk_pct),
    });

    let disk_threshold = ctx.config.thresholds.get("disk_usage_max_pct")
        .and_then(|v| v.as_f64())
        .unwrap_or(90.0);
    if disk_pct > disk_threshold {
        issues.push(format!("disk usage {:.1}% exceeds {:.0}% threshold", disk_pct, disk_threshold));
    }

    // --- Memory usage ---
    let mem = get_memory_usage();
    evidence.push(Evidence {
        key: "memory_usage_pct".to_string(),
        value: serde_json::json!(mem.usage_pct),
        detail: format!("Memory usage: {:.1}% ({} MB / {} MB)",
            mem.usage_pct, mem.used_mb, mem.total_mb),
    });

    let mem_threshold = ctx.config.thresholds.get("memory_usage_max_pct")
        .and_then(|v| v.as_f64())
        .unwrap_or(90.0);
    if mem.usage_pct > mem_threshold {
        issues.push(format!("memory usage {:.1}% exceeds {:.0}% threshold", mem.usage_pct, mem_threshold));
    }

    // --- CPU benchmark (matrix multiply) ---
    let cpu_score = cpu_benchmark();
    evidence.push(Evidence {
        key: "cpu_score".to_string(),
        value: serde_json::json!(cpu_score),
        detail: format!("CPU benchmark score: {} (128x128 matrix multiply iterations/sec)", cpu_score),
    });

    let cpu_min = ctx.config.thresholds.get("cpu_score_min")
        .and_then(|v| v.as_u64())
        .unwrap_or(0);
    if cpu_min > 0 && cpu_score < cpu_min {
        issues.push(format!("CPU score {} below minimum {}", cpu_score, cpu_min));
    }

    // --- Disk I/O benchmark ---
    let disk_write_mbps = disk_io_benchmark();
    evidence.push(Evidence {
        key: "disk_write_mbps".to_string(),
        value: serde_json::json!(format!("{:.1}", disk_write_mbps)),
        detail: format!("Sequential disk write: {:.1} MB/s", disk_write_mbps),
    });

    let disk_mbps_min = ctx.config.thresholds.get("disk_write_mbps_min")
        .and_then(|v| v.as_f64())
        .unwrap_or(0.0);
    if disk_mbps_min > 0.0 && disk_write_mbps < disk_mbps_min {
        issues.push(format!("disk write {:.1} MB/s below {:.1} MB/s minimum", disk_write_mbps, disk_mbps_min));
    }

    // --- Load average (Linux) ---
    #[cfg(target_os = "linux")]
    {
        if let Ok(loadavg) = std::fs::read_to_string("/proc/loadavg") {
            let parts: Vec<&str> = loadavg.split_whitespace().collect();
            if parts.len() >= 3 {
                let load1: f64 = parts[0].parse().unwrap_or(0.0);
                let load5: f64 = parts[1].parse().unwrap_or(0.0);
                let load15: f64 = parts[2].parse().unwrap_or(0.0);
                evidence.push(Evidence {
                    key: "load_average".to_string(),
                    value: serde_json::json!({"1m": load1, "5m": load5, "15m": load15}),
                    detail: format!("Load average: {load1:.2} / {load5:.2} / {load15:.2}"),
                });
            }
        }
    }

    // --- Trend analysis from historical data ---
    let trend = ctx.trends_db.compute_trend(
        "capacity_monitor", "disk_usage_pct", Some(100.0),
    );
    if let Ok(t) = trend {
        if t.data_points >= 5 {
            evidence.push(Evidence {
                key: "disk_trend_slope".to_string(),
                value: serde_json::json!(format!("{:.3}", t.slope_per_day)),
                detail: format!("Disk usage trend: {:.3}%/day over {} data points",
                    t.slope_per_day, t.data_points),
            });
            if let Some(ref date) = t.projected_exhaustion {
                evidence.push(Evidence {
                    key: "disk_projected_full".to_string(),
                    value: serde_json::json!(date),
                    detail: format!("Projected disk full: {date}"),
                });
                issues.push(format!("disk projected full by {date}"));
            }
        }
    }

    // Record metrics for future trend computation.
    let metrics = serde_json::json!({
        "disk_usage_pct": disk_pct,
        "memory_usage_pct": mem.usage_pct,
        "cpu_score": cpu_score,
        "disk_write_mbps": disk_write_mbps,
    });
    let _ = ctx.trends_db.record_run("capacity_monitor", "measured", &metrics);

    let status = if issues.is_empty() {
        VerificationStatus::Pass
    } else {
        VerificationStatus::Warn
    };

    let summary = if issues.is_empty() {
        format!("disk {:.0}%, memory {:.0}%, CPU score {}", disk_pct, mem.usage_pct, cpu_score)
    } else {
        issues.join("; ")
    };

    Ok((status, summary, evidence))
}

// --- System metrics ---

fn get_disk_usage_pct(path: &str) -> f64 {
    #[cfg(unix)]
    {
        unsafe {
            let mut stat: libc::statvfs = std::mem::zeroed();
            let c_path = std::ffi::CString::new(path).unwrap();
            if libc::statvfs(c_path.as_ptr(), &mut stat) == 0 {
                let total = stat.f_blocks as f64 * stat.f_frsize as f64;
                let avail = stat.f_bavail as f64 * stat.f_frsize as f64;
                if total > 0.0 {
                    return (1.0 - avail / total) * 100.0;
                }
            }
        }
        0.0
    }
    #[cfg(not(unix))]
    {
        // Windows: use GetDiskFreeSpaceExW or fall back to wmic.
        let _ = path;
        0.0
    }
}

struct MemoryInfo {
    total_mb: u64,
    used_mb: u64,
    usage_pct: f64,
}

fn get_memory_usage() -> MemoryInfo {
    #[cfg(target_os = "linux")]
    {
        if let Ok(content) = std::fs::read_to_string("/proc/meminfo") {
            let parse_kb = |key: &str| -> u64 {
                content.lines()
                    .find(|l| l.starts_with(key))
                    .and_then(|l| l.split_whitespace().nth(1))
                    .and_then(|v| v.parse::<u64>().ok())
                    .unwrap_or(0)
            };
            let total_kb = parse_kb("MemTotal:");
            let available_kb = parse_kb("MemAvailable:");
            let used_kb = total_kb.saturating_sub(available_kb);
            let usage_pct = if total_kb > 0 {
                used_kb as f64 / total_kb as f64 * 100.0
            } else { 0.0 };
            return MemoryInfo {
                total_mb: total_kb / 1024,
                used_mb: used_kb / 1024,
                usage_pct,
            };
        }
    }
    MemoryInfo { total_mb: 0, used_mb: 0, usage_pct: 0.0 }
}

/// CPU benchmark: 128x128 matrix multiply, count iterations in 1 second.
fn cpu_benchmark() -> u64 {
    let size = 128;
    let a: Vec<f64> = (0..size * size).map(|i| (i % 17) as f64).collect();
    let b: Vec<f64> = (0..size * size).map(|i| (i % 13) as f64).collect();
    let mut c = vec![0.0f64; size * size];

    let deadline = Instant::now() + std::time::Duration::from_secs(1);
    let mut iterations = 0u64;

    while Instant::now() < deadline {
        // Single matrix multiply pass.
        for i in 0..size {
            for j in 0..size {
                let mut sum = 0.0f64;
                for k in 0..size {
                    sum += a[i * size + k] * b[k * size + j];
                }
                c[i * size + j] = sum;
            }
        }
        iterations += 1;
    }

    // Prevent optimizer from eliding the computation.
    std::hint::black_box(&c);
    iterations
}

/// Disk I/O benchmark: sequential write of 64 MB, measure throughput.
fn disk_io_benchmark() -> f64 {
    let dir = match tempfile::tempdir() {
        Ok(d) => d,
        Err(_) => return 0.0,
    };
    let path = dir.path().join("connwaer_io_bench");
    let data = vec![0x42u8; 1024 * 1024]; // 1 MB block
    let blocks = 64;

    let start = Instant::now();
    let mut file = match std::fs::File::create(&path) {
        Ok(f) => f,
        Err(_) => return 0.0,
    };

    use std::io::Write;
    for _ in 0..blocks {
        if file.write_all(&data).is_err() {
            break;
        }
    }
    let _ = file.sync_all();
    let elapsed = start.elapsed();

    let mb_written = blocks as f64;
    if elapsed.as_secs_f64() > 0.0 {
        mb_written / elapsed.as_secs_f64()
    } else {
        0.0
    }
}
