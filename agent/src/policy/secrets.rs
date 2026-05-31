// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Bundle-level placeholder substitution for the agent.
//!
//! Server-side, the config loader emits `__VIGO_SECRET__<key>__END_VIGO_SECRET__`
//! markers wherever a `secret:<key>` reference appeared in a resource
//! attribute or var. The bundle carries those markers across the wire —
//! the agent can't apply them as-is (a placeholder string written into a
//! config file is a catastrophic failure mode). This module is the one
//! place that walks the bundle, fetches the placeholder values via the
//! server's `VigoAgent.ResolveSecrets` RPC, and substitutes them in
//! place before eval_bundle's template renderer sees the values.
//!
//! Substituting BEFORE template render lets the existing eval pipeline
//! work unchanged — `{{.Vars.dbPass}}` in a template renders the
//! already-resolved value, exactly as it did before the placeholder
//! transition. Only this module needs to know placeholders exist.
//!
//! In-memory cache only (v1). Resolved values are keyed by ref and
//! invalidated when the bundle's `secrets_epoch` advances. Persistent
//! at-rest caching is tracked separately in pending — the present
//! design needs the values for the duration of one apply cycle, not
//! across agent restarts. A cold restart (or a server outage at cold
//! start) cannot apply secret-bearing resources until contact returns.
//!
//! The cache holds plaintext, as do the many transient copies this module
//! and the downstream eval/apply pipeline make (vars_json, attribute
//! strings, rendered templates, exec args) — so the boundary that keeps
//! resolved secrets off disk is the process itself, not any single
//! allocation. The agent disables core dumps and marks itself non-dumpable
//! at startup (`platform::process::harden_process_memory`) so a crash or a
//! same-uid debugger can't capture that plaintext.
//!
//! Post-substitution assertion: after substitution every attribute /
//! vars_json string is re-scanned for the begin marker. Any survivor
//! fails the call (caller should abort the apply rather than write a
//! placeholder string into a file or command line).

use crate::proto;
use anyhow::{anyhow, bail, Result};
use serde_json::Value;
use std::collections::{HashMap, HashSet};
use std::sync::{Mutex, OnceLock};

const PLACEHOLDER_BEGIN: &str = "__VIGO_SECRET__";
const PLACEHOLDER_END: &str = "__END_VIGO_SECRET__";

/// global_cache returns the process-wide SecretCache. One cache per
/// agent process keeps placeholder resolves coherent across every code
/// path that processes a PolicyBundle (the main check-in loop, the
/// proxied-device path, bootstrap-time apply). Cache invalidation
/// rides the `secrets_epoch` advancing — see
/// `SecretCache::invalidate_if_epoch_changed`.
pub fn global_cache() -> &'static SecretCache {
    static CACHE: OnceLock<SecretCache> = OnceLock::new();
    CACHE.get_or_init(SecretCache::new)
}

/// SecretCache holds resolved placeholder values for one agent process.
/// Cleared (atomically) when the bundle's `secrets_epoch` bumps.
#[derive(Default)]
pub struct SecretCache {
    inner: Mutex<SecretCacheInner>,
}

#[derive(Default)]
struct SecretCacheInner {
    epoch: i64,
    values: HashMap<String, String>,
}

impl SecretCache {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn invalidate_if_epoch_changed(&self, observed_epoch: i64) {
        let mut g = self.inner.lock().unwrap();
        if g.epoch != observed_epoch {
            g.values.clear();
            g.epoch = observed_epoch;
        }
    }

    fn missing_refs<'a>(&self, refs: impl IntoIterator<Item = &'a str>) -> Vec<String> {
        let g = self.inner.lock().unwrap();
        refs.into_iter()
            .filter(|r| !g.values.contains_key(*r))
            .map(|r| r.to_string())
            .collect()
    }

    /// missing_refs_pub is the public form used by converge_local to
    /// check whether every placeholder ref in a cached bundle is
    /// satisfied by the in-memory cache before applying.
    pub fn missing_refs_pub<'a>(&self, refs: impl IntoIterator<Item = &'a str>) -> Vec<String> {
        self.missing_refs(refs)
    }

    fn extend(&self, values: impl IntoIterator<Item = (String, String)>) {
        let mut g = self.inner.lock().unwrap();
        g.values.extend(values);
    }

    fn get(&self, ref_: &str) -> Option<String> {
        let g = self.inner.lock().unwrap();
        g.values.get(ref_).cloned()
    }
}

/// resolve_credential substitutes a single placeholder-bearing string
/// (e.g. ProxiedDevice.Credential) and returns the resolved value. The
/// agent calls this for the small handful of bundle-adjacent strings
/// that aren't inside the PolicyBundle itself but still ship as
/// placeholders. Non-placeholder input is returned unchanged.
pub async fn resolve_credential(
    raw: &str,
    cache: &SecretCache,
    client: &mut crate::checkin::client::Client,
    epoch: i64,
) -> Result<String> {
    if !raw.contains(PLACEHOLDER_BEGIN) {
        return Ok(raw.to_string());
    }
    cache.invalidate_if_epoch_changed(epoch);
    let mut refs = HashSet::new();
    scan_into(raw, &mut refs);
    let missing = cache.missing_refs(refs.iter().map(|s| s.as_str()));
    if !missing.is_empty() {
        let resp = client.resolve_secrets(missing).await?;
        cache.extend(resp.values);
    }
    let out = substitute_in_string(raw, cache)?;
    if out.contains(PLACEHOLDER_BEGIN) {
        bail!("placeholder survived substitution in credential — refusing to use");
    }
    Ok(out)
}

/// resolve_and_substitute is the integration point check-in call sites
/// use. Walks `bundle` for placeholder markers in resource attributes +
/// vars_json + module-level templates, fetches missing values via the
/// server's ResolveSecrets RPC, populates the cache, then substitutes
/// in place. After this returns, the bundle's strings are placeholder-
/// free and safe to hand to `eval_bundle`.
pub async fn resolve_and_substitute(
    bundle: &mut proto::PolicyBundle,
    cache: &SecretCache,
    client: &mut crate::checkin::client::Client,
) -> Result<()> {
    cache.invalidate_if_epoch_changed(bundle.secrets_epoch);

    let refs = collect_refs_in_bundle(bundle);
    if refs.is_empty() {
        return Ok(());
    }

    let missing = cache.missing_refs(refs.iter().map(|s| s.as_str()));
    if !missing.is_empty() {
        let resp = client.resolve_secrets(missing).await?;
        cache.extend(resp.values);
    }

    substitute_in_bundle(bundle, cache)
}

/// collect_refs_in_bundle_pub is the public form used by the offline
/// reconvergence path: it scans the bundle's placeholder refs WITHOUT
/// making any RPC, so converge_local can check "are all my placeholder
/// secrets in the in-memory cache" before deciding to apply.
pub fn collect_refs_in_bundle_pub(bundle: &proto::PolicyBundle) -> HashSet<String> {
    collect_refs_in_bundle(bundle)
}

/// substitute_in_bundle_pub is the public form used by the offline
/// reconvergence path — same semantics as the internal walker but
/// reachable from converge_local once the caller has confirmed every
/// placeholder ref is satisfied by the in-memory cache.
pub fn substitute_in_bundle_pub(bundle: &mut proto::PolicyBundle, cache: &SecretCache) -> Result<()> {
    substitute_in_bundle(bundle, cache)
}

/// collect_refs_in_bundle walks every placeholder-bearing string in the
/// bundle and returns the set of secret keys that need substituting.
fn collect_refs_in_bundle(bundle: &proto::PolicyBundle) -> HashSet<String> {
    let mut refs = HashSet::new();
    if !bundle.vars_json.is_empty() {
        if let Ok(v) = serde_json::from_slice::<Value>(&bundle.vars_json) {
            scan_value(&v, &mut refs);
        }
    }
    for m in &bundle.modules {
        scan_into(&m.when, &mut refs);
        for r in &m.resources {
            scan_into(&r.when, &mut refs);
            for v in r.attributes.values() {
                scan_into(v, &mut refs);
            }
        }
    }
    refs
}

/// substitute_in_bundle walks the bundle and replaces every placeholder
/// marker. After the walk every visited string is re-scanned; any
/// survivor fails the call.
fn substitute_in_bundle(bundle: &mut proto::PolicyBundle, cache: &SecretCache) -> Result<()> {
    if !bundle.vars_json.is_empty() {
        let mut v: Value = serde_json::from_slice(&bundle.vars_json)
            .map_err(|e| anyhow!("parsing vars_json for substitution: {e}"))?;
        substitute_in_value(&mut v, cache)?;
        bundle.vars_json = serde_json::to_vec(&v)
            .map_err(|e| anyhow!("re-marshaling vars_json after substitution: {e}"))?;
        if bundle.vars_json.windows(PLACEHOLDER_BEGIN.len()).any(|w| w == PLACEHOLDER_BEGIN.as_bytes()) {
            bail!("placeholder survived substitution in vars_json — refusing to apply");
        }
    }
    for m in bundle.modules.iter_mut() {
        if m.when.contains(PLACEHOLDER_BEGIN) {
            m.when = substitute_in_string(&m.when, cache)?;
            if m.when.contains(PLACEHOLDER_BEGIN) {
                bail!(
                    "placeholder survived substitution in module {}.when — refusing to apply",
                    m.name
                );
            }
        }
        for r in m.resources.iter_mut() {
            if r.when.contains(PLACEHOLDER_BEGIN) {
                r.when = substitute_in_string(&r.when, cache)?;
                if r.when.contains(PLACEHOLDER_BEGIN) {
                    bail!(
                        "placeholder survived substitution in resource {}.{}.when — refusing to apply",
                        m.name,
                        r.name
                    );
                }
            }
            for (k, v) in r.attributes.iter_mut() {
                if !v.contains(PLACEHOLDER_BEGIN) {
                    continue;
                }
                *v = substitute_in_string(v, cache)?;
                if v.contains(PLACEHOLDER_BEGIN) {
                    bail!(
                        "placeholder survived substitution in {}.{}.{} — refusing to apply",
                        m.name,
                        r.name,
                        k
                    );
                }
            }
        }
    }
    Ok(())
}

fn scan_value(v: &Value, out: &mut HashSet<String>) {
    match v {
        Value::String(s) => scan_into(s, out),
        Value::Array(arr) => arr.iter().for_each(|item| scan_value(item, out)),
        Value::Object(map) => map.values().for_each(|item| scan_value(item, out)),
        _ => {}
    }
}

fn substitute_in_value(v: &mut Value, cache: &SecretCache) -> Result<()> {
    match v {
        Value::String(s) => {
            if s.contains(PLACEHOLDER_BEGIN) {
                *s = substitute_in_string(s, cache)?;
            }
        }
        Value::Array(arr) => {
            for item in arr.iter_mut() {
                substitute_in_value(item, cache)?;
            }
        }
        Value::Object(map) => {
            for item in map.values_mut() {
                substitute_in_value(item, cache)?;
            }
        }
        _ => {}
    }
    Ok(())
}

fn scan_into(s: &str, out: &mut HashSet<String>) {
    let mut i = 0;
    while let Some(rel) = s[i..].find(PLACEHOLDER_BEGIN) {
        let key_start = i + rel + PLACEHOLDER_BEGIN.len();
        let key_end = match s[key_start..].find(PLACEHOLDER_END) {
            Some(k) => key_start + k,
            None => return,
        };
        out.insert(s[key_start..key_end].to_string());
        i = key_end + PLACEHOLDER_END.len();
    }
}

fn substitute_in_string(s: &str, cache: &SecretCache) -> Result<String> {
    let mut out = String::with_capacity(s.len());
    let mut i = 0;
    loop {
        let rel = match s[i..].find(PLACEHOLDER_BEGIN) {
            Some(r) => r,
            None => {
                out.push_str(&s[i..]);
                return Ok(out);
            }
        };
        out.push_str(&s[i..i + rel]);
        let key_start = i + rel + PLACEHOLDER_BEGIN.len();
        let key_end = match s[key_start..].find(PLACEHOLDER_END) {
            Some(k) => key_start + k,
            None => {
                out.push_str(&s[i + rel..]);
                return Ok(out);
            }
        };
        let key = &s[key_start..key_end];
        let value = cache
            .get(key)
            .ok_or_else(|| anyhow!("secret {key:?} not in cache after resolve"))?;
        out.push_str(&value);
        i = key_end + PLACEHOLDER_END.len();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::{BTreeMap, HashMap};

    fn make_bundle(vars_json: &str, attrs: Vec<(&str, &str)>) -> proto::PolicyBundle {
        let mut attributes = BTreeMap::new();
        for (k, v) in attrs {
            attributes.insert(k.to_string(), v.to_string());
        }
        proto::PolicyBundle {
            secrets_epoch: 1,
            vars_json: vars_json.as_bytes().to_vec(),
            modules: vec![proto::PolicyModule {
                name: "m".to_string(),
                resources: vec![proto::PolicyResource {
                    name: "r".to_string(),
                    r#type: "file".to_string(),
                    attributes,
                    when: String::new(),
                    notify: vec![],
                    subscribes: vec![],
                    watch_secret: vec![],
                }],
                ..Default::default()
            }],
            ..Default::default()
        }
    }

    #[test]
    fn collect_refs_walks_vars_and_attributes() {
        let bundle = make_bundle(
            r#"{"dbPass":"__VIGO_SECRET__vigo/db/pw__END_VIGO_SECRET__"}"#,
            vec![(
                "content",
                "__VIGO_SECRET__tls/server.key__END_VIGO_SECRET__",
            )],
        );
        let refs = collect_refs_in_bundle(&bundle);
        assert_eq!(refs.len(), 2);
        assert!(refs.contains("vigo/db/pw"));
        assert!(refs.contains("tls/server.key"));
    }

    /// Regression guard for the ordering invariant that
    /// `agent/src/checkin/loop_impl.rs::run_once_with_update` relies on:
    /// verify_bundle must run on the wire-bundle bytes *before*
    /// substitute_in_bundle mutates them. Substitution rewrites signed
    /// fields (vars_json byte representation + module/resource attribute
    /// values), so a signature computed over the pre-substitution bundle
    /// will not verify against the post-substitution bundle.
    ///
    /// Pre-fix history: 0.68.10 added substitute_in_bundle without moving
    /// the existing verify_bundle call out of its post-substitute spot in
    /// the daemon loop. danlap-only bundle-rejection loop on 2026-05-26
    /// (see tshoot/log.md). This test fails on the broken order and
    /// passes on the fixed one.
    #[test]
    fn substitute_invalidates_bundle_signature() {
        use crate::checkin::verify::verify_bundle;
        use ed25519_dalek::{Signer, SigningKey};
        use spki::EncodePublicKey;

        // Build a bundle with a real placeholder in an attribute (verifies
        // the substitution actually mutates a signed field).
        let mut bundle = make_bundle(
            r#"{"flag":"plain"}"#,
            vec![("pw", "__VIGO_SECRET__vigo/db/pw__END_VIGO_SECRET__")],
        );

        // Sign the unmutated bundle. The Rust agent's marshal_bundle_content
        // is private to checkin::verify; we mirror its shape here for the
        // signing step. Drift between this helper and the real
        // marshal_bundle_content would surface as `verify_bundle == Err`
        // on the pre-substitution assertion below.
        let signing = SigningKey::generate(&mut rand::rngs::OsRng);
        let pem = signing
            .verifying_key()
            .to_public_key_pem(pkcs8::LineEnding::LF)
            .unwrap();
        let content = {
            use prost::Message;
            let c = proto::PolicyBundle {
                policy_version: bundle.policy_version,
                modules: bundle.modules.clone(),
                vars_json: bundle.vars_json.clone(),
                var_order: bundle.var_order.clone(),
                custom_traits: bundle.custom_traits.clone(),
                matched_pattern: bundle.matched_pattern.clone(),
                environment: bundle.environment.clone(),
                roles: bundle.roles.clone(),
                merkle_root: bundle.merkle_root.clone(),
                ..Default::default()
            };
            c.encode_to_vec()
        };
        bundle.signature = signing.sign(&content).to_bytes().to_vec();
        bundle.server_pubkey_id = "test".into();

        // Verify succeeds on the pre-substitution bundle.
        verify_bundle(&bundle, &pem).expect("pre-substitution verify should succeed");

        // Populate the cache and substitute.
        let cache = SecretCache::new();
        cache.extend(HashMap::from([(
            "vigo/db/pw".to_string(),
            "s3cret".to_string(),
        )]));
        substitute_in_bundle(&mut bundle, &cache).unwrap();
        assert_eq!(
            bundle.modules[0].resources[0].attributes["pw"],
            "s3cret",
            "substitution should have happened"
        );

        // Verify must now fail — substitute mutated a signed field.
        verify_bundle(&bundle, &pem)
            .expect_err("post-substitution verify must fail; otherwise the signed-field set has drifted from what substitute mutates");
    }

    #[test]
    fn substitute_replaces_in_vars_and_attributes() {
        let cache = SecretCache::new();
        cache.extend(HashMap::from([
            ("vigo/db/pw".to_string(), "s3cret".to_string()),
            ("tls/server.key".to_string(), "KEY-BYTES".to_string()),
        ]));
        let mut bundle = make_bundle(
            r#"{"dbPass":"__VIGO_SECRET__vigo/db/pw__END_VIGO_SECRET__"}"#,
            vec![(
                "content",
                "cert=__VIGO_SECRET__tls/server.key__END_VIGO_SECRET__",
            )],
        );
        substitute_in_bundle(&mut bundle, &cache).unwrap();
        let vars: Value = serde_json::from_slice(&bundle.vars_json).unwrap();
        assert_eq!(vars["dbPass"], "s3cret");
        assert_eq!(
            bundle.modules[0].resources[0].attributes["content"],
            "cert=KEY-BYTES"
        );
    }

    #[test]
    fn post_assert_catches_missing_cache_entry() {
        let cache = SecretCache::new();
        let mut bundle = make_bundle(
            "{}",
            vec![(
                "content",
                "__VIGO_SECRET__vigo/db/pw__END_VIGO_SECRET__",
            )],
        );
        let err = substitute_in_bundle(&mut bundle, &cache).unwrap_err();
        assert!(err.to_string().contains("not in cache"));
    }

    #[test]
    fn cache_invalidates_on_epoch_bump() {
        let cache = SecretCache::new();
        cache.invalidate_if_epoch_changed(1);
        cache.extend(HashMap::from([("k".to_string(), "v".to_string())]));
        assert_eq!(cache.get("k").as_deref(), Some("v"));
        cache.invalidate_if_epoch_changed(2);
        assert!(cache.get("k").is_none());
    }
}
