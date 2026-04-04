// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Package circuit breaker — tracks consecutive infrastructure failures per
//! resource and backs off to hourly retries when the threshold is reached.
//!
//! Infrastructure errors (broken repo, expired GPG key, disk full) indicate
//! that retrying every convergence cycle won't help. The breaker reduces noise
//! by reporting `Blocked` and rechecking once per hour.

use crate::executors::pkgmgr::errors::ErrorClass;
use std::collections::HashMap;
use std::time::{Duration, Instant};

/// Consecutive infrastructure failures before the breaker trips.
const BREAKER_THRESHOLD: u32 = 3;

/// How long a tripped breaker stays open before allowing a retry.
const BREAKER_COOLDOWN: Duration = Duration::from_secs(3600); // 1 hour

/// Per-resource circuit breaker state.
#[derive(Debug)]
struct BreakerEntry {
    /// Number of consecutive infrastructure-class errors.
    consecutive: u32,
    /// The last error class that triggered the breaker.
    last_class: ErrorClass,
    /// The last formatted error message.
    last_message: String,
    /// When the breaker tripped (None if not tripped).
    tripped_at: Option<Instant>,
}

/// Tracks circuit breaker state for package/repository resources.
/// Persists across convergence cycles (owned by the check-in loop).
pub struct PackageBreaker {
    entries: HashMap<String, BreakerEntry>,
}

/// Result of checking the breaker before execution.
pub enum BreakerCheck {
    /// Resource is not blocked — proceed with execution.
    Allow,
    /// Resource is blocked — skip execution and return this error message.
    Blocked(String),
}

impl PackageBreaker {
    pub fn new() -> Self {
        Self {
            entries: HashMap::new(),
        }
    }

    /// Check if a resource is currently blocked by the circuit breaker.
    /// Returns `BreakerCheck::Blocked` with the cached error message if the
    /// breaker is tripped and the cooldown hasn't elapsed.
    pub fn check(&self, resource_key: &str) -> BreakerCheck {
        let Some(entry) = self.entries.get(resource_key) else {
            return BreakerCheck::Allow;
        };

        let Some(tripped_at) = entry.tripped_at else {
            return BreakerCheck::Allow;
        };

        if tripped_at.elapsed() >= BREAKER_COOLDOWN {
            // Cooldown elapsed — allow a retry.
            return BreakerCheck::Allow;
        }

        let remaining = BREAKER_COOLDOWN - tripped_at.elapsed();
        let mins = remaining.as_secs() / 60;
        BreakerCheck::Blocked(format!(
            "{} (circuit breaker: {} consecutive failures, next retry in {}m)",
            entry.last_message, entry.consecutive, mins,
        ))
    }

    /// Record a successful execution — resets the breaker for this resource.
    pub fn record_success(&mut self, resource_key: &str) {
        self.entries.remove(resource_key);
    }

    /// Record a failed execution. If the error is an infrastructure class and
    /// the consecutive count reaches the threshold, trips the breaker.
    /// Returns `true` if the breaker just tripped (caller should change action to Blocked).
    pub fn record_failure(
        &mut self,
        resource_key: &str,
        class: &ErrorClass,
        message: &str,
    ) -> bool {
        if !class.is_infrastructure() {
            // Non-infrastructure errors don't participate in the breaker.
            // Reset the counter so a mix of error types doesn't trip it.
            self.entries.remove(resource_key);
            return false;
        }

        let entry = self.entries
            .entry(resource_key.to_string())
            .or_insert_with(|| BreakerEntry {
                consecutive: 0,
                last_class: class.clone(),
                last_message: String::new(),
                tripped_at: None,
            });

        entry.consecutive += 1;
        entry.last_class = class.clone();
        entry.last_message = message.to_string();

        if entry.consecutive >= BREAKER_THRESHOLD && entry.tripped_at.is_none() {
            entry.tripped_at = Some(Instant::now());
            return true;
        }

        false
    }

    /// Reset the cooldown timer for a breaker entry (e.g., after a retry
    /// during the cooldown window that still fails).
    pub fn reset_cooldown(&mut self, resource_key: &str) {
        if let Some(entry) = self.entries.get_mut(resource_key) {
            if entry.tripped_at.is_some() {
                entry.tripped_at = Some(Instant::now());
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn allows_first_failure() {
        let mut breaker = PackageBreaker::new();
        let tripped = breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        assert!(!tripped);
        assert!(matches!(breaker.check("nginx"), BreakerCheck::Allow));
    }

    #[test]
    fn trips_after_threshold() {
        let mut breaker = PackageBreaker::new();
        for i in 0..BREAKER_THRESHOLD {
            let tripped =
                breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
            if i < BREAKER_THRESHOLD - 1 {
                assert!(!tripped);
            } else {
                assert!(tripped);
            }
        }
        assert!(matches!(breaker.check("nginx"), BreakerCheck::Blocked(_)));
    }

    #[test]
    fn success_resets_breaker() {
        let mut breaker = PackageBreaker::new();
        breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        breaker.record_success("nginx");
        assert!(matches!(breaker.check("nginx"), BreakerCheck::Allow));
    }

    #[test]
    fn non_infrastructure_resets() {
        let mut breaker = PackageBreaker::new();
        breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        // A dependency conflict is not infrastructure — resets the counter.
        let tripped =
            breaker.record_failure("nginx", &ErrorClass::DependencyConflict, "conflict");
        assert!(!tripped);
        // Back to repo errors — counter starts over.
        let tripped = breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        assert!(!tripped); // only 1 consecutive, not at threshold yet
    }

    #[test]
    fn independent_resources() {
        let mut breaker = PackageBreaker::new();
        for _ in 0..BREAKER_THRESHOLD {
            breaker.record_failure("nginx", &ErrorClass::RepoError, "repo 404");
        }
        // nginx is blocked, but curl is not.
        assert!(matches!(breaker.check("nginx"), BreakerCheck::Blocked(_)));
        assert!(matches!(breaker.check("curl"), BreakerCheck::Allow));
    }
}
