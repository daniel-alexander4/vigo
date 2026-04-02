pub mod schema;

use anyhow::Result;
use std::path::Path;
use std::sync::Mutex;

/// Stores historical capability results for trend analysis.
/// Thread-safe via Mutex since rusqlite::Connection is not Sync.
pub struct TrendStore {
    conn: Mutex<rusqlite::Connection>,
}

impl TrendStore {
    pub fn open(path: &Path) -> Result<Self> {
        let conn = rusqlite::Connection::open(path)?;
        conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;")?;
        schema::initialize(&conn)?;
        Ok(Self { conn: Mutex::new(conn) })
    }

    /// Open an in-memory database (for testing).
    pub fn open_memory() -> Result<Self> {
        let conn = rusqlite::Connection::open_in_memory()?;
        schema::initialize(&conn)?;
        Ok(Self { conn: Mutex::new(conn) })
    }

    /// Record a capability run result.
    pub fn record_run(
        &self,
        capability: &str,
        status: &str,
        metrics: &serde_json::Value,
    ) -> Result<()> {
        let conn = self.conn.lock().map_err(|e| anyhow::anyhow!("lock: {e}"))?;
        conn.execute(
            "INSERT INTO runs (capability, status, metrics, timestamp) VALUES (?1, ?2, ?3, datetime('now'))",
            rusqlite::params![capability, status, metrics.to_string()],
        )?;
        Ok(())
    }

    /// Query recent runs for a capability, most recent first.
    pub fn query_runs(
        &self,
        capability: &str,
        limit: usize,
    ) -> Result<Vec<RunRecord>> {
        let conn = self.conn.lock().map_err(|e| anyhow::anyhow!("lock: {e}"))?;
        let mut stmt = conn.prepare(
            "SELECT id, capability, status, metrics, timestamp FROM runs WHERE capability = ?1 ORDER BY timestamp DESC LIMIT ?2",
        )?;
        let rows = stmt.query_map(rusqlite::params![capability, limit], |row| {
            Ok(RunRecord {
                id: row.get(0)?,
                capability: row.get(1)?,
                status: row.get(2)?,
                metrics: row.get(3)?,
                timestamp: row.get(4)?,
            })
        })?;
        Ok(rows.filter_map(|r| r.ok()).collect())
    }

    /// Compute a linear regression trend for a specific metric extracted from
    /// the JSON metrics column. Returns slope (change per day) and projected
    /// exhaustion date if slope is positive and a threshold is provided.
    ///
    /// `metric_key` is a JSON path into the metrics column (e.g., "rto_seconds",
    /// "disk_usage_pct"). `threshold` is the value at which exhaustion occurs
    /// (e.g., 100.0 for percentage-based metrics).
    pub fn compute_trend(
        &self,
        capability: &str,
        metric_key: &str,
        threshold: Option<f64>,
    ) -> Result<TrendResult> {
        let records = self.query_runs(capability, 1000)?;

        if records.len() < 2 {
            return Ok(TrendResult {
                slope_per_day: 0.0,
                data_points: records.len(),
                projected_exhaustion: None,
                latest_value: None,
            });
        }

        // Extract (day_offset, value) pairs. Day 0 = oldest record.
        let mut points: Vec<(f64, f64)> = Vec::new();
        let mut latest_value: Option<f64> = None;

        // Records are most-recent-first; reverse for chronological order.
        for (i, record) in records.iter().rev().enumerate() {
            if let Ok(metrics) = serde_json::from_str::<serde_json::Value>(&record.metrics) {
                if let Some(val) = metrics.get(metric_key).and_then(|v| v.as_f64()) {
                    let day = i as f64; // approximate: each record is one run interval
                    points.push((day, val));
                    latest_value = Some(val);
                }
            }
        }

        if points.len() < 2 {
            return Ok(TrendResult {
                slope_per_day: 0.0,
                data_points: points.len(),
                projected_exhaustion: None,
                latest_value,
            });
        }

        // Simple linear regression: y = mx + b.
        let n = points.len() as f64;
        let sum_x: f64 = points.iter().map(|(x, _)| x).sum();
        let sum_y: f64 = points.iter().map(|(_, y)| y).sum();
        let sum_xy: f64 = points.iter().map(|(x, y)| x * y).sum();
        let sum_xx: f64 = points.iter().map(|(x, _)| x * x).sum();

        let denom = n * sum_xx - sum_x * sum_x;
        let slope = if denom.abs() > f64::EPSILON {
            (n * sum_xy - sum_x * sum_y) / denom
        } else {
            0.0
        };

        // Project exhaustion date if slope is positive and threshold given.
        let projected_exhaustion = if let (Some(thresh), Some(latest)) = (threshold, latest_value) {
            if slope > f64::EPSILON && latest < thresh {
                let days_remaining = (thresh - latest) / slope;
                let exhaust_date = chrono::Utc::now() + chrono::Duration::days(days_remaining as i64);
                Some(exhaust_date.format("%Y-%m-%d").to_string())
            } else {
                None
            }
        } else {
            None
        };

        Ok(TrendResult {
            slope_per_day: slope,
            data_points: points.len(),
            projected_exhaustion,
            latest_value,
        })
    }

    /// Prune old records, keeping at most `keep` per capability.
    pub fn prune(&self, keep: usize) -> Result<usize> {
        let conn = self.conn.lock().map_err(|e| anyhow::anyhow!("lock: {e}"))?;
        let deleted: usize = conn.execute(
            "DELETE FROM runs WHERE id NOT IN (
                SELECT id FROM runs r2 WHERE r2.capability = runs.capability
                ORDER BY timestamp DESC LIMIT ?1
            )",
            rusqlite::params![keep],
        )?;
        Ok(deleted)
    }
}

#[derive(Debug)]
pub struct RunRecord {
    pub id: i64,
    pub capability: String,
    pub status: String,
    pub metrics: String,
    pub timestamp: String,
}

#[derive(Debug)]
pub struct TrendResult {
    pub slope_per_day: f64,
    pub data_points: usize,
    pub projected_exhaustion: Option<String>,
    pub latest_value: Option<f64>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn record_and_query() {
        let store = TrendStore::open_memory().unwrap();
        let metrics = serde_json::json!({"rto_seconds": 120});
        store.record_run("backup_restore", "pass", &metrics).unwrap();
        store.record_run("backup_restore", "pass", &serde_json::json!({"rto_seconds": 130})).unwrap();

        let runs = store.query_runs("backup_restore", 10).unwrap();
        assert_eq!(runs.len(), 2);
        assert_eq!(runs[0].capability, "backup_restore");
    }

    #[test]
    fn query_empty() {
        let store = TrendStore::open_memory().unwrap();
        let runs = store.query_runs("nonexistent", 10).unwrap();
        assert!(runs.is_empty());
    }

    #[test]
    fn query_respects_limit() {
        let store = TrendStore::open_memory().unwrap();
        let m = serde_json::json!({"v": 1});
        for _ in 0..10 {
            store.record_run("test", "pass", &m).unwrap();
        }
        let runs = store.query_runs("test", 3).unwrap();
        assert_eq!(runs.len(), 3);
    }

    #[test]
    fn compute_trend_insufficient_data() {
        let store = TrendStore::open_memory().unwrap();
        let result = store.compute_trend("test", "value", None).unwrap();
        assert_eq!(result.data_points, 0);
        assert_eq!(result.slope_per_day, 0.0);
    }

    #[test]
    fn compute_trend_increasing() {
        let store = TrendStore::open_memory().unwrap();
        // Simulate increasing disk usage.
        for i in 0..10 {
            let m = serde_json::json!({"disk_pct": 50.0 + i as f64 * 5.0});
            store.record_run("capacity", "pass", &m).unwrap();
        }
        let result = store.compute_trend("capacity", "disk_pct", Some(100.0)).unwrap();
        assert_eq!(result.data_points, 10);
        assert!(result.slope_per_day > 0.0, "slope should be positive");
        assert!(result.projected_exhaustion.is_some(), "should project exhaustion");
        assert!(result.latest_value.unwrap() > 80.0);
    }

    #[test]
    fn compute_trend_stable() {
        let store = TrendStore::open_memory().unwrap();
        for _ in 0..10 {
            let m = serde_json::json!({"rto_seconds": 120.0});
            store.record_run("backup", "pass", &m).unwrap();
        }
        let result = store.compute_trend("backup", "rto_seconds", None).unwrap();
        assert!(result.slope_per_day.abs() < 0.01, "slope should be near zero");
        assert!(result.projected_exhaustion.is_none());
    }

    #[test]
    fn compute_trend_missing_metric_key() {
        let store = TrendStore::open_memory().unwrap();
        for _ in 0..5 {
            let m = serde_json::json!({"other_key": 42});
            store.record_run("test", "pass", &m).unwrap();
        }
        let result = store.compute_trend("test", "nonexistent", None).unwrap();
        assert_eq!(result.data_points, 0);
    }

    #[test]
    fn prune_keeps_recent() {
        let store = TrendStore::open_memory().unwrap();
        let m = serde_json::json!({"v": 1});
        for _ in 0..20 {
            store.record_run("test", "pass", &m).unwrap();
        }
        let deleted = store.prune(5).unwrap();
        assert!(deleted > 0);
        let remaining = store.query_runs("test", 100).unwrap();
        assert!(remaining.len() <= 5);
    }
}
