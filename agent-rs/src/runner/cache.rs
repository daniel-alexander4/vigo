// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Result cache — skips redundant executor calls for unchanged resources.
//!
//! Only no-op results (no drift, no error) from cacheable resource types are
//! cached. Entries expire after a configurable number of cycles or when
//! resource params change.

use crate::executors::ResourceResult;
use std::collections::HashMap;

/// Cache entry for a single resource.
struct CacheEntry {
    params_hash: String,
    result: ResourceResult,
    age: usize,
}

/// Caches no-op resource results to skip redundant executor calls.
pub struct ResultCache {
    entries: HashMap<String, CacheEntry>,
    /// Tracks all resources that have been executed at least once (any type).
    /// Used by watch_secret to detect first-run resources.
    seen: std::collections::HashSet<String>,
    max_age: usize,
}

/// Resource types safe to cache (check-only, no side effects).
fn is_cacheable(res_type: &str) -> bool {
    matches!(
        res_type,
        "file" | "package" | "service" | "repository" | "archive"
    )
}

impl ResultCache {
    /// Create a cache that expires entries after `max_age` cycles.
    pub fn new(max_age: usize) -> Self {
        Self {
            entries: HashMap::new(),
            seen: std::collections::HashSet::new(),
            max_age: max_age.max(1),
        }
    }

    /// Look up a cached result. Returns Some if params match and entry hasn't expired.
    pub fn lookup(
        &mut self,
        name: &str,
        res_type: &str,
        params: &HashMap<String, String>,
    ) -> Option<ResourceResult> {
        if !is_cacheable(res_type) {
            return None;
        }

        let hash = hash_params(params);

        let entry = self.entries.get_mut(name)?;
        if entry.params_hash != hash || entry.age >= self.max_age {
            return None;
        }

        entry.age += 1;
        Some(entry.result.clone())
    }

    /// Returns true if a resource has been executed at least once in this
    /// cache instance's lifetime. Used by watch_secret to detect first run.
    pub fn has_run(&self, name: &str) -> bool {
        self.seen.contains(name)
    }

    /// Mark a resource as having been executed (regardless of cacheability).
    pub fn mark_seen(&mut self, name: &str) {
        self.seen.insert(name.to_string());
    }

    /// Store a no-op result for future cache hits.
    pub fn store(
        &mut self,
        name: &str,
        res_type: &str,
        params: &HashMap<String, String>,
        result: &ResourceResult,
    ) {
        if !is_cacheable(res_type) {
            return;
        }

        // Only cache no-op, error-free results.
        if result.changed || result.error.is_some() {
            self.entries.remove(name);
            return;
        }

        self.entries.insert(
            name.to_string(),
            CacheEntry {
                params_hash: hash_params(params),
                result: result.clone(),
                age: 0,
            },
        );
    }
}

/// Deterministic hash of resource params using FNV-1a (non-cryptographic).
fn hash_params(params: &HashMap<String, String>) -> String {
    let mut keys: Vec<&str> = params.keys().map(|k| k.as_str()).collect();
    keys.sort();

    // Simple FNV-1a 64-bit.
    let mut hash: u64 = 0xcbf29ce484222325;
    for key in &keys {
        for b in key.bytes() {
            hash ^= b as u64;
            hash = hash.wrapping_mul(0x100000001b3);
        }
        hash ^= b'=' as u64;
        hash = hash.wrapping_mul(0x100000001b3);
        for b in params[*key].bytes() {
            hash ^= b as u64;
            hash = hash.wrapping_mul(0x100000001b3);
        }
        hash ^= b'\n' as u64;
        hash = hash.wrapping_mul(0x100000001b3);
    }

    format!("{:016x}", hash)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::executors::Action;

    fn make_params(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    }

    fn noop_result() -> ResourceResult {
        ResourceResult {
            module_name: String::new(),
            resource_type: "file".to_string(),
            resource_name: "test".to_string(),
            action: Action::None,
            changed: false,
            error: None,
            duration_ms: 0,
            ..Default::default()
        }
    }

    #[test]
    fn hit_on_noop() {
        let mut c = ResultCache::new(5);
        let params = make_params(&[("path", "/etc/app.conf"), ("content", "hello")]);
        let result = noop_result();

        c.store("config-file", "file", &params, &result);
        let cached = c.lookup("config-file", "file", &params);
        assert!(cached.is_some());
        assert_eq!(cached.unwrap().action, Action::None);
    }

    #[test]
    fn miss_on_params_change() {
        let mut c = ResultCache::new(5);
        let params1 = make_params(&[("path", "/etc/app.conf"), ("content", "hello")]);
        let params2 = make_params(&[("path", "/etc/app.conf"), ("content", "world")]);

        c.store("config-file", "file", &params1, &noop_result());
        assert!(c.lookup("config-file", "file", &params2).is_none());
    }

    #[test]
    fn miss_on_changed() {
        let mut c = ResultCache::new(5);
        let params = make_params(&[("path", "/etc/app.conf")]);
        let mut result = noop_result();
        result.action = Action::Updated;
        result.changed = true;

        c.store("config-file", "file", &params, &result);
        assert!(c.lookup("config-file", "file", &params).is_none());
    }

    #[test]
    fn miss_on_error() {
        let mut c = ResultCache::new(5);
        let params = make_params(&[("path", "/etc/app.conf")]);
        let mut result = noop_result();
        result.error = Some("fail".into());

        c.store("config-file", "file", &params, &result);
        assert!(c.lookup("config-file", "file", &params).is_none());
    }

    #[test]
    fn expires_after_max_age() {
        let mut c = ResultCache::new(3);
        let params = make_params(&[("path", "/etc/app.conf")]);

        c.store("config-file", "file", &params, &noop_result());

        // 3 hits (age 0→1, 1→2, 2→3).
        for _ in 0..3 {
            assert!(c.lookup("config-file", "file", &params).is_some());
        }
        // 4th: age=3 == max_age, miss.
        assert!(c.lookup("config-file", "file", &params).is_none());
    }

    #[test]
    fn non_cacheable_type() {
        let mut c = ResultCache::new(5);
        let params = make_params(&[("command", "echo hello")]);

        c.store("run-cmd", "exec", &params, &noop_result());
        assert!(c.lookup("run-cmd", "exec", &params).is_none());
    }

    #[test]
    fn evicts_on_drift() {
        let mut c = ResultCache::new(5);
        let params = make_params(&[("path", "/etc/app.conf")]);

        c.store("config-file", "file", &params, &noop_result());
        assert!(c.lookup("config-file", "file", &params).is_some());

        // Drift evicts.
        let mut changed = noop_result();
        changed.action = Action::Updated;
        changed.changed = true;
        c.store("config-file", "file", &params, &changed);

        assert!(c.lookup("config-file", "file", &params).is_none());
    }

    #[test]
    fn hash_deterministic() {
        let p1 = make_params(&[("b", "2"), ("a", "1")]);
        let p2 = make_params(&[("a", "1"), ("b", "2")]);
        assert_eq!(hash_params(&p1), hash_params(&p2));
    }
}
