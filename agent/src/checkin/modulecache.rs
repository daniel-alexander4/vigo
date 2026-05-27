// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Module cache for delta policy transfer.
//!
//! Stores PolicyModules by name with content hashes. When the server sends a
//! stub module (content_hash only, no resources), the agent fills it from cache.
//! This avoids retransmitting unchanged modules on every check-in.

use crate::proto::PolicyModule;
use std::collections::{BTreeMap, HashMap};
use tracing::warn;

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
    /// Returns a BTreeMap so prost's encode_to_vec iterates in sorted
    /// key order — required for deterministic proto map encoding that
    /// matches the server's `MarshalOptions{Deterministic: true}`.
    pub fn hashes(&self) -> Option<BTreeMap<String, String>> {
        if self.modules.is_empty() {
            return None;
        }
        let mut h = BTreeMap::new();
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
    ///
    /// Stub modules (`m.stub == true`, no resources) are **never cached** —
    /// only modules carrying real content. Caching a stub would make the
    /// agent report its hash in `cached_module_hashes` on the next check-in,
    /// the server would reply with a stub again ("you already have it"),
    /// `merge_stubs` would "fill" the stub from the cached stub, and the
    /// envoy would be permanently stuck applying zero resources. Skipping
    /// stubs here is the load-bearing line: an unfilled stub falls out of
    /// the cache, so the next check-in omits its hash and the server sends
    /// it full. (A module that reappears as a stub keeps its prior full
    /// copy in the cache — the stub doesn't overwrite it.)
    pub fn update(&mut self, modules: &[PolicyModule]) {
        let mut active: std::collections::HashSet<&str> =
            std::collections::HashSet::with_capacity(modules.len());

        for m in modules {
            active.insert(&m.name);
            if !m.content_hash.is_empty() && !m.stub {
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

/// Fill stub modules from the cache. A stub the cache can't fill is returned
/// as-is (empty resources) and warned about — the module has no content this
/// cycle; `update` won't cache the stub, so the next check-in won't report
/// its hash and the server re-sends it full, closing the gap within two
/// cycles. (Only ever fills a stub from a *full* cached module — `update`
/// never caches stubs, so `cache.get` can't return one.)
pub fn merge_stubs(modules: Vec<PolicyModule>, cache: &ModuleCache) -> Vec<PolicyModule> {
    modules
        .into_iter()
        .map(|m| {
            if m.stub {
                if let Some(cached) = cache.get(&m.name, &m.content_hash) {
                    return cached.clone();
                }
                warn!(
                    module = %m.name,
                    content_hash = %m.content_hash,
                    "delta-transfer stub not in module cache — no content this cycle; next check-in re-fetches it full"
                );
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
        c.update(std::slice::from_ref(&full));

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

    #[test]
    fn update_skips_stub_modules() {
        // A stub carries a content_hash but no resources. Caching it would
        // poison the delta-transfer loop (server sends stub → "fill" with
        // cached stub → 0 resources, forever). It must not be cached.
        let mut c = ModuleCache::new();
        c.update(&[make_module("nginx", "abc", true)]);
        assert!(c.hashes().is_none(), "stub must not appear in cached hashes");
        assert!(c.get("nginx", "abc").is_none(), "stub must not be cached");
    }

    #[test]
    fn update_keeps_prior_full_when_module_reappears_as_stub() {
        // First cycle: full module, cached. Next cycle the server sends a
        // stub for it (delta transfer). update() with that stub must leave
        // the prior full copy in place so merge_stubs can still fill it.
        let mut c = ModuleCache::new();
        c.update(&[make_module("nginx", "abc", false)]);
        c.update(&[make_module("nginx", "abc", true)]);

        let cached = c.get("nginx", "abc").expect("prior full module retained");
        assert!(!cached.stub);
        assert!(!cached.resources.is_empty());

        let merged = merge_stubs(vec![make_module("nginx", "abc", true)], &c);
        assert!(!merged[0].stub, "stub filled from the retained full copy");
        assert!(!merged[0].resources.is_empty());
    }
}
