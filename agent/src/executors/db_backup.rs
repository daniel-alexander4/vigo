// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Database backup executor — dumps databases using native tools, then
//! stores the dumps in hardlink-based incremental snapshot directories.
//!
//! Supported db_type values: postgres, mysql, mariadb, sqlite, mongodb.
//! Custom databases: set db_dump_command to override the built-in command.
//!
//! The dump file is written to a staging directory, then the staging dir
//! is snapshotted using the same hardlink technique as the backup executor.
//! Directory naming: YYYY.JJJ.HH.MM (year.julian_day.hour.minute)

use super::backup::{self};
use super::{Action, CommandRunner, DefaultRunner, Executor, ResourceResult};
use std::collections::HashMap;
use std::fs;
use std::path::Path;
use std::time::Instant;
use tracing::info;

pub struct DbBackupExecutor {
    pub runner: Box<dyn CommandRunner>,
}

impl DbBackupExecutor {
    pub fn new() -> Self {
        Self {
            runner: Box::new(DefaultRunner),
        }
    }
}

/// Built-in database dump configurations.
struct DumpConfig {
    tool: &'static str,
    package_hint: &'static str,
    default_port: &'static str,
}

fn dump_config(db_type: &str) -> Option<DumpConfig> {
    match db_type {
        "postgres" => Some(DumpConfig {
            tool: "pg_dump",
            package_hint: "postgresql-client",
            default_port: "5432",
        }),
        "mysql" | "mariadb" => Some(DumpConfig {
            tool: "mysqldump",
            package_hint: if db_type == "mariadb" {
                "mariadb-client"
            } else {
                "mysql-client"
            },
            default_port: "3306",
        }),
        "sqlite" => Some(DumpConfig {
            tool: "sqlite3",
            package_hint: "sqlite3",
            default_port: "",
        }),
        "mongodb" => Some(DumpConfig {
            tool: "mongodump",
            package_hint: "mongodb-database-tools",
            default_port: "27017",
        }),
        _ => None,
    }
}

/// Build the dump command args for a built-in db_type.
fn build_dump_args(
    db_type: &str,
    db_name: &str,
    db_user: &str,
    db_host: &str,
    db_port: &str,
    output_path: &str,
) -> Vec<String> {
    match db_type {
        "postgres" => {
            let mut args = vec![
                "-Fc".to_string(), // custom format (compressed, restorable)
                "-f".to_string(),
                output_path.to_string(),
            ];
            if !db_host.is_empty() {
                args.extend(["-h".to_string(), db_host.to_string()]);
            }
            if !db_port.is_empty() {
                args.extend(["-p".to_string(), db_port.to_string()]);
            }
            if !db_user.is_empty() {
                args.extend(["-U".to_string(), db_user.to_string()]);
            }
            args.push(db_name.to_string());
            args
        }
        "mysql" | "mariadb" => {
            let mut args = vec![
                "--single-transaction".to_string(), // consistent snapshot
                "--result-file".to_string(),
                output_path.to_string(),
            ];
            if !db_host.is_empty() {
                args.extend(["-h".to_string(), db_host.to_string()]);
            }
            if !db_port.is_empty() {
                args.extend(["-P".to_string(), db_port.to_string()]);
            }
            if !db_user.is_empty() {
                args.extend(["-u".to_string(), db_user.to_string()]);
            }
            args.push(db_name.to_string());
            args
        }
        "sqlite" => {
            // db_name is the path to the database file.
            vec![
                db_name.to_string(),
                format!(".backup '{}'", output_path),
            ]
        }
        "mongodb" => {
            let mut args = vec![
                format!("--archive={}", output_path),
                format!("--db={}", db_name),
            ];
            if !db_host.is_empty() {
                args.push(format!("--host={}", db_host));
            }
            if !db_port.is_empty() {
                args.push(format!("--port={}", db_port));
            }
            if !db_user.is_empty() {
                args.push(format!("--username={}", db_user));
            }
            args
        }
        _ => vec![],
    }
}

/// Dump file extension for each db_type.
fn dump_extension(db_type: &str) -> &'static str {
    match db_type {
        "postgres" => "pgfc",
        "mysql" | "mariadb" => "sql",
        "sqlite" => "db",
        "mongodb" => "archive",
        _ => "dump",
    }
}

impl Executor for DbBackupExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let database = params.get("source_db").map(|s| s.as_str()).unwrap_or("");
        let mut result = ResourceResult::new("db_backup", database);

        let res = self.execute_db_backup(params, dry_run, &mut result);
        result.duration_ms = start.elapsed().as_millis() as i32;
        if let Err(e) = res {
            result.error = Some(e);
        }
        result
    }
}

impl DbBackupExecutor {
    fn execute_db_backup(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
        result: &mut ResourceResult,
    ) -> Result<(), String> {
        let db_type = params
            .get("db_type")
            .filter(|s| !s.is_empty())
            .ok_or_else(|| "db_backup: db_type param is required".to_string())?;

        let db_name = params
            .get("source_db")
            .filter(|s| !s.is_empty())
            .ok_or_else(|| "db_backup: source_db param is required".to_string())?;

        let backup_dir = params
            .get("target_path")
            .filter(|s| !s.is_empty())
            .ok_or_else(|| "db_backup: target_path param is required".to_string())?;

        let retain_days: u64 = params
            .get("retain")
            .and_then(|s| s.parse().ok())
            .unwrap_or(30);

        let schedule = params.get("schedule").map(|s| s.as_str()).unwrap_or("daily");
        let db_user = params.get("db_user").map(|s| s.as_str()).unwrap_or("");
        let db_host = params.get("db_host").map(|s| s.as_str()).unwrap_or("localhost");
        let custom_command = params.get("db_dump_command");

        let min_free = backup::parse_size(
            params.get("min_free_space").map(|s| s.as_str()).unwrap_or("1G"),
        )
        .map_err(|e| format!("db_backup: invalid min_free_space: {}", e))?;

        // Check free space on the backup_dir filesystem.
        let backup_check_path = if Path::new(backup_dir).exists() {
            backup_dir.to_string()
        } else if let Some(parent) = Path::new(backup_dir).parent() {
            parent.to_string_lossy().to_string()
        } else {
            backup_dir.to_string()
        };
        if let Some(avail) = backup::free_space(&backup_check_path) {
            if avail < min_free {
                return Err(format!(
                    "db_backup skipped: {} has {} free (min_free_space: {})",
                    backup_dir,
                    backup::format_size(avail),
                    backup::format_size(min_free),
                ));
            }
        }

        // Resolve the dump tool.
        let (tool, args_fn): (String, Box<dyn Fn(&str) -> Vec<String>>) =
            if let Some(cmd) = custom_command {
                // Custom command — extract the tool name (first word) for validation.
                let tool = cmd.split_whitespace().next().unwrap_or("").to_string();
                let cmd_owned = cmd.clone();
                let output_placeholder = "{output}".to_string();
                (
                    tool,
                    Box::new(move |output_path: &str| {
                        // Replace {output} placeholder in the command.
                        let full = cmd_owned.replace(&output_placeholder, output_path);
                        vec!["-c".to_string(), full]
                    }),
                )
            } else {
                let cfg = dump_config(db_type).ok_or_else(|| {
                    format!(
                        "db_backup: unknown db_type {:?} — use postgres, mysql, mariadb, sqlite, \
                         mongodb, or set db_dump_command for custom databases",
                        db_type
                    )
                })?;

                let db_port = params
                    .get("db_port")
                    .map(|s| s.as_str())
                    .unwrap_or(cfg.default_port);

                // Check the dump tool is available.
                if self.runner.run("which", &[cfg.tool]).is_err()
                    || !self
                        .runner
                        .run("which", &[cfg.tool])
                        .map(|o| o.status.success())
                        .unwrap_or(false)
                {
                    return Err(format!(
                        "db_backup: {} not found — install {}",
                        cfg.tool, cfg.package_hint
                    ));
                }

                let tool = cfg.tool.to_string();
                let db_type = db_type.to_string();
                let db_name = db_name.to_string();
                let db_user = db_user.to_string();
                let db_host = db_host.to_string();
                let db_port = db_port.to_string();
                (
                    tool,
                    Box::new(move |output_path: &str| {
                        build_dump_args(
                            &db_type,
                            &db_name,
                            &db_user,
                            &db_host,
                            &db_port,
                            output_path,
                        )
                    }),
                )
            };

        // Check if a snapshot already exists in the current schedule window.
        let now = backup::snapshot_name();
        if let Some(latest) = backup::find_latest_snapshot(backup_dir) {
            let latest_name = latest.file_name().unwrap_or_default().to_string_lossy();
            if backup::same_schedule_window(&latest_name, &now, schedule) {
                result.action = Action::None;
                result.changed = false;
                return Ok(());
            }
        }

        if dry_run {
            result.action = Action::Created;
            result.changed = true;
            return Ok(());
        }

        // Create the snapshot directory.
        let snapshot_path = Path::new(backup_dir).join(&now);
        fs::create_dir_all(&snapshot_path)
            .map_err(|e| format!("create snapshot dir {}: {}", snapshot_path.display(), e))?;

        // Run the dump.
        let ext = dump_extension(db_type);
        let dump_file = if db_type == "sqlite" {
            // db_name is an absolute path to the source file; Path::join would
            // replace snapshot_path, and the filename already carries a .db/.sqlite
            // suffix, so use the basename as-is.
            let basename = Path::new(db_name)
                .file_name()
                .map(|s| s.to_string_lossy().into_owned())
                .unwrap_or_else(|| format!("dump.{}", ext));
            snapshot_path.join(basename)
        } else {
            snapshot_path.join(format!("{}.{}", db_name, ext))
        };
        let dump_path = dump_file.to_string_lossy().to_string();

        info!(
            db_type = %db_type,
            db_name = %db_name,
            output = %dump_path,
            "running database dump"
        );

        let args = args_fn(&dump_path);

        // For custom commands, run via shell. For built-in, run the tool directly.
        let run_result = if custom_command.is_some() {
            self.runner.run("sh", &args.iter().map(|s| s.as_str()).collect::<Vec<_>>())
        } else {
            self.runner.run(&tool, &args.iter().map(|s| s.as_str()).collect::<Vec<_>>())
        };

        match run_result {
            Ok(output) if !output.status.success() => {
                let stderr = String::from_utf8_lossy(&output.stderr);
                // Clean up failed snapshot dir.
                let _ = fs::remove_dir_all(&snapshot_path);
                return Err(format!("db_backup: {} failed: {}", tool, stderr.trim()));
            }
            Err(e) => {
                let _ = fs::remove_dir_all(&snapshot_path);
                return Err(format!("db_backup: {} failed: {}", tool, e));
            }
            _ => {}
        }

        // Verify the dump file was created and is non-empty.
        match fs::metadata(&dump_file) {
            Ok(meta) if meta.len() == 0 => {
                let _ = fs::remove_dir_all(&snapshot_path);
                return Err(format!("db_backup: dump file is empty: {}", dump_path));
            }
            Err(e) => {
                let _ = fs::remove_dir_all(&snapshot_path);
                return Err(format!("db_backup: dump file not created: {}", e));
            }
            Ok(meta) => {
                info!(
                    db_type = %db_type,
                    db_name = %db_name,
                    size_bytes = meta.len(),
                    snapshot = %snapshot_path.display(),
                    "database dump complete"
                );
            }
        }

        // Prune old snapshots.
        let pruned = backup::prune_snapshots(backup_dir, retain_days)?;
        if pruned > 0 {
            info!(pruned = pruned, retain_days = retain_days, "pruned old db snapshots");
        }

        result.action = Action::Created;
        result.changed = true;
        Ok(())
    }
}
