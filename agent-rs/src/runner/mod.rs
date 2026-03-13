// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

//! Convergence runner — executes resources in DAG order with notify/subscribe
//! triggers, result caching, and dry-run support.

pub mod cache;
pub mod dag;

use crate::executors::{Action, Executor, ResourceResult};
use crate::proto::Module;
use cache::ResultCache;
use dag::{build_dag, needs_topo_sort, topo_sort, ResourceNode};
use std::collections::{HashMap, HashSet};
use tracing::{debug, info, warn};

/// Options for a convergence run.
pub struct RunOptions {
    pub dry_run: bool,
    /// Filter to a single module (by name).
    pub module_filter: Option<String>,
    /// Run ID for this convergence cycle.
    pub run_id: String,
}

/// Converge: build DAG, sort, execute resources, handle notify triggers.
///
/// Returns the list of resource results for reporting back to the server.
pub fn converge(
    modules: &[Module],
    executors: &HashMap<String, Box<dyn Executor>>,
    cache: &mut ResultCache,
    opts: &RunOptions,
) -> Vec<ResourceResult> {
    if modules.is_empty() {
        warn!("no modules to apply — check thralls.hnb");
        return Vec::new();
    }

    // Filter to single module if requested.
    let filtered: Vec<Module>;
    let modules = if let Some(ref filter) = opts.module_filter {
        filtered = modules
            .iter()
            .filter(|m| m.name == *filter)
            .cloned()
            .collect();
        if filtered.is_empty() {
            warn!(module = filter, "requested module not found");
            return Vec::new();
        }
        &filtered
    } else {
        modules
    };

    // Build DAG.
    let (nodes, notify_map) = match build_dag(modules) {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "failed to build DAG");
            return vec![ResourceResult {
                module_name: String::new(),
                resource_type: String::new(),
                resource_name: String::new(),
                action: Action::None,
                changed: false,
                error: Some(format!("DAG build error: {}", e)),
                duration_ms: 0,
            }];
        }
    };

    // Topological sort if needed.
    let ordered = if needs_topo_sort(&nodes) {
        match topo_sort(nodes) {
            Ok(sorted) => sorted,
            Err(e) => {
                warn!(error = %e, "topological sort failed");
                return vec![ResourceResult {
                    module_name: String::new(),
                    resource_type: String::new(),
                    resource_name: String::new(),
                    action: Action::None,
                    changed: false,
                    error: Some(format!("DAG sort error: {}", e)),
                    duration_ms: 0,
                }];
            }
        }
    } else {
        nodes
    };

    info!(resources = ordered.len(), "executing convergence run");

    // Track which resources changed (for notify triggers).
    let mut changed_resources: HashSet<String> = HashSet::new();
    // Track resources that need to be re-run due to notify.
    let mut notified: HashSet<String> = HashSet::new();
    let mut results: Vec<ResourceResult> = Vec::new();

    // First pass: execute all resources.
    for node in &ordered {
        let mut result = execute_resource(node, executors, cache, opts.dry_run, &opts.run_id);
        result.module_name = node.module_name.clone();

        if result.changed {
            changed_resources.insert(node.name.clone());
            // Queue notify targets.
            if let Some(targets) = notify_map.get(&node.name) {
                for target in targets {
                    notified.insert(target.clone());
                }
            }
        }

        results.push(result);
    }

    // Second pass: re-run notified resources.
    if !notified.is_empty() {
        debug!(count = notified.len(), "re-running notified resources");

        for node in &ordered {
            if !notified.contains(&node.name) {
                continue;
            }
            // Skip if already changed in the first pass (no need to re-run).
            if changed_resources.contains(&node.name) {
                continue;
            }

            // Set _triggered flag so exec resources bypass guards.
            let mut triggered_params = node.params.clone();
            triggered_params.insert("_triggered".to_string(), "true".to_string());

            let triggered_node = ResourceNode {
                params: triggered_params,
                ..node.clone()
            };

            let mut result = execute_resource(&triggered_node, executors, cache, opts.dry_run, &opts.run_id);
            result.module_name = node.module_name.clone();

            // Update the result for this resource in the results vec.
            if let Some(existing) = results.iter_mut().find(|r| {
                r.resource_name == node.name && r.module_name == node.module_name
            }) {
                *existing = result;
            } else {
                results.push(result);
            }
        }
    }

    results
}

/// Execute a single resource node.
fn execute_resource(
    node: &ResourceNode,
    executors: &HashMap<String, Box<dyn Executor>>,
    cache: &mut ResultCache,
    dry_run: bool,
    run_id: &str,
) -> ResourceResult {
    // Check when expression (agent-side builtins).
    if !node.when.is_empty() {
        match crate::policy::when::parse(&node.when) {
            Ok(expr) => {
                // Evaluate with no var substitution (already done by eval_bundle).
                let var_fn = |_name: &str| false;
                match crate::policy::when::eval(&expr, &var_fn) {
                    Ok(false) => {
                        return ResourceResult {
                            module_name: String::new(),
                            resource_type: node.resource_type.clone(),
                            resource_name: node.name.clone(),
                            action: Action::Skipped,
                            changed: false,
                            error: None,
                            duration_ms: 0,
                        };
                    }
                    Err(e) => {
                        warn!(
                            resource = %node.name,
                            error = %e,
                            "when expression eval error, executing anyway"
                        );
                    }
                    Ok(true) => {} // proceed
                }
            }
            Err(e) => {
                warn!(
                    resource = %node.name,
                    error = %e,
                    "when expression parse error, executing anyway"
                );
            }
        }
    }

    // Check result cache.
    if !dry_run {
        if let Some(cached) = cache.lookup(&node.name, &node.resource_type, &node.params) {
            debug!(resource = %node.name, "cache hit, skipping executor");
            return cached;
        }
    }

    // Find executor for this resource type.
    let executor = match executors.get(&node.resource_type) {
        Some(e) => e,
        None => {
            return ResourceResult {
                module_name: String::new(),
                resource_type: node.resource_type.clone(),
                resource_name: node.name.clone(),
                action: Action::None,
                changed: false,
                error: Some(format!(
                    "no executor for resource type {:?}",
                    node.resource_type
                )),
                duration_ms: 0,
            };
        }
    };

    // Inject _run_id for snapshot support (used by file executor).
    let params = if node.resource_type == "file" && !run_id.is_empty() {
        let mut p = node.params.clone();
        p.insert("_run_id".to_string(), run_id.to_string());
        p
    } else {
        node.params.clone()
    };

    let result = executor.execute(&params, dry_run);

    // Cache the result.
    if !dry_run {
        cache.store(&node.name, &node.resource_type, &node.params, &result);
    }

    if let Some(ref err) = result.error {
        warn!(
            resource = %node.name,
            resource_type = %node.resource_type,
            error = %err,
            "resource execution failed"
        );
    } else {
        debug!(
            resource = %node.name,
            resource_type = %node.resource_type,
            action = %result.action,
            "resource executed"
        );
    }

    result
}

/// Build the default executor map with all built-in resource types.
pub fn build_executors(
    os_family: &str,
    os_distro: &str,
) -> HashMap<String, Box<dyn Executor>> {
    build_executors_with_snapshots(os_family, os_distro, None)
}

/// Build executors with optional snapshot manager for the file executor.
pub fn build_executors_with_snapshots(
    os_family: &str,
    os_distro: &str,
    snapshot_mgr: Option<crate::snapshots::Manager>,
) -> HashMap<String, Box<dyn Executor>> {
    use crate::executors::*;

    let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();

    // Core executors (Phase 3).
    executors.insert("file".to_string(), Box::new(file::FileExecutor::new(snapshot_mgr)));
    executors.insert("exec".to_string(), Box::new(exec::ExecExecutor::new()));
    executors.insert("service".to_string(), Box::new(service::ServiceExecutor::new()));

    // Package executor: detect package manager from OS traits.
    match pkgmgr::detect(os_family, os_distro) {
        Ok(pm) => {
            executors.insert(
                "package".to_string(),
                Box::new(PackageExecutorWrapper { pkg_mgr: pm }),
            );
        }
        Err(e) => {
            warn!(error = %e, "no package manager detected, package resources will fail");
        }
    }

    // Phase 5 executors.
    executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
    executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
    executors.insert("cron".to_string(), Box::new(cron::CronExecutor));
    executors.insert("process".to_string(), Box::new(process::ProcessExecutor::new()));
    executors.insert("file_line".to_string(), Box::new(file_line::FileLineExecutor));
    executors.insert("host".to_string(), Box::new(host::HostExecutor::new()));
    executors.insert("hostname".to_string(), Box::new(hostname::HostnameExecutor::new()));
    executors.insert("timezone".to_string(), Box::new(timezone::TimezoneExecutor::new()));
    executors.insert("sysctl".to_string(), Box::new(sysctl::SysctlExecutor::new()));
    executors.insert("sshkey".to_string(), Box::new(sshkey::SshKeyExecutor));
    executors.insert("firewall".to_string(), Box::new(firewall::FirewallExecutor::new()));
    executors.insert("kernel_module".to_string(), Box::new(kernel_module::KernelModuleExecutor::new()));
    executors.insert("mount".to_string(), Box::new(mount::MountExecutor::new()));
    executors.insert("swap".to_string(), Box::new(swap::SwapExecutor::new()));
    executors.insert("tmpfile".to_string(), Box::new(tmpfile::TmpfileExecutor::new()));
    executors.insert("ini".to_string(), Box::new(ini::IniExecutor));
    executors.insert("json_file".to_string(), Box::new(json_file::JsonFileExecutor));
    executors.insert("blockinfile".to_string(), Box::new(blockinfile::BlockInFileExecutor));
    executors.insert("archive".to_string(), Box::new(archive::ArchiveExecutor::new()));
    executors.insert("repository".to_string(), Box::new(repository::RepositoryExecutor::for_os(os_family)));
    executors.insert("git".to_string(), Box::new(git::GitExecutor::new()));
    executors.insert("logrotate".to_string(), Box::new(logrotate::LogrotateExecutor));
    executors.insert("reboot".to_string(), Box::new(reboot::RebootExecutor::new()));
    executors.insert("alternatives".to_string(), Box::new(alternatives::AlternativesExecutor::new()));
    executors.insert("pip".to_string(), Box::new(pip::PipExecutor::new()));
    executors.insert("gem".to_string(), Box::new(gem::GemExecutor::new()));
    executors.insert("npm".to_string(), Box::new(npm::NpmExecutor::new()));
    executors.insert("docker_container".to_string(), Box::new(docker_container::DockerContainerExecutor::new()));
    executors.insert("docker_image".to_string(), Box::new(docker_image::DockerImageExecutor::new()));

    executors
}

/// Load custom executors from a directory.
pub fn load_custom_executors(
    executors: &mut HashMap<String, Box<dyn Executor>>,
    dir: &str,
) {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return, // Directory doesn't exist — that's fine.
    };

    for entry in entries.flatten() {
        let path = entry.path();
        if !path.is_file() {
            continue;
        }

        // Check executable bit.
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if let Ok(meta) = path.metadata() {
                if meta.permissions().mode() & 0o111 == 0 {
                    continue; // Not executable.
                }
            }
        }

        let name = path
            .file_stem()
            .and_then(|s| s.to_str())
            .unwrap_or("")
            .to_string();

        if name.is_empty() {
            continue;
        }

        // Never override built-in types.
        if executors.contains_key(&name) {
            warn!(name = %name, "custom executor would override built-in type, skipping");
            continue;
        }

        info!(name = %name, path = %path.display(), "loaded custom executor");
        executors.insert(
            name,
            Box::new(crate::executors::custom::CustomExecutor {
                script_path: path.to_string_lossy().to_string(),
            }),
        );
    }
}

/// Wrapper that adapts the PackageManager trait to the Executor trait.
struct PackageExecutorWrapper {
    pkg_mgr: Box<dyn crate::executors::pkgmgr::PackageManager>,
}

impl Executor for PackageExecutorWrapper {
    fn execute(
        &self,
        params: &HashMap<String, String>,
        dry_run: bool,
    ) -> ResourceResult {
        let start = std::time::Instant::now();
        let name_param = params
            .get("package")
            .or_else(|| params.get("name"))
            .cloned()
            .unwrap_or_default();

        let mut result = ResourceResult::new("package", &name_param);

        if name_param.is_empty() {
            result.error = Some("package executor: name param is required".to_string());
            result.duration_ms = start.elapsed().as_millis() as i32;
            return result;
        }

        let state = params.get("state").map(|s| s.as_str()).unwrap_or("present");
        let version = params.get("version").map(|s| s.as_str()).unwrap_or("");

        // Support comma-separated package names.
        let names: Vec<&str> = name_param
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .collect();

        for name in &names {
            let res = self.execute_one_package(name, state, version, dry_run);
            if res.error.is_some() {
                result.error = res.error;
                result.duration_ms = start.elapsed().as_millis() as i32;
                return result;
            }
            if res.changed {
                result.changed = true;
                if result.action == Action::None {
                    result.action = res.action;
                }
            }
        }

        result.duration_ms = start.elapsed().as_millis() as i32;
        result
    }
}

impl PackageExecutorWrapper {
    fn execute_one_package(
        &self,
        name: &str,
        state: &str,
        version: &str,
        dry_run: bool,
    ) -> ResourceResult {
        let mut result = ResourceResult::new("package", name);

        let (installed, current_version) = match self.pkg_mgr.is_installed(name) {
            Ok(r) => r,
            Err(e) => {
                result.error = Some(format!("query package {}: {}", name, e));
                return result;
            }
        };

        match state {
            "present" => {
                if installed {
                    if !version.is_empty() && current_version != version {
                        result.action = Action::Updated;
                        result.changed = true;
                        if dry_run {
                            return result;
                        }
                        if let Err(e) = self.pkg_mgr.upgrade(name, version) {
                            result.error =
                                Some(format!("upgrade package {} to {}: {}", name, version, e));
                        }
                        return result;
                    }
                    result.action = Action::None;
                    result.changed = false;
                    return result;
                }
                result.action = Action::Created;
                result.changed = true;
                if dry_run {
                    return result;
                }
                if let Err(e) = self.pkg_mgr.install(name, version) {
                    result.error = Some(format!("install package {}: {}", name, e));
                }
                result
            }
            "absent" => {
                if !installed {
                    result.action = Action::None;
                    result.changed = false;
                    return result;
                }
                result.action = Action::Deleted;
                result.changed = true;
                if dry_run {
                    return result;
                }
                if let Err(e) = self.pkg_mgr.remove(name) {
                    result.error = Some(format!("remove package {}: {}", name, e));
                }
                result
            }
            _ => {
                result.error = Some(format!(
                    "package executor: unknown state {:?} (expected present or absent)",
                    state
                ));
                result
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{Module, Resource};

    struct MockExecutor {
        action: Action,
        changed: bool,
    }

    impl Executor for MockExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            let name = params.get("name").map(|s| s.as_str()).unwrap_or("mock");
            ResourceResult {
                module_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action: self.action.clone(),
                changed: self.changed,
                error: None,
                duration_ms: 1,
            }
        }
    }

    fn make_module(name: &str, resources: Vec<(&str, &str)>) -> Module {
        Module {
            name: name.to_string(),
            resource_type: String::new(),
            resources: resources
                .into_iter()
                .map(|(rname, rtype)| Resource {
                    name: rname.to_string(),
                    r#type: rtype.to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), rname.to_string());
                        p
                    },
                    when: String::new(),
                })
                .collect(),
            depends_on: vec![],
            notify: vec![],
            subscribes: vec![],
        }
    }

    #[test]
    fn converge_empty_modules() {
        let mut cache = ResultCache::new(5);
        let executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&[], &executors, &mut cache, &opts);
        assert!(results.is_empty());
    }

    #[test]
    fn converge_executes_resources() {
        let mut cache = ResultCache::new(5);
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert(
            "mock".to_string(),
            Box::new(MockExecutor {
                action: Action::None,
                changed: false,
            }),
        );

        let modules = vec![make_module("test-mod", vec![("res1", "mock"), ("res2", "mock")])];

        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts);
        assert_eq!(results.len(), 2);
        assert!(results.iter().all(|r| r.error.is_none()));
    }

    #[test]
    fn converge_unknown_executor() {
        let mut cache = ResultCache::new(5);
        let executors: HashMap<String, Box<dyn Executor>> = HashMap::new();

        let modules = vec![make_module("test-mod", vec![("res1", "unknown_type")])];

        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts);
        assert_eq!(results.len(), 1);
        assert!(results[0].error.is_some());
        assert!(results[0].error.as_ref().unwrap().contains("no executor"));
    }

    #[test]
    fn converge_module_filter() {
        let mut cache = ResultCache::new(5);
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert(
            "mock".to_string(),
            Box::new(MockExecutor {
                action: Action::None,
                changed: false,
            }),
        );

        let modules = vec![
            make_module("keep", vec![("res1", "mock")]),
            make_module("skip", vec![("res2", "mock")]),
        ];

        let opts = RunOptions {
            dry_run: false,
            module_filter: Some("keep".into()),
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts);
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].module_name, "keep");
    }

    // ── Notify/subscribe trigger chain tests ──────────────────────────

    use std::sync::{Arc, Mutex};

    /// Executor that tracks per-resource behavior and records every call
    /// with whether `_triggered` was set.
    struct TrackingExecutor {
        /// Maps resource name -> (action, changed) to return.
        behaviors: HashMap<String, (Action, bool)>,
        /// Records each (resource_name, was_triggered) call in order.
        calls: Arc<Mutex<Vec<(String, bool)>>>,
    }

    impl TrackingExecutor {
        fn new(behaviors: Vec<(&str, Action, bool)>) -> Self {
            Self {
                behaviors: behaviors
                    .into_iter()
                    .map(|(n, a, c)| (n.to_string(), (a, c)))
                    .collect(),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl Executor for TrackingExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            let name = params
                .get("name")
                .map(|s| s.as_str())
                .unwrap_or("unknown");
            let triggered = params
                .get("_triggered")
                .map(|s| s == "true")
                .unwrap_or(false);
            self.calls
                .lock()
                .unwrap()
                .push((name.to_string(), triggered));

            let (action, changed) = self
                .behaviors
                .get(name)
                .cloned()
                .unwrap_or((Action::None, false));
            ResourceResult {
                module_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action,
                changed,
                error: None,
                duration_ms: 1,
            }
        }
    }

    /// Executor that also records whether dry_run was set on each call.
    struct DryRunTrackingExecutor {
        calls: Arc<Mutex<Vec<(String, bool)>>>,
    }

    impl DryRunTrackingExecutor {
        fn new() -> Self {
            Self {
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl Executor for DryRunTrackingExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            dry_run: bool,
        ) -> ResourceResult {
            let name = params
                .get("name")
                .map(|s| s.as_str())
                .unwrap_or("unknown");
            self.calls
                .lock()
                .unwrap()
                .push((name.to_string(), dry_run));

            ResourceResult {
                module_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action: Action::None,
                changed: false,
                error: None,
                duration_ms: 1,
            }
        }
    }

    fn make_module_with_notify(
        name: &str,
        resources: Vec<(&str, &str)>,
        notify: Vec<&str>,
    ) -> Module {
        Module {
            name: name.to_string(),
            resource_type: String::new(),
            resources: resources
                .into_iter()
                .map(|(rname, rtype)| Resource {
                    name: rname.to_string(),
                    r#type: rtype.to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), rname.to_string());
                        p
                    },
                    when: String::new(),
                })
                .collect(),
            depends_on: vec![],
            notify: notify.into_iter().map(|s| s.to_string()).collect(),
            subscribes: vec![],
        }
    }

    fn make_module_with_subscribes(
        name: &str,
        resources: Vec<(&str, &str)>,
        subscribes: Vec<&str>,
    ) -> Module {
        Module {
            name: name.to_string(),
            resource_type: String::new(),
            resources: resources
                .into_iter()
                .map(|(rname, rtype)| Resource {
                    name: rname.to_string(),
                    r#type: rtype.to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), rname.to_string());
                        p
                    },
                    when: String::new(),
                })
                .collect(),
            depends_on: vec![],
            notify: vec![],
            subscribes: subscribes.into_iter().map(|s| s.to_string()).collect(),
        }
    }

    /// Module A (config file) changes -> notifies Module B (service).
    /// B should be re-run in the second pass with _triggered=true.
    #[test]
    fn converge_notify_triggers_subscriber() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::Updated, true),   // A changes
            ("app-service", Action::None, false),       // B no-op first pass
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        // Module A notifies Module B.
        let modules = vec![
            make_module_with_notify("A", vec![("config-file", "mock")], vec!["B"]),
            make_module("B", vec![("app-service", "mock")]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts);

        let recorded = calls.lock().unwrap();
        // First pass: config-file (not triggered), app-service (not triggered).
        // Second pass: app-service (triggered).
        assert_eq!(recorded.len(), 3, "expected 3 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
        assert_eq!(recorded[2], ("app-service".to_string(), true));

        // The final results should have 2 entries (one per resource).
        assert_eq!(results.len(), 2);

        // config-file should show as changed.
        let config_result = results.iter().find(|r| r.resource_name == "config-file").unwrap();
        assert!(config_result.changed);
    }

    /// Module A does NOT change -> Module B should NOT be re-run.
    #[test]
    fn converge_no_change_no_notify() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::None, false),   // A no change
            ("app-service", Action::None, false),   // B no-op
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            make_module_with_notify("A", vec![("config-file", "mock")], vec!["B"]),
            make_module("B", vec![("app-service", "mock")]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        converge(&modules, &executors, &mut cache, &opts);

        let recorded = calls.lock().unwrap();
        // Only first pass: each resource called once.
        assert_eq!(recorded.len(), 2, "expected 2 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
    }

    /// Module B subscribes to Module A. When A changes, B should be
    /// re-run with _triggered=true (subscribes is the inverse of notify).
    #[test]
    fn converge_subscribes_triggers_on_change() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::Updated, true),   // A changes
            ("app-service", Action::None, false),      // B no-op first pass
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        // B subscribes to A (instead of A notifying B).
        let modules = vec![
            make_module("A", vec![("config-file", "mock")]),
            make_module_with_subscribes("B", vec![("app-service", "mock")], vec!["A"]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts);

        let recorded = calls.lock().unwrap();
        // First pass: config-file, app-service. Second pass: app-service (triggered).
        assert_eq!(recorded.len(), 3, "expected 3 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
        assert_eq!(recorded[2], ("app-service".to_string(), true));

        assert_eq!(results.len(), 2);
    }

    /// If the notified resource already changed in the first pass, it
    /// should NOT be re-run (line 130 optimization).
    #[test]
    fn converge_changed_notified_resource_not_rerun() {
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::Updated, true),     // A changes
            ("app-service", Action::Updated, true),     // B also changes in first pass
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            make_module_with_notify("A", vec![("config-file", "mock")], vec!["B"]),
            make_module("B", vec![("app-service", "mock")]),
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        converge(&modules, &executors, &mut cache, &opts);

        let recorded = calls.lock().unwrap();
        // B already changed, so second pass skips it. Only 2 calls total.
        assert_eq!(recorded.len(), 2, "expected 2 calls, got: {:?}", *recorded);
        assert_eq!(recorded[0], ("config-file".to_string(), false));
        assert_eq!(recorded[1], ("app-service".to_string(), false));
    }

    /// dry_run=true should propagate to all executor calls.
    #[test]
    fn converge_dry_run_propagates_to_all() {
        let exec = DryRunTrackingExecutor::new();
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![make_module(
            "test-mod",
            vec![("res1", "mock"), ("res2", "mock"), ("res3", "mock")],
        )];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: true,
            module_filter: None,
            run_id: "test".into(),
        };

        converge(&modules, &executors, &mut cache, &opts);

        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 3);
        // Every call should have dry_run=true.
        for (name, was_dry_run) in recorded.iter() {
            assert!(
                *was_dry_run,
                "resource {:?} should have received dry_run=true",
                name
            );
        }
    }

    /// Result cache prevents re-execution on second converge run.
    /// Uses "file" resource type since the cache only stores cacheable types.
    #[test]
    fn converge_result_cache_prevents_reexecution() {
        // Use "file" type so the cache actually stores results.
        let exec = TrackingExecutor::new(vec![
            ("config-file", Action::None, false), // no-op: cacheable
        ]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("file".to_string(), Box::new(exec));

        let modules = vec![{
            Module {
                name: "cache-test".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "config-file".to_string(),
                    r#type: "file".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "config-file".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            }
        }];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        // First converge: executor called, result cached.
        let results1 = converge(&modules, &executors, &mut cache, &opts);
        assert_eq!(results1.len(), 1);
        assert!(results1[0].error.is_none());

        let count_after_first = calls.lock().unwrap().len();
        assert_eq!(count_after_first, 1, "first converge should call executor once");

        // Second converge: cache hit, executor NOT called again.
        let results2 = converge(&modules, &executors, &mut cache, &opts);
        assert_eq!(results2.len(), 1);
        assert!(results2[0].error.is_none());

        let count_after_second = calls.lock().unwrap().len();
        assert_eq!(
            count_after_second, 1,
            "second converge should hit cache, not call executor again"
        );
    }
}
