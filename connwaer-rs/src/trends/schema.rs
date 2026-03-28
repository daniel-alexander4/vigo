use anyhow::Result;
use rusqlite::Connection;

pub fn initialize(conn: &Connection) -> Result<()> {
    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS runs (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            capability TEXT NOT NULL,
            status TEXT NOT NULL,
            metrics TEXT NOT NULL DEFAULT '{}',
            timestamp TEXT NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_runs_capability ON runs (capability, timestamp);

        CREATE TABLE IF NOT EXISTS trend_cache (
            capability TEXT PRIMARY KEY,
            metric_key TEXT NOT NULL,
            slope REAL,
            projected_exhaustion TEXT,
            computed_at TEXT NOT NULL
        );"
    )?;
    Ok(())
}
