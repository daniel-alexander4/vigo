// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Exponential backoff for server unavailability.
//!
//! Mirrors the Go `cenkalti/backoff` configuration: 5s initial, 5m max,
//! 2x multiplier, unlimited retries.

use std::time::Duration;

pub struct Backoff {
    current: Duration,
    initial: Duration,
    max: Duration,
    multiplier: f64,
}

impl Backoff {
    pub fn new() -> Self {
        Self {
            current: Duration::from_secs(5),
            initial: Duration::from_secs(5),
            max: Duration::from_secs(300), // 5 minutes
            multiplier: 2.0,
        }
    }

    /// Returns the next backoff interval and advances the state.
    pub fn next_backoff(&mut self) -> Duration {
        let result = self.current;
        let next = Duration::from_secs_f64(self.current.as_secs_f64() * self.multiplier);
        self.current = if next > self.max { self.max } else { next };
        result
    }

    /// Reset backoff to the initial interval.
    pub fn reset(&mut self) {
        self.current = self.initial;
    }

    /// The current backoff ceiling — `set_max_from_interval` keeps this at 2×
    /// the check-in interval (floor 10s), or the 5-minute default before any
    /// interval is known. The orphaned-envoy retry path floors against this so
    /// it never retries more often than ~2 normal cycles.
    pub fn max(&self) -> Duration {
        self.max
    }

    /// Update the max backoff based on the server-provided check-in interval.
    /// Caps at 2x the interval so the agent reconnects within two cycles of
    /// the server coming back. Floor of 10s to avoid tight retry loops on
    /// very short intervals.
    pub fn set_max_from_interval(&mut self, interval: Duration) {
        let capped = (interval * 2).max(Duration::from_secs(10));
        self.max = capped;
        // If current backoff already exceeds the new max, clamp it.
        if self.current > self.max {
            self.current = self.max;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn initial_interval() {
        let mut b = Backoff::new();
        assert_eq!(b.next_backoff(), Duration::from_secs(5));
    }

    #[test]
    fn doubles() {
        let mut b = Backoff::new();
        assert_eq!(b.next_backoff(), Duration::from_secs(5));
        assert_eq!(b.next_backoff(), Duration::from_secs(10));
        assert_eq!(b.next_backoff(), Duration::from_secs(20));
        assert_eq!(b.next_backoff(), Duration::from_secs(40));
        assert_eq!(b.next_backoff(), Duration::from_secs(80));
        assert_eq!(b.next_backoff(), Duration::from_secs(160));
    }

    #[test]
    fn caps_at_max() {
        let mut b = Backoff::new();
        for _ in 0..20 {
            let d = b.next_backoff();
            assert!(d <= Duration::from_secs(300));
        }
    }

    #[test]
    fn max_getter_reflects_interval() {
        let mut b = Backoff::new();
        assert_eq!(b.max(), Duration::from_secs(300)); // default before any interval
        b.set_max_from_interval(Duration::from_secs(600)); // 10m → 2× = 20m
        assert_eq!(b.max(), Duration::from_secs(1200));
    }

    #[test]
    fn reset_returns_to_initial() {
        let mut b = Backoff::new();
        b.next_backoff();
        b.next_backoff();
        b.reset();
        assert_eq!(b.next_backoff(), Duration::from_secs(5));
    }

    #[test]
    fn set_max_from_interval_caps_at_2x() {
        let mut b = Backoff::new();
        b.set_max_from_interval(Duration::from_secs(15));
        // Max should be 30s (2x15s)
        for _ in 0..20 {
            assert!(b.next_backoff() <= Duration::from_secs(30));
        }
    }

    #[test]
    fn set_max_from_interval_floor_10s() {
        let mut b = Backoff::new();
        b.set_max_from_interval(Duration::from_secs(2));
        // 2x2s = 4s, but floor is 10s
        for _ in 0..20 {
            assert!(b.next_backoff() <= Duration::from_secs(10));
        }
    }

    #[test]
    fn set_max_from_interval_clamps_current() {
        let mut b = Backoff::new();
        // Drive backoff up to 160s
        for _ in 0..6 {
            b.next_backoff();
        }
        // Now set a lower max — current should clamp down
        b.set_max_from_interval(Duration::from_secs(10));
        assert!(b.next_backoff() <= Duration::from_secs(20));
    }
}
