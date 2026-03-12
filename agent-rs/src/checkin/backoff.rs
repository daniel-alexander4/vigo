// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

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
    fn reset_returns_to_initial() {
        let mut b = Backoff::new();
        b.next_backoff();
        b.next_backoff();
        b.reset();
        assert_eq!(b.next_backoff(), Duration::from_secs(5));
    }
}
