// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Agent-side policy bundle evaluation.
//! Port of agent/policy/eval.go.
//!
//! Performs the same computation the server would do in legacy mode:
//! 1. Render vars templates with traits
//! 2. Filter modules by when expressions
//! 3. Filter resources by when expressions (split server/agent-side)
//! 4. Render resource attribute templates

use crate::policy::{template, when};
use crate::proto;
use anyhow::{Context, Result};
use serde_json::Value;
use tracing::{debug, warn};

/// Evaluate a policy bundle using the agent's traits.
/// Returns compiled proto modules in the same format the runner expects.
pub fn eval_bundle(
    bundle: Option<&proto::PolicyBundle>,
    traits: &Value,
) -> Result<Vec<proto::Configcrate>> {
    let bundle = match bundle {
        Some(b) if !b.configcrates.is_empty() => b,
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

    // 2. Render vars templates.
    let var_order: Vec<String> = bundle.var_order.clone();
    template::render_vars(&mut vars, traits, &var_order)
        .context("rendering vars")?;

    // 4. Convert policy modules to compiled proto modules.
    let vars_value = Value::Object(vars.clone());
    let mut modules = Vec::with_capacity(bundle.configcrates.len());

    for pmod in &bundle.configcrates {
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
                        when = %crate::policy::secrets::global_cache().scrub(&pmod.when),
                        error = %crate::policy::secrets::global_cache().scrub(&e.to_string()),
                        "skipping module: when expression failed"
                    );
                    continue;
                }
            }
        }

        let mut pb_mod = proto::Configcrate {
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
                            when = %crate::policy::secrets::global_cache().scrub(&pres.when),
                            error = %crate::policy::secrets::global_cache().scrub(&e.to_string()),
                            "skipping resource: when template render failed"
                        );
                        continue;
                    }
                }
            } else {
                pres.when.clone()
            };

            // A templated when: reads very differently before and after var
            // substitution — the configcrate's own `vars:` default is often
            // overridden by common.vgo or the match block, so the literal in
            // the .vgo file is not what actually gates the resource. Log both
            // forms so diagnosing "why did this run?" doesn't require dumping
            // the cached bundle out of LMDB.
            if pres.when.contains("{{") {
                debug!(
                    module = %pmod.name,
                    resource = %pres.name,
                    raw = %pres.when,
                    rendered = %crate::policy::secrets::global_cache().scrub(&rendered_when),
                    "when template rendered"
                );
            }

            // Evaluate resource when expression.
            let (should_include, agent_when) = match evaluate_resource_when(&rendered_when, &vars) {
                Ok(result) => result,
                Err(e) => {
                    warn!(
                        module = %pmod.name,
                        resource = %pres.name,
                        when = %crate::policy::secrets::global_cache().scrub(&rendered_when),
                        error = %crate::policy::secrets::global_cache().scrub(&e.to_string()),
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
            // BTreeMap (not HashMap) because this map flows into
            // `proto::Resource.params`, which prost generates as
            // BTreeMap for deterministic encoding (see agent/build.rs).
            let mut params = std::collections::BTreeMap::new();
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
                            error = %crate::policy::secrets::global_cache().scrub(&e.to_string()),
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

            // notify / subscribes belong to THIS resource, not to its
            // configcrate: carry them on the resource (the evaluated
            // `proto::Resource` has no fields for them) and let
            // `runner::dag::build_dag` lift them onto the node. Lifting them
            // to `pb_mod.notify` made every sibling resource notify (and every
            // sibling subscribe) too.
            if !pres.notify.is_empty() {
                params.insert(
                    crate::runner::dag::NOTIFY_TARGETS_PARAM.to_string(),
                    crate::runner::dag::encode_targets(&pres.notify),
                );
            }
            if !pres.subscribes.is_empty() {
                params.insert(
                    crate::runner::dag::SUBSCRIBE_TARGETS_PARAM.to_string(),
                    crate::runner::dag::encode_targets(&pres.subscribes),
                );
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

        }

        if pb_mod.resources.is_empty() {
            continue;
        }

        modules.push(pb_mod);
    }

    Ok(modules)
}

/// Evaluate a module-level when expression against vars.
fn eval_module_when(when_str: &str, vars: &serde_json::Map<String, Value>) -> Result<bool> {
    let expr = when::parse(when_str)?;
    // Substitute first so `==` / `!=` operands become string literals.
    when::eval(&when::substitute_vars(&expr, vars), &|name| when::when_var_is_true(name, vars))
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

    // `changed` is the runner's notify sentinel, not a var. Only the runner can
    // know its value — it is set per-execution from whether a notify woke this
    // resource. Evaluating it here would look it up in vars, miss, and resolve
    // false (and for `changed && <func>` the tristate pass short-circuits the
    // whole expression to false off that same VarRef), dropping the resource
    // from the bundle before the runner could ever trigger it. Pass the
    // expression through untouched and let the runner decide.
    if when::references_changed(&expr) {
        return Ok((true, when_str.to_string()));
    }

    if !when::contains_func_calls(&expr) {
        // Pure var expression — evaluate fully.
        let result = when::eval(&when::substitute_vars(&expr, vars), &|name| {
            when::when_var_is_true(name, vars)
        })?;
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
    use crate::proto::{PolicyBundle, PolicyConfigcrate, PolicyResource};
    use serde_json::json;

    fn must_marshal_vars(vars: &Value) -> Vec<u8> {
        serde_json::to_vec(vars).unwrap()
    }

    // `changed` is a runner-level sentinel, not a var: the runner gates on it
    // via the `_triggered` param it sets during the notify second pass
    // (runner/mod.rs). But eval_bundle runs FIRST, and it treats every bare
    // identifier as a var lookup — `changed` is never in vars, so it evaluates
    // false and the resource is dropped from the bundle before the runner can
    // ever trigger it. Every `notify:`/`subscribes:` target in the fleet is
    // therefore silently unreachable.
    #[test]
    fn when_changed_resource_survives_eval_bundle() {
        let bundle = PolicyBundle {
            policy_version: 1,
            configcrates: vec![PolicyConfigcrate {
                name: "svc".to_string(),
                resources: vec![PolicyResource {
                    name: "restart-on-change".to_string(),
                    r#type: "service".to_string(),
                    when: "changed".to_string(),
                    attributes: [("state".to_string(), "restarted".to_string())].into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        let kept: Vec<_> = modules.iter().flat_map(|m| &m.resources).collect();
        assert_eq!(
            kept.len(),
            1,
            "a `when: changed` resource must reach the runner so notify can trigger it; \
             eval_bundle dropped it instead"
        );
    }

    // Same defect via the compound form. `tristate_eval` maps VarRef -> Some(false)
    // unconditionally, so `changed && <func>` short-circuits to false and is
    // dropped even though the func half is genuinely unknown at this stage.
    #[test]
    fn when_changed_and_builtin_survives_eval_bundle() {
        let bundle = PolicyBundle {
            policy_version: 1,
            configcrates: vec![PolicyConfigcrate {
                name: "svc".to_string(),
                resources: vec![PolicyResource {
                    name: "reload-on-change".to_string(),
                    r#type: "exec".to_string(),
                    when: "changed && os_family('debian')".to_string(),
                    attributes: [("command".to_string(), "systemctl daemon-reload".to_string())]
                        .into(),
                    ..Default::default()
                }],
                ..Default::default()
            }],
            ..Default::default()
        };

        let modules = eval_bundle(Some(&bundle), &json!(null)).unwrap();
        let kept: Vec<_> = modules.iter().flat_map(|m| &m.resources).collect();
        assert_eq!(
            kept.len(),
            1,
            "`when: changed && os_family(...)` must reach the runner; eval_bundle dropped it"
        );
    }

    #[test]
    fn test_eval_bundle_basic_module() {
        let bundle = PolicyBundle {
            policy_version: 1,
            configcrates: vec![PolicyConfigcrate {
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
            configcrates: vec![PolicyConfigcrate {
                name: "app-config".to_string(),
                resources: vec![PolicyResource {
                    name: "config-file".to_string(),
                    r#type: "file".to_string(),
                    attributes: [
                        ("target_path".to_string(), "/etc/{{ .Vars.app_name }}/config.yaml".to_string()),
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
        assert_eq!(params["target_path"], "/etc/myapp/config.yaml");
        assert_eq!(params["content"], "hostname=web-01.example.com");
    }

    // Removed in 0.27.0: `ConditionalVarDef` / `conditional_vars` — the
    // former `test_eval_bundle_conditional_vars` exercised a proto field
    // that is now `reserved 5` in vigo.proto. Conditional-var evaluation
    // moved to server-side resolution; there's nothing to exercise here.

    #[test]
    fn test_eval_bundle_resource_when_filter() {
        let bundle = PolicyBundle {
            policy_version: 1,
            vars_json: must_marshal_vars(&json!({"enable_ssl": "true"})),
            var_order: vec!["enable_ssl".to_string()],
            configcrates: vec![PolicyConfigcrate {
                name: "web".to_string(),
                resources: vec![
                    PolicyResource {
                        name: "base-config".to_string(),
                        r#type: "file".to_string(),
                        attributes: [("target_path".to_string(), "/etc/web.conf".to_string())].into(),
                        ..Default::default()
                    },
                    PolicyResource {
                        name: "ssl-config".to_string(),
                        r#type: "file".to_string(),
                        when: "enable_ssl".to_string(),
                        attributes: [("target_path".to_string(), "/etc/ssl.conf".to_string())].into(),
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
            configcrates: vec![
                PolicyConfigcrate {
                    name: "config".to_string(),
                    resources: vec![PolicyResource {
                        name: "app-config".to_string(),
                        r#type: "file".to_string(),
                        attributes: [("target_path".to_string(), "/etc/app.conf".to_string())].into(),
                        notify: vec!["svc".to_string()],
                        ..Default::default()
                    }],
                    ..Default::default()
                },
                PolicyConfigcrate {
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
        // Resource-level notify rides on the resource, never lifted to the module.
        assert!(modules[0].notify.is_empty());
        let targets = &modules[0].resources[0].params[crate::runner::dag::NOTIFY_TARGETS_PARAM];
        assert_eq!(crate::runner::dag::decode_targets(targets), vec!["svc"]);
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
            configcrates: vec![PolicyConfigcrate {
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
            configcrates: vec![PolicyConfigcrate {
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
            configcrates: vec![
                PolicyConfigcrate {
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
                PolicyConfigcrate {
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
            configcrates: vec![PolicyConfigcrate {
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
                            ("target_path".to_string(), "/etc/foo".to_string()),
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
            configcrates: vec![PolicyConfigcrate {
                name: "app".to_string(),
                resources: vec![PolicyResource {
                    name: "guarded-file".to_string(),
                    r#type: "file".to_string(),
                    when: "enabled and file_exists('/etc/app')".to_string(),
                    attributes: [("target_path".to_string(), "/etc/app/config".to_string())].into(),
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
