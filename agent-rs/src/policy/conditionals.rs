// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Conditional variable evaluation.
//! Port of server/conditionals/conditionals.go (255 lines).
//!
//! A conditional var has a trait dot-path and an operator (in, not_in, gt, lt,
//! gte, lte, regex). It evaluates to a boolean based on the trait value.

use regex::Regex;
use serde_json::Value;

/// Parsed conditional variable (matches Go's ConditionalVar struct).
pub struct ConditionalVar {
    pub name: String,
    pub trait_path: String,
    pub operator: String,
    pub operand: Value,
    /// For regex operator, the compiled regex (None for other operators).
    pub compiled_regex: Option<Regex>,
}

/// Evaluate a conditional var against the traits map.
/// Returns false if the trait is missing or cannot be coerced.
pub fn evaluate(cv: &ConditionalVar, traits: &Value) -> bool {
    let trait_val = traverse_dot_path(&cv.trait_path, traits);
    let trait_val = match trait_val {
        Some(v) => v,
        None => return false,
    };

    match cv.operator.as_str() {
        "in" => eval_in(trait_val, &cv.operand),
        "not_in" => eval_not_in(trait_val, &cv.operand),
        "gt" => eval_numeric(trait_val, &cv.operand, |a, b| a > b),
        "lt" => eval_numeric(trait_val, &cv.operand, |a, b| a < b),
        "gte" => eval_numeric(trait_val, &cv.operand, |a, b| a >= b),
        "lte" => eval_numeric(trait_val, &cv.operand, |a, b| a <= b),
        "regex" => eval_regex(trait_val, cv),
        _ => false,
    }
}

/// Walk nested JSON using dot-separated path.
/// Returns None if any segment is missing.
pub fn traverse_dot_path<'a>(path: &str, val: &'a Value) -> Option<&'a Value> {
    let parts: Vec<&str> = path.split('.').collect();
    let mut current = val;
    for part in parts {
        match current.as_object() {
            Some(map) => match map.get(part) {
                Some(v) => current = v,
                None => return None,
            },
            None => return None,
        }
    }
    Some(current)
}

/// Check if trait value (as string) is in the operand list.
fn eval_in(trait_val: &Value, operand: &Value) -> bool {
    let list = match operand.as_array() {
        Some(l) => l,
        None => return false,
    };
    let trait_str = value_to_string(trait_val);
    list.iter().any(|item| value_to_string(item) == trait_str)
}

/// Check if trait value (as string) is NOT in the operand list.
fn eval_not_in(trait_val: &Value, operand: &Value) -> bool {
    let list = match operand.as_array() {
        Some(l) => l,
        None => return false,
    };
    let trait_str = value_to_string(trait_val);
    !list.iter().any(|item| value_to_string(item) == trait_str)
}

/// Coerce both values to f64 and apply comparator.
fn eval_numeric(trait_val: &Value, operand: &Value, cmp: fn(f64, f64) -> bool) -> bool {
    let a = match to_f64(trait_val) {
        Some(v) => v,
        None => return false,
    };
    let b = match to_f64(operand) {
        Some(v) => v,
        None => return false,
    };
    cmp(a, b)
}

/// Run compiled regex against string form of trait value.
fn eval_regex(trait_val: &Value, cv: &ConditionalVar) -> bool {
    let re = match &cv.compiled_regex {
        Some(r) => r,
        None => return false,
    };
    let trait_str = value_to_string(trait_val);
    re.is_match(&trait_str)
}

/// Convert a JSON value to its string representation (matches Go's fmt.Sprintf("%v", v)).
fn value_to_string(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Number(n) => {
            // Prefer integer representation when possible (Go's %v behavior)
            if let Some(i) = n.as_i64() {
                i.to_string()
            } else if let Some(u) = n.as_u64() {
                u.to_string()
            } else if let Some(f) = n.as_f64() {
                format!("{}", f)
            } else {
                n.to_string()
            }
        }
        Value::Bool(b) => b.to_string(),
        Value::Null => "".to_string(),
        _ => format!("{}", v),
    }
}

/// Coerce JSON value to f64.
fn to_f64(v: &Value) -> Option<f64> {
    match v {
        Value::Number(n) => n.as_f64(),
        Value::String(s) => s.parse::<f64>().ok(),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn make_cv(name: &str, trait_path: &str, operator: &str, operand: Value) -> ConditionalVar {
        let compiled_regex = if operator == "regex" {
            operand.as_str().and_then(|s| Regex::new(s).ok())
        } else {
            None
        };
        ConditionalVar {
            name: name.to_string(),
            trait_path: trait_path.to_string(),
            operator: operator.to_string(),
            operand,
            compiled_regex,
        }
    }

    #[test]
    fn test_in_match() {
        let cv = make_cv("is_debian", "os.distro", "in", json!(["debian", "ubuntu"]));
        let traits = json!({"os": {"distro": "debian"}});
        assert!(evaluate(&cv, &traits));
    }

    #[test]
    fn test_in_no_match() {
        let cv = make_cv("is_debian", "os.distro", "in", json!(["debian", "ubuntu"]));
        let traits = json!({"os": {"distro": "centos"}});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_not_in_match() {
        let cv = make_cv("not_debian", "os.distro", "not_in", json!(["debian", "ubuntu"]));
        let traits = json!({"os": {"distro": "centos"}});
        assert!(evaluate(&cv, &traits));
    }

    #[test]
    fn test_not_in_no_match() {
        let cv = make_cv("not_debian", "os.distro", "not_in", json!(["debian", "ubuntu"]));
        let traits = json!({"os": {"distro": "debian"}});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_gt() {
        let cv = make_cv("big_mem", "memory.total_gb", "gt", json!(16));
        let traits = json!({"memory": {"total_gb": 32}});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 16}});
        assert!(!evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 8}});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_lt() {
        let cv = make_cv("small_mem", "memory.total_gb", "lt", json!(16));
        let traits = json!({"memory": {"total_gb": 8}});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 32}});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_gte() {
        let cv = make_cv("enough_mem", "memory.total_gb", "gte", json!(16));
        let traits = json!({"memory": {"total_gb": 16}});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 32}});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 8}});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_lte() {
        let cv = make_cv("small_mem", "memory.total_gb", "lte", json!(16));
        let traits = json!({"memory": {"total_gb": 8}});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 16}});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"memory": {"total_gb": 32}});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_regex_match() {
        let cv = make_cv("is_web", "hostname", "regex", json!("^web-\\d+"));
        let traits = json!({"hostname": "web-01"});
        assert!(evaluate(&cv, &traits));

        let traits = json!({"hostname": "db-01"});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_missing_trait() {
        let cv = make_cv("x", "os.distro", "in", json!(["debian"]));
        let traits = json!({"os": {}});
        assert!(!evaluate(&cv, &traits));

        let traits = json!({});
        assert!(!evaluate(&cv, &traits));
    }

    #[test]
    fn test_string_numeric_coercion() {
        let cv = make_cv("x", "os.version", "gte", json!("22.04"));
        let traits = json!({"os": {"version": "22.04"}});
        assert!(evaluate(&cv, &traits));
    }

    #[test]
    fn test_dot_path_traversal() {
        let traits = json!({"a": {"b": {"c": "deep"}}});
        let val = traverse_dot_path("a.b.c", &traits);
        assert_eq!(val, Some(&json!("deep")));

        let val = traverse_dot_path("a.b.missing", &traits);
        assert_eq!(val, None);

        let val = traverse_dot_path("a.missing.c", &traits);
        assert_eq!(val, None);
    }
}
