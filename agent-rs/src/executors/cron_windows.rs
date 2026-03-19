// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0
#![cfg(windows)]

//! Windows scheduled task executor — manages scheduled tasks via `schtasks.exe`.
//!
//! Translates common cron-style schedule patterns to `schtasks` equivalents.
//! Supports creating, deleting, and checking scheduled tasks.

use super::{Action, Executor, ResourceResult, run_command};
use std::collections::HashMap;
use std::time::Instant;

/// Manages Windows scheduled tasks using `schtasks.exe`.
pub struct CronWindowsExecutor;

impl CronWindowsExecutor {
    pub fn new() -> Self {
        Self
    }

    /// Check if a scheduled task exists by name.
    fn task_exists(name: &str) -> bool {
        let output = run_command("schtasks", &["/Query", "/TN", name, "/FO", "LIST"]);
        match output {
            Ok(o) => o.status.success(),
            Err(_) => false,
        }
    }

    /// Translate a cron schedule expression to schtasks /SC and /MO parameters.
    ///
    /// Supports common patterns:
    /// - `@hourly`  -> /SC HOURLY
    /// - `@daily`   -> /SC DAILY
    /// - `@weekly`  -> /SC WEEKLY
    /// - `@monthly` -> /SC MONTHLY
    /// - `@reboot`  -> /SC ONSTART
    /// - `*/N * * * *` -> /SC MINUTE /MO N
    /// - `0 */N * * *` -> /SC HOURLY /MO N
    /// - `0 N * * *`   -> /SC DAILY /ST HH:00
    /// - `0 N * * D`   -> /SC WEEKLY /D day /ST HH:00
    /// - `0 N D * *`   -> /SC MONTHLY /D day /ST HH:00
    ///
    /// Returns (schedule_type, modifier_or_empty, start_time_or_empty, day_or_empty).
    fn translate_schedule(schedule: &str) -> Result<ScheduleParams, String> {
        let s = schedule.trim();

        // Handle named shortcuts.
        match s {
            "@hourly" => {
                return Ok(ScheduleParams {
                    sc: "HOURLY".into(),
                    mo: String::new(),
                    st: String::new(),
                    day: String::new(),
                });
            }
            "@daily" | "@midnight" => {
                return Ok(ScheduleParams {
                    sc: "DAILY".into(),
                    mo: String::new(),
                    st: "00:00".into(),
                    day: String::new(),
                });
            }
            "@weekly" => {
                return Ok(ScheduleParams {
                    sc: "WEEKLY".into(),
                    mo: String::new(),
                    st: "00:00".into(),
                    day: String::new(),
                });
            }
            "@monthly" => {
                return Ok(ScheduleParams {
                    sc: "MONTHLY".into(),
                    mo: String::new(),
                    st: "00:00".into(),
                    day: "1".into(),
                });
            }
            "@reboot" => {
                return Ok(ScheduleParams {
                    sc: "ONSTART".into(),
                    mo: String::new(),
                    st: String::new(),
                    day: String::new(),
                });
            }
            _ => {}
        }

        // Parse 5-field cron expression: minute hour day month weekday
        let fields: Vec<&str> = s.split_whitespace().collect();
        if fields.len() != 5 {
            return Err(format!(
                "cron_windows executor: unsupported schedule {:?} (expected 5 fields or @shortcut)",
                schedule
            ));
        }

        let minute = fields[0];
        let hour = fields[1];
        let day = fields[2];
        let _month = fields[3];
        let weekday = fields[4];

        // */N * * * * -> every N minutes
        if minute.starts_with("*/") && hour == "*" && day == "*" && weekday == "*" {
            let interval = &minute[2..];
            return Ok(ScheduleParams {
                sc: "MINUTE".into(),
                mo: interval.to_string(),
                st: String::new(),
                day: String::new(),
            });
        }

        // * */N * * * -> every N hours
        if minute == "0" && hour.starts_with("*/") && day == "*" && weekday == "*" {
            let interval = &hour[2..];
            return Ok(ScheduleParams {
                sc: "HOURLY".into(),
                mo: interval.to_string(),
                st: String::new(),
                day: String::new(),
            });
        }

        // 0 N * * D -> weekly on day D at N:00
        if minute == "0" && day == "*" && weekday != "*" {
            let h: u32 = hour.parse().unwrap_or(0);
            let day_name = Self::cron_weekday_to_schtasks(weekday);
            return Ok(ScheduleParams {
                sc: "WEEKLY".into(),
                mo: String::new(),
                st: format!("{:02}:00", h),
                day: day_name,
            });
        }

        // 0 N D * * -> monthly on day D at N:00
        if minute == "0" && day != "*" && weekday == "*" {
            let h: u32 = hour.parse().unwrap_or(0);
            return Ok(ScheduleParams {
                sc: "MONTHLY".into(),
                mo: String::new(),
                st: format!("{:02}:00", h),
                day: day.to_string(),
            });
        }

        // M N * * * -> daily at N:M
        if day == "*" && weekday == "*" {
            let h: u32 = hour.parse().unwrap_or(0);
            let m: u32 = minute.parse().unwrap_or(0);
            return Ok(ScheduleParams {
                sc: "DAILY".into(),
                mo: String::new(),
                st: format!("{:02}:{:02}", h, m),
                day: String::new(),
            });
        }

        Err(format!(
            "cron_windows executor: cannot translate schedule {:?} to schtasks",
            schedule
        ))
    }

    /// Convert cron weekday number (0=Sun..6=Sat) to schtasks day name.
    fn cron_weekday_to_schtasks(wd: &str) -> String {
        match wd {
            "0" | "7" => "SUN".into(),
            "1" => "MON".into(),
            "2" => "TUE".into(),
            "3" => "WED".into(),
            "4" => "THU".into(),
            "5" => "FRI".into(),
            "6" => "SAT".into(),
            _ => wd.to_uppercase(),
        }
    }
}

struct ScheduleParams {
    sc: String,
    mo: String,
    st: String,
    day: String,
}

impl Executor for CronWindowsExecutor {
    fn execute(&self, params: &HashMap<String, String>, dry_run: bool) -> ResourceResult {
        let start = Instant::now();
        let name = params.get("name").cloned().unwrap_or_default();

        let mut result = ResourceResult::new("cron_windows", &name);

        if name.is_empty() {
            result.error = Some(
                "cron_windows executor: name param is required".to_string(),
            );
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params
            .get("state")
            .map(|s| s.as_str())
            .unwrap_or("present");
        let exists = Self::task_exists(&name);

        match state {
            "present" => {
                let command = params.get("command").cloned().unwrap_or_default();
                if command.is_empty() {
                    result.error = Some(
                        "cron_windows executor: command param is required when state=present"
                            .to_string(),
                    );
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                let schedule = params
                    .get("schedule")
                    .cloned()
                    .unwrap_or_else(|| "* * * * *".into());

                let sched = match Self::translate_schedule(&schedule) {
                    Ok(s) => s,
                    Err(e) => {
                        result.error = Some(e);
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                };

                if exists {
                    // Task already exists. For idempotency, we delete and recreate
                    // to ensure the schedule and command match.
                    result.action = Action::Updated;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }

                    // Delete existing task first.
                    match run_command("schtasks", &["/Delete", "/TN", &name, "/F"]) {
                        Ok(o) if !o.status.success() => {
                            let stderr = String::from_utf8_lossy(&o.stderr);
                            result.error = Some(format!(
                                "schtasks /Delete {}: {}",
                                name, stderr
                            ));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        Err(e) => {
                            result.error = Some(format!(
                                "schtasks /Delete {}: {}",
                                name, e
                            ));
                            result.duration_ms = start.elapsed().as_millis() as i32;
                            return result;
                        }
                        _ => {}
                    }
                } else {
                    result.action = Action::Created;
                    result.changed = true;

                    if dry_run {
                        result.duration_ms = start.elapsed().as_millis() as i32;
                        return result;
                    }
                }

                // Build schtasks /Create command.
                let mut args = vec![
                    "/Create".to_string(),
                    "/TN".into(),
                    name.clone(),
                    "/TR".into(),
                    command.clone(),
                    "/SC".into(),
                    sched.sc,
                ];

                if !sched.mo.is_empty() {
                    args.push("/MO".into());
                    args.push(sched.mo);
                }
                if !sched.st.is_empty() {
                    args.push("/ST".into());
                    args.push(sched.st);
                }
                if !sched.day.is_empty() {
                    args.push("/D".into());
                    args.push(sched.day);
                }

                // Run as specific user if provided.
                if let Some(user) = params.get("user") {
                    if !user.is_empty() {
                        args.push("/RU".into());
                        args.push(user.clone());
                    }
                }

                // Force overwrite.
                args.push("/F".into());

                let arg_refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
                match run_command("schtasks", &arg_refs) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        let stdout = String::from_utf8_lossy(&o.stdout);
                        result.error = Some(format!(
                            "schtasks /Create {}: {} {}",
                            name, stderr, stdout
                        ));
                    }
                    Err(e) => {
                        result.error =
                            Some(format!("schtasks /Create {}: {}", name, e));
                    }
                    _ => {}
                }
            }
            "absent" => {
                if !exists {
                    result.action = Action::None;
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                result.action = Action::Deleted;
                result.changed = true;

                if dry_run {
                    result.duration_ms = start.elapsed().as_millis() as i32;
                    return result;
                }

                match run_command("schtasks", &["/Delete", "/TN", &name, "/F"]) {
                    Ok(o) if !o.status.success() => {
                        let stderr = String::from_utf8_lossy(&o.stderr);
                        result.error = Some(format!(
                            "schtasks /Delete {}: {}",
                            name, stderr
                        ));
                    }
                    Err(e) => {
                        result.error =
                            Some(format!("schtasks /Delete {}: {}", name, e));
                    }
                    _ => {}
                }
            }
            _ => {
                result.error = Some(format!(
                    "cron_windows executor: unknown state {:?}",
                    state
                ));
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_name() {
        let exec = CronWindowsExecutor::new();
        let params = HashMap::new();
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("name param is required"));
    }

    #[test]
    fn missing_command_when_present() {
        let exec = CronWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("name".to_string(), "MyTask".to_string());
        params.insert("state".to_string(), "present".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result
            .error
            .unwrap()
            .contains("command param is required when state=present"));
    }

    #[test]
    fn invalid_schedule_bad_field_count() {
        let result = CronWindowsExecutor::translate_schedule("* * *");
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("expected 5 fields or @shortcut"));
    }

    #[test]
    fn translate_schedule_named_shortcuts() {
        let hourly = CronWindowsExecutor::translate_schedule("@hourly").unwrap();
        assert_eq!(hourly.sc, "HOURLY");

        let daily = CronWindowsExecutor::translate_schedule("@daily").unwrap();
        assert_eq!(daily.sc, "DAILY");
        assert_eq!(daily.st, "00:00");

        let weekly = CronWindowsExecutor::translate_schedule("@weekly").unwrap();
        assert_eq!(weekly.sc, "WEEKLY");

        let monthly = CronWindowsExecutor::translate_schedule("@monthly").unwrap();
        assert_eq!(monthly.sc, "MONTHLY");
        assert_eq!(monthly.day, "1");

        let reboot = CronWindowsExecutor::translate_schedule("@reboot").unwrap();
        assert_eq!(reboot.sc, "ONSTART");
    }

    #[test]
    fn translate_schedule_every_n_minutes() {
        let sched = CronWindowsExecutor::translate_schedule("*/15 * * * *").unwrap();
        assert_eq!(sched.sc, "MINUTE");
        assert_eq!(sched.mo, "15");
    }

    #[test]
    fn translate_schedule_every_n_hours() {
        let sched = CronWindowsExecutor::translate_schedule("0 */2 * * *").unwrap();
        assert_eq!(sched.sc, "HOURLY");
        assert_eq!(sched.mo, "2");
    }

    #[test]
    fn translate_schedule_daily_at_time() {
        let sched = CronWindowsExecutor::translate_schedule("30 14 * * *").unwrap();
        assert_eq!(sched.sc, "DAILY");
        assert_eq!(sched.st, "14:30");
    }

    #[test]
    fn translate_schedule_weekly() {
        let sched = CronWindowsExecutor::translate_schedule("0 3 * * 1").unwrap();
        assert_eq!(sched.sc, "WEEKLY");
        assert_eq!(sched.st, "03:00");
        assert_eq!(sched.day, "MON");
    }

    #[test]
    fn translate_schedule_monthly() {
        let sched = CronWindowsExecutor::translate_schedule("0 5 15 * *").unwrap();
        assert_eq!(sched.sc, "MONTHLY");
        assert_eq!(sched.st, "05:00");
        assert_eq!(sched.day, "15");
    }

    #[test]
    fn cron_weekday_mapping() {
        assert_eq!(CronWindowsExecutor::cron_weekday_to_schtasks("0"), "SUN");
        assert_eq!(CronWindowsExecutor::cron_weekday_to_schtasks("7"), "SUN");
        assert_eq!(CronWindowsExecutor::cron_weekday_to_schtasks("1"), "MON");
        assert_eq!(CronWindowsExecutor::cron_weekday_to_schtasks("5"), "FRI");
        assert_eq!(CronWindowsExecutor::cron_weekday_to_schtasks("6"), "SAT");
    }

    #[test]
    fn unknown_state() {
        let exec = CronWindowsExecutor::new();
        let mut params = HashMap::new();
        params.insert("name".to_string(), "MyTask".to_string());
        params.insert("state".to_string(), "bogus".to_string());
        let result = exec.execute(&params, false);
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown state"));
    }
}
