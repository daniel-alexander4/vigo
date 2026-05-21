// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Target spec matching for swarm self-assignment.
//!
//! Port of `server/swarm/target.go` `MatchTarget()`. Supports comma-separated
//! patterns with `-` prefix for exclusion, glob matching on hostnames, and
//! `key=value` trait matching. [`flatten_traits`] (a port of
//! `server/fleet/index.go::FlattenJSON`) turns this envoy's nested traits JSON
//! into the dotted-key map the `key=value` patterns match against, so the
//! agent's self-assignment and the server's `vigocli swarm filecast match`
//! count agree.

use std::collections::HashMap;

use serde_json::Value;

/// Check whether this agent matches a target spec.
///
/// Supports comma-separated patterns with `-` prefix for exclusion:
///   `"*"`                — all agents
///   `"*lap*"`            — hostname glob
///   `"*lap*,-rasp*"`     — include `*lap*`, exclude `rasp*`
///   `"os.distro=ubuntu"` — trait match
///
/// An agent matches if at least one inclusion pattern matches AND no
/// exclusion pattern matches. If only exclusion patterns are given,
/// all agents are included by default (then exclusions applied).
pub fn match_target(hostname: &str, traits: &HashMap<String, String>, spec: &str) -> bool {
    let mut includes = Vec::new();
    let mut excludes = Vec::new();

    for part in spec.split(',') {
        let p = part.trim();
        if p.is_empty() {
            continue;
        }
        if let Some(rest) = p.strip_prefix('-') {
            excludes.push(rest);
        } else {
            includes.push(p);
        }
    }

    // Check exclusions first — any match means reject.
    for pat in &excludes {
        if match_single(hostname, traits, pat) {
            return false;
        }
    }

    // If no inclusion patterns, default to include-all (exclusions-only mode).
    if includes.is_empty() {
        return true;
    }

    // At least one inclusion pattern must match.
    includes.iter().any(|pat| match_single(hostname, traits, pat))
}

/// Match a single pattern: glob, trait=value, or exact hostname.
fn match_single(hostname: &str, traits: &HashMap<String, String>, pat: &str) -> bool {
    if pat == "*" {
        return true;
    }
    if let Some((key, value)) = pat.split_once('=') {
        return traits.get(key).is_some_and(|v| v == value);
    }
    if glob_match(pat, hostname) {
        return true;
    }
    hostname == pat
}

/// Simple glob matching compatible with Go's `filepath.Match`.
/// Supports `*` (any sequence of non-separator chars) and `?` (any single char).
fn glob_match(pattern: &str, text: &str) -> bool {
    let mut pi = pattern.chars().peekable();
    let mut ti = text.chars().peekable();

    while pi.peek().is_some() || ti.peek().is_some() {
        match pi.peek() {
            Some('*') => {
                pi.next();
                // * at end matches everything.
                if pi.peek().is_none() {
                    return true;
                }
                // Try matching the rest of the pattern at every position.
                let remaining_pattern: String = pi.clone().collect();
                let remaining_text: String = ti.clone().collect();
                for i in 0..=remaining_text.len() {
                    if glob_match(&remaining_pattern, &remaining_text[i..]) {
                        return true;
                    }
                }
                return false;
            }
            Some('?') => {
                pi.next();
                if ti.next().is_none() {
                    return false;
                }
            }
            Some(&pc) => {
                pi.next();
                match ti.next() {
                    Some(tc) if tc == pc => {}
                    _ => return false,
                }
            }
            None => return false,
        }
    }
    true
}

/// Flatten a nested traits JSON value into the dotted-key
/// `HashMap<String, String>` that [`match_target`]'s `key=value` patterns
/// expect — a port of `server/fleet/index.go::FlattenJSON`. Nested objects
/// produce dotted keys (`os.distro`); arrays produce `key.0`, `key.1`, …;
/// string leaves and string array elements are stored verbatim; numbers,
/// bools, and null are JSON-stringified (`42` → `"42"`, `true` → `"true"`,
/// `null` → `"null"`), matching Go's `json.Marshal`.
pub fn flatten_traits(value: &Value) -> HashMap<String, String> {
    let mut out = HashMap::new();
    flatten_into("", value, &mut out);
    out
}

fn flatten_into(prefix: &str, value: &Value, out: &mut HashMap<String, String>) {
    let dotted = |key: &str| -> String {
        if prefix.is_empty() {
            key.to_string()
        } else {
            format!("{prefix}.{key}")
        }
    };
    match value {
        Value::Object(map) => {
            for (k, v) in map {
                flatten_into(&dotted(k), v, out);
            }
        }
        Value::Array(arr) => {
            for (i, v) in arr.iter().enumerate() {
                flatten_into(&dotted(&i.to_string()), v, out);
            }
        }
        Value::String(s) => {
            if !prefix.is_empty() {
                out.insert(prefix.to_string(), s.clone());
            }
        }
        // Number / Bool / Null — same as the server's json.Marshal fallback.
        other => {
            if !prefix.is_empty() {
                out.insert(prefix.to_string(), other.to_string());
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn traits(pairs: &[(&str, &str)]) -> HashMap<String, String> {
        pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect()
    }

    #[test]
    fn test_wildcard() {
        assert!(match_target("anything", &HashMap::new(), "*"));
    }

    #[test]
    fn test_exact_hostname() {
        assert!(match_target("web-01", &HashMap::new(), "web-01"));
        assert!(!match_target("web-02", &HashMap::new(), "web-01"));
    }

    #[test]
    fn test_glob() {
        assert!(match_target("girlslaptop", &HashMap::new(), "*lap*"));
        assert!(!match_target("raspberry", &HashMap::new(), "*lap*"));
    }

    #[test]
    fn test_exclusion() {
        assert!(match_target("girlslaptop", &HashMap::new(), "*lap*,-rasp*"));
        assert!(!match_target("raspberry", &HashMap::new(), "*,-rasp*"));
    }

    #[test]
    fn test_exclusion_only() {
        // No inclusions → all included, then exclusions applied.
        assert!(match_target("danlap", &HashMap::new(), "-rasp*"));
        assert!(!match_target("raspberry", &HashMap::new(), "-rasp*"));
    }

    #[test]
    fn test_trait_match() {
        let t = traits(&[("os.distro", "ubuntu")]);
        assert!(match_target("web-01", &t, "os.distro=ubuntu"));
        assert!(!match_target("web-01", &t, "os.distro=debian"));
    }

    #[test]
    fn test_combined() {
        let t = traits(&[("os.distro", "ubuntu")]);
        assert!(match_target("web-01", &t, "*web*,os.distro=ubuntu"));
        assert!(match_target("db-01", &t, "*web*,os.distro=ubuntu"));
        assert!(!match_target("db-01", &traits(&[("os.distro", "debian")]), "*web*,os.distro=ubuntu"));
    }

    #[test]
    fn test_empty_spec() {
        // Empty spec → no includes, no excludes → match all.
        assert!(match_target("anything", &HashMap::new(), ""));
    }

    #[test]
    fn test_glob_question_mark() {
        assert!(match_target("web-01", &HashMap::new(), "web-0?"));
        assert!(!match_target("web-01", &HashMap::new(), "web-?"));
    }

    #[test]
    fn test_flatten_traits() {
        let v = serde_json::json!({
            "os": {"family": "linux", "distro": "ubuntu"},
            "version": "22.04",
            "mem": {"total_bytes": 8589934592i64},
            "is_container": false,
            "users": {"human": ["dan", "ann"], "logged_in": []},
        });
        let f = flatten_traits(&v);
        assert_eq!(f.get("os.family").map(String::as_str), Some("linux"));
        assert_eq!(f.get("os.distro").map(String::as_str), Some("ubuntu"));
        assert_eq!(f.get("version").map(String::as_str), Some("22.04"));
        assert_eq!(f.get("mem.total_bytes").map(String::as_str), Some("8589934592"));
        assert_eq!(f.get("is_container").map(String::as_str), Some("false"));
        assert_eq!(f.get("users.human.0").map(String::as_str), Some("dan"));
        assert_eq!(f.get("users.human.1").map(String::as_str), Some("ann"));
        // Empty array contributes nothing.
        assert!(f.keys().all(|k| !k.starts_with("users.logged_in")));
        // A flattened trait map then drives match_target's `key=value` patterns.
        assert!(match_target("web-01", &f, "os.distro=ubuntu"));
        assert!(!match_target("web-01", &f, "os.distro=debian"));
    }
}
