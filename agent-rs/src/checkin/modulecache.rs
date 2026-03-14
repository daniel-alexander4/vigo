// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

//! Module cache for delta policy transfer.
//!
//! Stores PolicyModules by name with content hashes. When the server sends a
//! stub module (content_hash only, no resources), the agent fills it from cache.
//! This avoids retransmitting unchanged modules on every check-in.

use crate::proto::PolicyModule;
use std::collections::HashMap;

struct CachedModule {
    hash: String,
    module: PolicyModule,
}

pub struct ModuleCache {
    modules: HashMap<String, CachedModule>,
}

impl ModuleCache {
    pub fn new() -> Self {
        Self {
            modules: HashMap::new(),
        }
    }

    /// Returns module_name → content_hash for all cached modules.
    /// Sent to the server in CheckInRequest.cached_module_hashes.
    pub fn hashes(&self) -> Option<HashMap<String, String>> {
        if self.modules.is_empty() {
            return None;
        }
        let mut h = HashMap::with_capacity(self.modules.len());
        for (name, cm) in &self.modules {
            h.insert(name.clone(), cm.hash.clone());
        }
        Some(h)
    }

    /// Get a cached module if name and hash match.
    pub fn get(&self, name: &str, hash: &str) -> Option<&PolicyModule> {
        let cm = self.modules.get(name)?;
        if cm.hash == hash {
            Some(&cm.module)
        } else {
            None
        }
    }

    /// Update the cache with fully resolved modules. Evicts modules no longer
    /// in the bundle (role changed, etc.).
    pub fn update(&mut self, modules: &[PolicyModule]) {
        let mut active: std::collections::HashSet<&str> =
            std::collections::HashSet::with_capacity(modules.len());

        for m in modules {
            active.insert(&m.name);
            if !m.content_hash.is_empty() {
                self.modules.insert(
                    m.name.clone(),
                    CachedModule {
                        hash: m.content_hash.clone(),
                        module: m.clone(),
                    },
                );
            }
        }

        self.modules.retain(|name, _| active.contains(name.as_str()));
    }
}

/// Fill stub modules from the cache. Stubs whose hash doesn't match are
/// returned as-is (empty resources, harmless).
pub fn merge_stubs(modules: Vec<PolicyModule>, cache: &ModuleCache) -> Vec<PolicyModule> {
    modules
        .into_iter()
        .map(|m| {
            if m.stub {
                if let Some(cached) = cache.get(&m.name, &m.content_hash) {
                    return cached.clone();
                }
            }
            m
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_module(name: &str, hash: &str, stub: bool) -> PolicyModule {
        PolicyModule {
            name: name.to_string(),
            content_hash: hash.to_string(),
            stub,
            resources: if stub {
                vec![]
            } else {
                vec![crate::proto::PolicyResource {
                    name: "res1".into(),
                    r#type: "file".into(),
                    attributes: Default::default(),
                    ..Default::default()
                }]
            },
            ..Default::default()
        }
    }

    #[test]
    fn empty_cache_returns_none_hashes() {
        let c = ModuleCache::new();
        assert!(c.hashes().is_none());
    }

    #[test]
    fn update_and_hashes() {
        let mut c = ModuleCache::new();
        let modules = vec![make_module("nginx", "abc123", false)];
        c.update(&modules);

        let h = c.hashes().unwrap();
        assert_eq!(h.get("nginx").unwrap(), "abc123");
    }

    #[test]
    fn get_hit_and_miss() {
        let mut c = ModuleCache::new();
        c.update(&[make_module("nginx", "abc", false)]);

        assert!(c.get("nginx", "abc").is_some());
        assert!(c.get("nginx", "wrong").is_none());
        assert!(c.get("other", "abc").is_none());
    }

    #[test]
    fn evicts_removed_modules() {
        let mut c = ModuleCache::new();
        c.update(&[
            make_module("nginx", "a", false),
            make_module("redis", "b", false),
        ]);
        assert_eq!(c.modules.len(), 2);

        // Update with only nginx — redis should be evicted.
        c.update(&[make_module("nginx", "a", false)]);
        assert_eq!(c.modules.len(), 1);
        assert!(c.get("redis", "b").is_none());
    }

    #[test]
    fn merge_stubs_fills_from_cache() {
        let mut c = ModuleCache::new();
        let full = make_module("nginx", "abc", false);
        c.update(&[full.clone()]);

        let stub = make_module("nginx", "abc", true);
        let merged = merge_stubs(vec![stub], &c);

        assert_eq!(merged.len(), 1);
        assert!(!merged[0].stub);
        assert!(!merged[0].resources.is_empty());
    }

    #[test]
    fn merge_stubs_cache_miss() {
        let c = ModuleCache::new();
        let stub = make_module("nginx", "abc", true);
        let merged = merge_stubs(vec![stub], &c);

        assert_eq!(merged.len(), 1);
        assert!(merged[0].stub); // stays as stub
    }
}
