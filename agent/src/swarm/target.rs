// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Target spec matching for swarm self-assignment.
//!
//! Port of `server/swarm/target.go` `MatchTarget()`. Supports comma-separated
//! patterns with `-` prefix for exclusion, glob matching on hostnames, and
//! `key=value` trait matching.

use std::collections::HashMap;

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
        return traits.get(key).map_or(false, |v| v == value);
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
}
