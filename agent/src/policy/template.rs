// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Go-template-compatible renderer.
//! Port of server/config/template.go (78 lines).
//!
//! Supports only the subset of Go `text/template` actually used by Vigo:
//! - `{{ .Vars.key }}` — dot-path access into vars map
//! - `{{ .Traits.os.distro }}` — dot-path access into traits map
//! - Whitespace trimming: `{{- .Vars.key -}}` (leading/trailing dash)
//!
//! No if/range/with/pipes/custom functions needed.

use anyhow::{bail, Result};
use serde_json::Value;

/// Template context matching Go's templateContext struct.
pub struct TemplateContext<'a> {
    pub traits: &'a Value,
    pub vars: &'a Value,
}

/// Render a Go template string with .Traits and .Vars namespaces.
pub fn render(tmpl: &str, ctx: &TemplateContext) -> Result<String> {
    let mut result = String::with_capacity(tmpl.len());
    let mut rest = tmpl;

    while let Some(start) = rest.find("{{") {
        let end = match rest[start..].find("}}") {
            Some(e) => start + e + 2,
            None => bail!("unterminated template expression"),
        };

        // Check for trim markers
        let before = &rest[..start];
        let inner = &rest[start + 2..end - 2];

        let (trim_left, trim_right, expr) = parse_trim_markers(inner);

        if trim_left {
            // Trim trailing whitespace from accumulated text
            let trimmed = before.trim_end();
            result.push_str(trimmed);
        } else {
            result.push_str(before);
        }

        // Resolve the dot-path expression
        let expr = expr.trim();
        if expr.is_empty() {
            bail!("empty template expression");
        }

        let resolved = resolve_expr(expr, ctx)?;
        result.push_str(&resolved);

        rest = &rest[end..];
        if trim_right {
            rest = rest.trim_start();
        }
    }

    result.push_str(rest);
    Ok(result)
}

/// Parse `{{- expr -}}` trim markers. Returns (trim_left, trim_right, inner_expr).
fn parse_trim_markers(inner: &str) -> (bool, bool, &str) {
    let inner = inner.trim();
    let trim_left = inner.starts_with('-');
    let trim_right = inner.ends_with('-');

    let inner = if trim_left {
        inner[1..].trim_start()
    } else {
        inner
    };
    let inner = if trim_right {
        inner[..inner.len() - 1].trim_end()
    } else {
        inner
    };

    (trim_left, trim_right, inner)
}

/// Resolve a dot-path expression like `.Vars.app_name` or `.Traits.os.distro`.
fn resolve_expr(expr: &str, ctx: &TemplateContext) -> Result<String> {
    if !expr.starts_with('.') {
        bail!("template expression must start with '.', got {:?}", expr);
    }

    let path = &expr[1..]; // strip leading dot
    let parts: Vec<&str> = path.splitn(2, '.').collect();
    if parts.is_empty() {
        bail!("empty dot-path in template expression");
    }

    let (root, remaining) = match parts.as_slice() {
        [root, remaining] => (*root, *remaining),
        [root] => (*root, ""),
        _ => bail!("invalid dot-path {:?}", expr),
    };

    let root_val = match root {
        "Traits" => ctx.traits,
        "Vars" => ctx.vars,
        _ => bail!("unknown template namespace {:?} (expected .Traits or .Vars)", root),
    };

    if remaining.is_empty() {
        return Ok(value_to_template_string(root_val));
    }

    // Walk the remaining dot-path
    let val = walk_path(remaining, root_val);
    match val {
        Some(v) => Ok(value_to_template_string(v)),
        None => {
            // Go template returns "<no value>" for missing paths by default,
            // but with Option "missingkey=zero" it returns "". Vigo uses the
            // default (which actually returns the zero value for the type).
            // For map lookups that fail, Go returns "".
            Ok(String::new())
        }
    }
}

/// Walk a dot-separated path into a JSON value.
fn walk_path<'a>(path: &str, val: &'a Value) -> Option<&'a Value> {
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

/// Convert a JSON value to its template output string (matches Go's fmt behavior).
fn value_to_template_string(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        Value::Number(n) => {
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
        Value::Null => String::new(),
        Value::Array(_) | Value::Object(_) => format!("{}", v),
    }
}

/// Render vars templates in declaration order (matching Go's RenderVars).
/// Only string values are rendered; non-string values pass through unchanged.
pub fn render_vars(
    vars: &mut serde_json::Map<String, Value>,
    traits: &Value,
    var_order: &[String],
) -> Result<()> {
    // Use var_order for deterministic top-down rendering.
    let keys: Vec<String> = if var_order.is_empty() {
        vars.keys().cloned().collect()
    } else {
        var_order.to_vec()
    };

    for key in &keys {
        let val = match vars.get(key) {
            Some(v) => v.clone(),
            None => continue,
        };
        if let Value::String(tmpl) = &val {
            let ctx = TemplateContext {
                traits,
                vars: &Value::Object(vars.clone()),
            };
            let rendered = render(tmpl, &ctx)?;
            vars.insert(key.clone(), Value::String(rendered));
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn ctx<'a>(traits: &'a Value, vars: &'a Value) -> TemplateContext<'a> {
        TemplateContext { traits, vars }
    }

    #[test]
    fn test_no_template() {
        let traits = json!({});
        let vars = json!({});
        assert_eq!(render("plain text", &ctx(&traits, &vars)).unwrap(), "plain text");
    }

    #[test]
    fn test_var_substitution() {
        let traits = json!({});
        let vars = json!({"app_name": "myapp"});
        assert_eq!(
            render("/etc/{{ .Vars.app_name }}/config.yaml", &ctx(&traits, &vars)).unwrap(),
            "/etc/myapp/config.yaml"
        );
    }

    #[test]
    fn test_trait_substitution() {
        let traits = json!({"hostname": "web-01.example.com"});
        let vars = json!({});
        assert_eq!(
            render("hostname={{ .Traits.hostname }}", &ctx(&traits, &vars)).unwrap(),
            "hostname=web-01.example.com"
        );
    }

    #[test]
    fn test_nested_trait() {
        let traits = json!({"os": {"distro": "ubuntu"}});
        let vars = json!({});
        assert_eq!(
            render("distro={{ .Traits.os.distro }}", &ctx(&traits, &vars)).unwrap(),
            "distro=ubuntu"
        );
    }

    #[test]
    fn test_multiple_expressions() {
        let traits = json!({"hostname": "web-01"});
        let vars = json!({"port": "8080"});
        assert_eq!(
            render("{{ .Traits.hostname }}:{{ .Vars.port }}", &ctx(&traits, &vars)).unwrap(),
            "web-01:8080"
        );
    }

    #[test]
    fn test_trim_markers() {
        let traits = json!({});
        let vars = json!({"x": "val"});
        assert_eq!(
            render("  {{- .Vars.x -}}  rest", &ctx(&traits, &vars)).unwrap(),
            "valrest"
        );
    }

    #[test]
    fn test_missing_key_returns_empty() {
        let traits = json!({});
        let vars = json!({});
        assert_eq!(
            render("val={{ .Vars.missing }}", &ctx(&traits, &vars)).unwrap(),
            "val="
        );
    }

    #[test]
    fn test_render_vars_chaining() {
        let traits = json!({});
        let mut vars = serde_json::Map::new();
        vars.insert("host".to_string(), json!("web-01"));
        vars.insert("fqdn".to_string(), json!("{{ .Vars.host }}.example.com"));

        render_vars(&mut vars, &traits, &["host".to_string(), "fqdn".to_string()]).unwrap();

        assert_eq!(vars["host"], json!("web-01"));
        assert_eq!(vars["fqdn"], json!("web-01.example.com"));
    }

    #[test]
    fn test_render_vars_non_string_passthrough() {
        let traits = json!({});
        let mut vars = serde_json::Map::new();
        vars.insert("count".to_string(), json!(42));
        vars.insert("enabled".to_string(), json!(true));

        render_vars(&mut vars, &traits, &["count".to_string(), "enabled".to_string()]).unwrap();

        assert_eq!(vars["count"], json!(42));
        assert_eq!(vars["enabled"], json!(true));
    }

    #[test]
    fn test_numeric_value_rendering() {
        let traits = json!({});
        let vars = json!({"port": 8080});
        assert_eq!(
            render("port={{ .Vars.port }}", &ctx(&traits, &vars)).unwrap(),
            "port=8080"
        );
    }
}
