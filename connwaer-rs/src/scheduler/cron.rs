/// Hand-rolled cron expression parser. Supports:
/// - Standard 5-field: "minute hour day-of-month month day-of-week"
/// - Shortcuts: @daily, @hourly, @weekly, @monthly, @yearly
/// - Interval: @every 5m, @every 1h, @every 30s
///
/// Field syntax: *, */N, N, N-M, N-M/S, comma-separated lists

use anyhow::{bail, Result};
use chrono::{Datelike, NaiveDateTime, Timelike};

#[derive(Debug, Clone)]
pub enum CronSchedule {
    /// Standard 5-field cron.
    Cron {
        minutes: Vec<u32>,
        hours: Vec<u32>,
        days_of_month: Vec<u32>,
        months: Vec<u32>,
        days_of_week: Vec<u32>,
    },
    /// Fixed interval in seconds.
    Interval { seconds: u64 },
}

impl CronSchedule {
    /// Parse a cron expression string.
    pub fn parse(expr: &str) -> Result<Self> {
        let expr = expr.trim();

        // Handle shortcuts.
        match expr {
            "@yearly" | "@annually" => return Self::parse("0 0 1 1 *"),
            "@monthly" => return Self::parse("0 0 1 * *"),
            "@weekly" => return Self::parse("0 0 * * 0"),
            "@daily" | "@midnight" => return Self::parse("0 0 * * *"),
            "@hourly" => return Self::parse("0 * * * *"),
            _ => {}
        }

        // Handle @every interval.
        if let Some(interval) = expr.strip_prefix("@every ") {
            let seconds = parse_duration(interval.trim())?;
            if seconds == 0 {
                bail!("@every interval must be > 0");
            }
            return Ok(Self::Interval { seconds });
        }

        // Standard 5-field cron.
        let fields: Vec<&str> = expr.split_whitespace().collect();
        if fields.len() != 5 {
            bail!("cron expression must have 5 fields, got {}: '{expr}'", fields.len());
        }

        let minutes = parse_field(fields[0], 0, 59, "minute")?;
        let hours = parse_field(fields[1], 0, 23, "hour")?;
        let days_of_month = parse_field(fields[2], 1, 31, "day-of-month")?;
        let months = parse_field(fields[3], 1, 12, "month")?;
        let days_of_week = parse_field(fields[4], 0, 6, "day-of-week")?;

        Ok(Self::Cron { minutes, hours, days_of_month, months, days_of_week })
    }

    /// Check if the given time matches this schedule.
    pub fn matches(&self, dt: &NaiveDateTime) -> bool {
        match self {
            Self::Cron { minutes, hours, days_of_month, months, days_of_week } => {
                minutes.contains(&dt.minute())
                    && hours.contains(&dt.hour())
                    && days_of_month.contains(&dt.day())
                    && months.contains(&dt.month())
                    && days_of_week.contains(&dt.weekday().num_days_from_sunday())
            }
            Self::Interval { .. } => {
                // Interval schedules are handled by the scheduler loop, not by time matching.
                false
            }
        }
    }

    /// For interval schedules, return the interval in seconds.
    pub fn interval_seconds(&self) -> Option<u64> {
        match self {
            Self::Interval { seconds } => Some(*seconds),
            _ => None,
        }
    }
}

/// Parse a single cron field (e.g., "*/5", "1-15", "1,3,5", "*").
fn parse_field(field: &str, min: u32, max: u32, name: &str) -> Result<Vec<u32>> {
    let mut result = Vec::new();

    for part in field.split(',') {
        let part = part.trim();
        if part == "*" {
            return Ok((min..=max).collect());
        }

        // */N — step from min.
        if let Some(step_str) = part.strip_prefix("*/") {
            let step: u32 = step_str.parse()
                .map_err(|_| anyhow::anyhow!("{name}: invalid step in '{part}'"))?;
            if step == 0 {
                bail!("{name}: step must be > 0 in '{part}'");
            }
            let mut v = min;
            while v <= max {
                result.push(v);
                v += step;
            }
            continue;
        }

        // N-M or N-M/S — range with optional step.
        if part.contains('-') {
            let (range_part, step) = if part.contains('/') {
                let parts: Vec<&str> = part.splitn(2, '/').collect();
                let s: u32 = parts[1].parse()
                    .map_err(|_| anyhow::anyhow!("{name}: invalid step in '{part}'"))?;
                (parts[0], s)
            } else {
                (part, 1)
            };

            let bounds: Vec<&str> = range_part.splitn(2, '-').collect();
            let lo: u32 = bounds[0].parse()
                .map_err(|_| anyhow::anyhow!("{name}: invalid range start in '{part}'"))?;
            let hi: u32 = bounds[1].parse()
                .map_err(|_| anyhow::anyhow!("{name}: invalid range end in '{part}'"))?;

            if lo < min || hi > max || lo > hi {
                bail!("{name}: range {lo}-{hi} out of bounds ({min}-{max})");
            }
            if step == 0 {
                bail!("{name}: step must be > 0 in '{part}'");
            }

            let mut v = lo;
            while v <= hi {
                result.push(v);
                v += step;
            }
            continue;
        }

        // Single value.
        let val: u32 = part.parse()
            .map_err(|_| anyhow::anyhow!("{name}: invalid value '{part}'"))?;
        if val < min || val > max {
            bail!("{name}: value {val} out of bounds ({min}-{max})");
        }
        result.push(val);
    }

    if result.is_empty() {
        bail!("{name}: field produced no values: '{field}'");
    }

    result.sort_unstable();
    result.dedup();
    Ok(result)
}

/// Parse a duration string like "5m", "1h", "30s", "2h30m".
fn parse_duration(s: &str) -> Result<u64> {
    let s = s.trim();
    if s.is_empty() {
        bail!("empty duration");
    }

    let mut total: u64 = 0;
    let mut num_buf = String::new();

    for ch in s.chars() {
        if ch.is_ascii_digit() {
            num_buf.push(ch);
        } else {
            if num_buf.is_empty() {
                bail!("invalid duration: '{s}'");
            }
            let n: u64 = num_buf.parse()?;
            num_buf.clear();
            match ch {
                's' => total += n,
                'm' => total += n * 60,
                'h' => total += n * 3600,
                'd' => total += n * 86400,
                _ => bail!("unknown duration unit '{ch}' in '{s}'"),
            }
        }
    }

    // Bare number = seconds.
    if !num_buf.is_empty() {
        let n: u64 = num_buf.parse()?;
        total += n;
    }

    Ok(total)
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;

    fn dt(year: i32, month: u32, day: u32, hour: u32, min: u32) -> NaiveDateTime {
        NaiveDate::from_ymd_opt(year, month, day)
            .unwrap()
            .and_hms_opt(hour, min, 0)
            .unwrap()
    }

    #[test]
    fn parse_star() {
        let s = CronSchedule::parse("* * * * *").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 14, 30)));
    }

    #[test]
    fn parse_specific_time() {
        let s = CronSchedule::parse("30 14 * * *").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 14, 30)));
        assert!(!s.matches(&dt(2026, 3, 27, 14, 31)));
        assert!(!s.matches(&dt(2026, 3, 27, 15, 30)));
    }

    #[test]
    fn parse_step() {
        let s = CronSchedule::parse("*/15 * * * *").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 10, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 10, 15)));
        assert!(s.matches(&dt(2026, 3, 27, 10, 30)));
        assert!(s.matches(&dt(2026, 3, 27, 10, 45)));
        assert!(!s.matches(&dt(2026, 3, 27, 10, 10)));
    }

    #[test]
    fn parse_range() {
        let s = CronSchedule::parse("0 9-17 * * *").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 9, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 17, 0)));
        assert!(!s.matches(&dt(2026, 3, 27, 8, 0)));
        assert!(!s.matches(&dt(2026, 3, 27, 18, 0)));
    }

    #[test]
    fn parse_range_with_step() {
        let s = CronSchedule::parse("0 0-23/6 * * *").unwrap();
        // 0, 6, 12, 18
        assert!(s.matches(&dt(2026, 3, 27, 0, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 6, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 12, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 18, 0)));
        assert!(!s.matches(&dt(2026, 3, 27, 3, 0)));
    }

    #[test]
    fn parse_list() {
        let s = CronSchedule::parse("0 8,12,18 * * *").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 8, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 12, 0)));
        assert!(s.matches(&dt(2026, 3, 27, 18, 0)));
        assert!(!s.matches(&dt(2026, 3, 27, 10, 0)));
    }

    #[test]
    fn parse_day_of_week() {
        // 2026-03-27 is a Friday (day 5).
        let s = CronSchedule::parse("0 0 * * 5").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 0, 0)));
        // 2026-03-28 is Saturday (day 6).
        assert!(!s.matches(&dt(2026, 3, 28, 0, 0)));
    }

    #[test]
    fn shortcut_daily() {
        let s = CronSchedule::parse("@daily").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 0, 0)));
        assert!(!s.matches(&dt(2026, 3, 27, 0, 1)));
    }

    #[test]
    fn shortcut_hourly() {
        let s = CronSchedule::parse("@hourly").unwrap();
        assert!(s.matches(&dt(2026, 3, 27, 14, 0)));
        assert!(!s.matches(&dt(2026, 3, 27, 14, 1)));
    }

    #[test]
    fn parse_every_interval() {
        let s = CronSchedule::parse("@every 5m").unwrap();
        assert_eq!(s.interval_seconds(), Some(300));
    }

    #[test]
    fn parse_every_compound() {
        let s = CronSchedule::parse("@every 1h30m").unwrap();
        assert_eq!(s.interval_seconds(), Some(5400));
    }

    #[test]
    fn parse_every_seconds() {
        let s = CronSchedule::parse("@every 30s").unwrap();
        assert_eq!(s.interval_seconds(), Some(30));
    }

    #[test]
    fn reject_invalid_field_count() {
        assert!(CronSchedule::parse("* * *").is_err());
        assert!(CronSchedule::parse("* * * * * *").is_err());
    }

    #[test]
    fn reject_out_of_range() {
        assert!(CronSchedule::parse("60 * * * *").is_err());
        assert!(CronSchedule::parse("* 24 * * *").is_err());
        assert!(CronSchedule::parse("* * 32 * *").is_err());
        assert!(CronSchedule::parse("* * * 13 *").is_err());
        assert!(CronSchedule::parse("* * * * 7").is_err());
    }

    #[test]
    fn reject_zero_step() {
        assert!(CronSchedule::parse("*/0 * * * *").is_err());
    }

    #[test]
    fn reject_zero_interval() {
        assert!(CronSchedule::parse("@every 0s").is_err());
    }

    #[test]
    fn duration_parse() {
        assert_eq!(parse_duration("30s").unwrap(), 30);
        assert_eq!(parse_duration("5m").unwrap(), 300);
        assert_eq!(parse_duration("2h").unwrap(), 7200);
        assert_eq!(parse_duration("1d").unwrap(), 86400);
        assert_eq!(parse_duration("1h30m").unwrap(), 5400);
        assert_eq!(parse_duration("60").unwrap(), 60);
    }
}
