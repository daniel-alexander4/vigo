// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Cached trait collection with volatile/stable split. Stable collectors
//! run every N cycles to avoid expensive operations (e.g. package listing)
//! on every check-in.

use super::{collect_all, stable_collectors, volatile_collectors, CollectorKind};
use serde_json::Value;
use std::sync::Mutex;

pub struct CachedCollectors {
    volatile: Vec<CollectorKind>,
    stable: Vec<CollectorKind>,
    inner: Mutex<CacheState>,
}

struct CacheState {
    cached: Option<serde_json::Map<String, Value>>,
    cycle: usize,
    interval: usize,
}

impl CachedCollectors {
    /// Create a new CachedCollectors that refreshes stable collectors every
    /// `stable_interval` cycles.
    pub fn new(stable_interval: usize) -> Self {
        Self {
            volatile: volatile_collectors(),
            stable: stable_collectors(),
            inner: Mutex::new(CacheState {
                cached: None,
                cycle: 0,
                interval: stable_interval.max(1),
            }),
        }
    }

    /// Collect all traits. Volatile collectors run every call. Stable collectors
    /// run on the first call and every N calls, reusing cached results otherwise.
    pub async fn collect_all(&self) -> Value {
        let (run_stable, collectors) = {
            let mut state = self.inner.lock().unwrap();
            let run_stable = state.cached.is_none() || state.cycle % state.interval == 0;
            state.cycle += 1;

            let mut collectors = self.volatile.clone();
            if run_stable {
                collectors.extend_from_slice(&self.stable);
            }
            (run_stable, collectors)
        };

        let collected = collect_all(&collectors).await;

        let mut state = self.inner.lock().unwrap();

        // Update stable cache with fresh results.
        if run_stable {
            let mut cache = serde_json::Map::new();
            if let Value::Object(ref map) = collected {
                for c in &self.stable {
                    if let Some(v) = map.get(c.name()) {
                        cache.insert(c.name().to_string(), v.clone());
                    }
                }
            }
            state.cached = Some(cache);
        }

        // Merge volatile results with cached stable results.
        let mut result = serde_json::Map::new();
        if let Value::Object(map) = collected {
            for (k, v) in map {
                result.insert(k, v);
            }
        }
        if let Some(ref cached) = state.cached {
            for (k, v) in cached {
                result.entry(k.clone()).or_insert_with(|| v.clone());
            }
        }

        Value::Object(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_cached_collectors_first_cycle_runs_all() {
        let cc = CachedCollectors::new(5);
        let result = cc.collect_all().await;
        assert!(result.is_object());
    }

    #[tokio::test]
    async fn test_cached_collectors_stable_cached() {
        let cc = CachedCollectors::new(5);

        // First call collects everything.
        let _ = cc.collect_all().await;

        // Second call should reuse stable cache.
        let result = cc.collect_all().await;
        assert!(result.is_object());

        // Verify cycle count advanced.
        let state = cc.inner.lock().unwrap();
        assert_eq!(state.cycle, 2);
        assert!(state.cached.is_some());
    }
}
