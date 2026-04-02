// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! Agent-side policy bundle evaluation.
//! Port of agent/policy/eval.go (212 lines).
//!
//! Performs the same computation the server would do in legacy mode:
//! 1. Parse conditional var definitions and evaluate against traits
//! 2. Render vars templates with traits
//! 3. Filter modules by when expressions
//! 4. Filter resources by when expressions (split server/agent-side)
//! 5. Render resource attribute templates

use crate::policy::{conditionals, template, when};
use crate::proto;
use anyhow::{Context, Result};
use regex_lite::Regex;
use serde_json::Value;
use std::collections::HashMap;
use std::sync::OnceLock;
use tracing::warn;

/// Regex cache: avoid recompilation across evaluation cycles.
static REGEX_CACHE: OnceLock<std::sync::Mutex<HashMap<String, Regex>>> = OnceLock::new();

fn get_regex_cache() -> &'static std::sync::Mutex<HashMap<String, Regex>> {
    REGEX_CACHE.get_or_init(|| std::sync::Mutex::new(HashMap::new()))
}

/// Evaluate a policy bundle using the agent's traits.
/// Returns compiled proto modules in the same format the runner expects.
pub fn eval_bundle(
    bundle: Option<&proto::PolicyBundle>,
    traits: &Value,
) -> Result<Vec<proto::Module>> {
    let bundle = match bundle {
        Some(b) if !b.modules.is_empty() => b,
        _ => return Ok(Vec::new()),
    };

    // 1. Reconstruct vars from JSON.
    let mut vars: serde_json::Map<String, Value> = if bundle.vars_json.is_empty() {
        serde_json::Map::new()
    } else {
        let val: Value = serde_json::from_slice(&bundle.vars_json)
            .context("unmarshaling vars")?;
        match val {
            Value::Object(m) => m,
            _ => serde_json::Map::new(),
        }
    };

    // 2. Evaluate conditional vars against traits.
    let mut var_order: Vec<String> = bundle.var_order.clone();
    evaluate_conditional_vars(&bundle.conditional_vars, traits, &mut vars, &mut var_order);

    // 3. Render vars templates.
    template::render_vars(&mut vars, traits, &var_order)
        .context("rendering vars")?;

    // 4. Convert policy modules to compiled proto modules.
    let vars_value = Value::Object(vars.clone());
    let mut modules = Vec::with_capacity(bundle.modules.len());

    for pmod in &bundle.modules {
        // Render templates in module-level when before evaluating.
        let mod_when = if pmod.when.contains("{{") {
            let mod_ctx = template::TemplateContext { traits, vars: &vars_value };
            template::render(&pmod.when, &mod_ctx).unwrap_or_else(|_| pmod.when.clone())
        } else {
            pmod.when.clone()
        };

        // Check module-level when.
        if !mod_when.is_empty() {
            match eval_module_when(&mod_when, &vars) {
                Ok(true) => {}
                Ok(false) => continue,
                Err(e) => {
                    warn!(
                        module = %pmod.name,
                        when = %pmod.when,
                        error = %e,
                        "skipping module: when expression failed"
                    );
                    continue;
                }
            }
        }

        let mut pb_mod = proto::Module {
            name: pmod.name.clone(),
            depends_on: pmod.depends_on.clone(),
            ..Default::default()
        };

        // Build template context once per module (vars + traits are the same for all resources).
        let ctx = template::TemplateContext {
            traits,
            vars: &vars_value,
        };

        for pres in &pmod.resources {
            // Render templates in the when expression before evaluating it.
            // This allows when: "hour_range('{{ .Vars.scan_hour_start }}','{{ .Vars.scan_hour_end }}')"
            let rendered_when = if pres.when.contains("{{") {
                match template::render(&pres.when, &ctx) {
                    Ok(w) => w,
                    Err(e) => {
                        warn!(
                            module = %pmod.name,
                            resource = %pres.name,
                            when = %pres.when,
                            error = %e,
                            "skipping resource: when template render failed"
                        );
                        continue;
                    }
                }
            } else {
                pres.when.clone()
            };

            // Evaluate resource when expression.
            let (should_include, agent_when) = match evaluate_resource_when(&rendered_when, &vars) {
                Ok(result) => result,
                Err(e) => {
                    warn!(
                        module = %pmod.name,
                        resource = %pres.name,
                        when = %rendered_when,
                        error = %e,
                        "skipping resource: when expression failed"
                    );
                    continue;
                }
            };
            if !should_include {
                continue;
            }

            // Render attribute templates (ctx already built above).
            let ctx = template::TemplateContext {
                traits,
                vars: &vars_value,
            };
            let mut params = HashMap::with_capacity(pres.attributes.len());
            let mut render_failed = false;
            for (k, v) in &pres.attributes {
                match template::render(v, &ctx) {
                    Ok(rendered) => {
                        params.insert(k.clone(), rendered);
                    }
                    Err(e) => {
                        warn!(
                            resource = %pres.name,
                            attr = %k,
                            error = %e,
                            "skipping resource: template render failed"
                        );
                        render_failed = true;
                        break;
                    }
                }
            }
            if render_failed {
                continue;
            }

            // Mark resources with watch_secret so the runner can skip them
            // in the main pass unless _secret_triggered=true.
            if !pres.watch_secret.is_empty()
                && params.get("_secret_triggered").map(|s| s.as_str()) != Some("true")
            {
                params.insert("_watch_secret".to_string(), "true".to_string());
            }

            let mut pb_res = proto::Resource {
                name: pres.name.clone(),
                r#type: pres.r#type.clone(),
                params,
                when: String::new(),
            };
            if !agent_when.is_empty() {
                pb_res.when = agent_when;
            }

            pb_mod.resources.push(pb_res);

            if pb_mod.resource_type.is_empty() {
                pb_mod.resource_type = pres.r#type.clone();
            }

            pb_mod.notify.extend(pres.notify.iter().cloned());
            pb_mod.subscribes.extend(pres.subscribes.iter().cloned());
        }

        if pb_mod.resources.is_empty() {
            continue;
        }

        modules.push(pb_mod);
    }

    Ok(modules)
}

/// Parse ConditionalVarDef protos, evaluate each against traits,
/// set "true"/"false" string values in vars.
fn evaluate_conditional_vars(
    defs: &[proto::ConditionalVarDef],
    traits: &Value,
    vars: &mut serde_json::Map<String, Value>,
    var_order: &mut Vec<String>,
) {
    if defs.is_empty() {
        return;
    }

    let mut cond_names = Vec::with_capacity(defs.len());

    for def in defs {
        let cv = match parse_conditional_var_def(def) {
            Ok(cv) => cv,
            Err(_) => {
                vars.insert(def.name.clone(), Value::String("false".to_string()));
                cond_names.push(def.name.clone());
                continue;
            }
        };

        let result = conditionals::evaluate(&cv, traits);
        vars.insert(
            cv.name.clone(),
            Value::String(if result { "true" } else { "false" }.to_string()),
        );
        cond_names.push(cv.name.clone());
    }

    // Prepend conditional names to var_order.
    let mut new_order = cond_names;
    new_order.append(var_order);
    *var_order = new_order;
}

/// Reconstruct a ConditionalVar from a proto definition.
fn parse_conditional_var_def(
    def: &proto::ConditionalVarDef,
) -> Result<conditionals::ConditionalVar> {
    let operand: Value = serde_json::from_str(&def.operand_json)
        .with_context(|| format!("parsing operand for {:?}", def.name))?;

    let compiled_regex = if def.operator == "regex" {
        let pattern = operand
            .as_str()
            .with_context(|| format!("regex operand for {:?} must be a string", def.name))?;

        let cache = get_regex_cache();
        let mut cache = cache.lock().unwrap();
        if let Some(re) = cache.get(pattern) {
            Some(re.clone())
        } else {
            let re = Regex::new(pattern)
                .with_context(|| format!("invalid regex for {:?}", def.name))?;
            cache.insert(pattern.to_string(), re.clone());
            Some(re)
        }
    } else {
        None
    };

    Ok(conditionals::ConditionalVar {
        name: def.name.clone(),
        trait_path: def.r#trait.clone(),
        operator: def.operator.clone(),
        operand,
        compiled_regex,
    })
}

/// Evaluate a module-level when expression against vars.
fn eval_module_when(when_str: &str, vars: &serde_json::Map<String, Value>) -> Result<bool> {
    let expr = when::parse(when_str)?;
    when::eval(&expr, &|name| when::when_var_is_true(name, vars))
}

/// Evaluate a resource's when expression (split server/agent-side).
/// Returns (should_include, agent_when_string).
fn evaluate_resource_when(
    when_str: &str,
    vars: &serde_json::Map<String, Value>,
) -> Result<(bool, String)> {
    if when_str.is_empty() {
        return Ok((true, String::new()));
    }

    let expr = when::parse(when_str)?;

    if !when::contains_func_calls(&expr) {
        // Pure var expression — evaluate fully.
        let result = when::eval(&expr, &|name| when::when_var_is_true(name, vars))?;
        return Ok((result, String::new()));
    }

    // Expression contains builtins — substitute vars, try short-circuit.
    let substituted = when::substitute_vars(&expr, vars);
    if let Some(result) = when::tristate_eval(&substituted) {
        return Ok((result, String::new()));
    }

    // Can't determine — pass to agent.
    Ok((true, when::expr_string(&substituted)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{ConditionalVarDef, PolicyBundle, PolicyModule, PolicyResource};
    use serde_json::json;

    fn must_marshal_vars(vars: &Value) -> Vec<u8> {
        serde_json::to_vec(vars).unwrap()
    }

    #[test]
    fn test_eval_bundle_basic_module() {
        let bundle = PolicyBundle {
            policy_version: 1,
            modules: vec![PolicyModule {
                name: "nginx".to_string(),
                resources: vec![PolicyResource {
                    name: "nginx-pkg".to_string(),
                    r#type: "package".to_string(),
                    attributes: [
                        ("package".to_string(), "nginx".to_string()),
                        ("state".to_string(), "present".to_string()),
                    ]
                    .into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].name, "nginx");
        assert_eq!(modules[0].resources.len(), 1);
        assert_eq!(modules[0].resources[0].params["package"], "nginx");
    }

    #[test]
    fn test_eval_bundle_template_rendering() {
        let bundle = PolicyBundle {
            policy_version: 1,
            vars_json: must_marshal_vars(&json!({"app_name": "myapp"})),
            var_order: vec!["app_name".to_string()],
            modules: vec![PolicyModule {
                name: "app-config".to_string(),
                resources: vec![PolicyResource {
                    name: "config-file".to_string(),
                    r#type: "file".to_string(),
                    attributes: [
                        ("targetpath".to_string(), "/etc/{{ .Vars.app_name }}/config.yaml".to_string()),
                        ("content".to_string(), "hostname={{ .Traits.hostname }}".to_string()),
                    ]
                    .into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        let traits = json!({"hostname": "web-01.example.com"});
        let modules = eval_bundle(Some(&bundle), &traits).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].resources.len(), 1);

        let params = &modules[0].resources[0].params;
        assert_eq!(params["targetpath"], "/etc/myapp/config.yaml");
        assert_eq!(params["content"], "hostname=web-01.example.com");
    }

    #[test]
    fn test_eval_bundle_conditional_vars() {
        let bundle = PolicyBundle {
            policy_version: 1,
            conditional_vars: vec![ConditionalVarDef {
                name: "is_debian".to_string(),
                r#trait: "os.distro".to_string(),
                operator: "in".to_string(),
                operand_json: r#"["debian", "ubuntu"]"#.to_string(),
            }],
            modules: vec![PolicyModule {
                name: "apt-config".to_string(),
                when: "is_debian".to_string(),
                resources: vec![PolicyResource {
                    name: "apt-conf".to_string(),
                    r#type: "file".to_string(),
                    attributes: [("targetpath".to_string(), "/etc/apt/apt.conf".to_string())].into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        // Debian → included
        let traits = json!({"os": {"distro": "debian"}});
        let modules = eval_bundle(Some(&bundle), &traits).unwrap();
        assert_eq!(modules.len(), 1);

        // CentOS → excluded
        let traits = json!({"os": {"distro": "centos"}});
        let modules = eval_bundle(Some(&bundle), &traits).unwrap();
        assert_eq!(modules.len(), 0);
    }

    #[test]
    fn test_eval_bundle_resource_when_filter() {
        let bundle = PolicyBundle {
            policy_version: 1,
            vars_json: must_marshal_vars(&json!({"enable_ssl": "true"})),
            var_order: vec!["enable_ssl".to_string()],
            modules: vec![PolicyModule {
                name: "web".to_string(),
                resources: vec![
                    PolicyResource {
                        name: "base-config".to_string(),
                        r#type: "file".to_string(),
                        attributes: [("targetpath".to_string(), "/etc/web.conf".to_string())].into(),
                        ..Default::default()
                    },
                    PolicyResource {
                        name: "ssl-config".to_string(),
                        r#type: "file".to_string(),
                        when: "enable_ssl".to_string(),
                        attributes: [("targetpath".to_string(), "/etc/ssl.conf".to_string())].into(),
                        ..Default::default()
                    },
                ],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].resources.len(), 2);

        // Disable SSL
        let mut bundle2 = bundle.clone();
        bundle2.vars_json = must_marshal_vars(&json!({"enable_ssl": "false"}));
        let modules = eval_bundle(Some(&bundle2), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].resources.len(), 1);
        assert_eq!(modules[0].resources[0].name, "base-config");
    }

    #[test]
    fn test_eval_bundle_depends_on_and_notify() {
        let bundle = PolicyBundle {
            policy_version: 1,
            modules: vec![
                PolicyModule {
                    name: "config".to_string(),
                    resources: vec![PolicyResource {
                        name: "app-config".to_string(),
                        r#type: "file".to_string(),
                        attributes: [("targetpath".to_string(), "/etc/app.conf".to_string())].into(),
                        notify: vec!["svc".to_string()],
                        ..Default::default()
                    }],
                    ..Default::default()
                },
                PolicyModule {
                    name: "svc".to_string(),
                    depends_on: vec!["config".to_string()],
                    resources: vec![PolicyResource {
                        name: "app-svc".to_string(),
                        r#type: "service".to_string(),
                        attributes: [("name".to_string(), "app".to_string())].into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            ],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 2);
        assert_eq!(modules[1].depends_on, vec!["config"]);
        assert_eq!(modules[0].notify, vec!["svc"]);
    }

    #[test]
    fn test_eval_bundle_var_template_chaining() {
        let bundle = PolicyBundle {
            policy_version: 1,
            vars_json: must_marshal_vars(&json!({
                "host": "web-01",
                "fqdn": "{{ .Vars.host }}.example.com"
            })),
            var_order: vec!["host".to_string(), "fqdn".to_string()],
            modules: vec![PolicyModule {
                name: "dns".to_string(),
                resources: vec![PolicyResource {
                    name: "hosts-entry".to_string(),
                    r#type: "file".to_string(),
                    attributes: [("content".to_string(), "{{ .Vars.fqdn }}".to_string())].into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules[0].resources[0].params["content"], "web-01.example.com");
    }

    #[test]
    fn test_eval_bundle_empty() {
        let modules = eval_bundle(None, &json!(null)).unwrap();
        assert_eq!(modules.len(), 0);
    }

    #[test]
    fn test_eval_bundle_skips_bad_when_resource() {
        // A resource with an unparseable when expression should be skipped.
        let bundle = PolicyBundle {
            policy_version: 1,
            modules: vec![PolicyModule {
                name: "mixed".to_string(),
                resources: vec![
                    PolicyResource {
                        name: "good-resource".to_string(),
                        r#type: "package".to_string(),
                        attributes: [("package".to_string(), "nginx".to_string())].into(),
                        ..Default::default()
                    },
                    PolicyResource {
                        name: "bad-when".to_string(),
                        r#type: "package".to_string(),
                        when: "!@#$invalid".to_string(),
                        attributes: [("package".to_string(), "foo".to_string())].into(),
                        ..Default::default()
                    },
                ],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].resources.len(), 1);
        assert_eq!(modules[0].resources[0].name, "good-resource");
    }

    #[test]
    fn test_eval_bundle_skips_bad_when_module() {
        // A module with an unparseable when expression should be skipped.
        let bundle = PolicyBundle {
            policy_version: 1,
            modules: vec![
                PolicyModule {
                    name: "bad-module".to_string(),
                    when: "!@#$invalid".to_string(),
                    resources: vec![PolicyResource {
                        name: "res".to_string(),
                        r#type: "package".to_string(),
                        attributes: [("package".to_string(), "foo".to_string())].into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
                PolicyModule {
                    name: "good-module".to_string(),
                    resources: vec![PolicyResource {
                        name: "res".to_string(),
                        r#type: "package".to_string(),
                        attributes: [("package".to_string(), "nginx".to_string())].into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            ],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].name, "good-module");
    }

    #[test]
    fn test_eval_bundle_skips_bad_template_resource() {
        // A resource with a broken template should be skipped, not abort the bundle.
        let bundle = PolicyBundle {
            policy_version: 1,
            modules: vec![PolicyModule {
                name: "mixed".to_string(),
                resources: vec![
                    PolicyResource {
                        name: "good-resource".to_string(),
                        r#type: "package".to_string(),
                        attributes: [
                            ("package".to_string(), "nginx".to_string()),
                            ("state".to_string(), "present".to_string()),
                        ]
                        .into(),
                        ..Default::default()
                    },
                    PolicyResource {
                        name: "bad-resource".to_string(),
                        r#type: "package".to_string(),
                        attributes: [
                            ("package".to_string(), "{{ .Vars.missing_closing".to_string()),
                        ]
                        .into(),
                        ..Default::default()
                    },
                    PolicyResource {
                        name: "another-good".to_string(),
                        r#type: "file".to_string(),
                        attributes: [
                            ("targetpath".to_string(), "/etc/foo".to_string()),
                        ]
                        .into(),
                        ..Default::default()
                    },
                ],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        // bad-resource should be skipped, the other two should remain
        assert_eq!(modules[0].resources.len(), 2);
        assert_eq!(modules[0].resources[0].name, "good-resource");
        assert_eq!(modules[0].resources[1].name, "another-good");
    }

    #[test]
    fn test_eval_bundle_agent_side_when() {
        let bundle = PolicyBundle {
            policy_version: 1,
            vars_json: must_marshal_vars(&json!({"enabled": "true"})),
            var_order: vec!["enabled".to_string()],
            modules: vec![PolicyModule {
                name: "app".to_string(),
                resources: vec![PolicyResource {
                    name: "guarded-file".to_string(),
                    r#type: "file".to_string(),
                    when: "enabled and file_exists('/etc/app')".to_string(),
                    attributes: [("targetpath".to_string(), "/etc/app/config".to_string())].into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        assert_eq!(modules.len(), 1);
        assert_eq!(modules[0].resources.len(), 1);
        // The file_exists builtin should be passed to agent-side when
        assert!(!modules[0].resources[0].when.is_empty());
    }
}
