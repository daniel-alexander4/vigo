// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Elastic-2.0

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

/// Resource types that edit files and should participate in file transactions.
const FILE_EDITING_TYPES: &[&str] = &[
    "file", "file_line", "blockinfile", "ini", "json_file", "replace", "field_edit",
];

/// Tracks file snapshots for rollback on resource execution failure.
///
/// Before a file-editing resource executes, capture the file's original content.
/// If the resource errors, restore the file from the snapshot.
struct FileTransactionTracker {
    snapshots: HashMap<String, Option<String>>,
}

impl FileTransactionTracker {
    fn new() -> Self {
        Self {
            snapshots: HashMap::new(),
        }
    }

    /// Capture original content for `path` if not already captured.
    fn ensure_snapshot(&mut self, path: &str) {
        if self.snapshots.contains_key(path) {
            return;
        }
        let content = std::fs::read_to_string(path).ok();
        self.snapshots.insert(path.to_string(), content);
    }

    /// Restore `path` to its original content (or delete if it didn't exist).
    fn rollback(&self, path: &str) {
        if let Some(original) = self.snapshots.get(path) {
            match original {
                Some(content) => {
                    let _ = std::fs::write(path, content);
                }
                None => {
                    let _ = std::fs::remove_file(path);
                }
            }
        }
    }
}

/// Extract the target file path from a resource node's params.
fn extract_target_path(node: &ResourceNode) -> Option<String> {
    if node.resource_type == "file" {
        node.params.get("targetpath").cloned()
    } else {
        node.params.get("path").cloned()
    }
    .filter(|s| !s.is_empty())
}

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
    traits: &serde_json::Value,
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
                ..Default::default()
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
                ..Default::default()
                }];
            }
        }
    } else {
        nodes
    };

    info!(resources = ordered.len(), "executing convergence run");

    // Track which resources changed (for notify triggers).
    let mut changed_resources: HashSet<String> = HashSet::new();
    // Track which resources failed (for depends_on failure propagation).
    let mut failed_resources: HashSet<String> = HashSet::new();
    // Track resources that need to be re-run due to notify.
    let mut notified: HashSet<String> = HashSet::new();
    let mut results: Vec<ResourceResult> = Vec::new();
    // File transaction tracking for rollback on error.
    let mut tracker = FileTransactionTracker::new();

    // First pass: execute all resources.
    for node in &ordered {
        // Skip if any dependency failed — cascading errors are noise.
        if let Some(failed_dep) = node.depends_on.iter().find(|d| failed_resources.contains(d.as_str())) {
            warn!(
                resource = %node.name,
                failed_dependency = %failed_dep,
                "skipping resource — dependency failed"
            );
            failed_resources.insert(node.name.clone());
            results.push(ResourceResult {
                module_name: node.module_name.clone(),
                resource_type: node.resource_type.clone(),
                resource_name: node.name.clone(),
                action: Action::Skipped,
                changed: false,
                error: Some(format!("dependency {:?} failed", failed_dep)),
                duration_ms: 0,
                ..Default::default()
            });
            continue;
        }

        // Snapshot file before editing (for rollback).
        if !opts.dry_run && FILE_EDITING_TYPES.contains(&node.resource_type.as_str()) {
            if let Some(path) = extract_target_path(node) {
                tracker.ensure_snapshot(&path);
            }
        }

        // watch_secret resources should run on first convergence (no cached
        // result) so initial secrets like passwords get applied. On subsequent
        // cycles they skip unless the server sets _secret_triggered=true.
        let exec_node;
        let node_ref = if node.params.get("_watch_secret").map(|s| s == "true").unwrap_or(false)
            && !cache.has_run(&node.name)
        {
            let mut params = node.params.clone();
            params.insert("_triggered".to_string(), "true".to_string());
            exec_node = ResourceNode {
                params,
                ..node.clone()
            };
            &exec_node
        } else {
            node
        };

        let mut result = execute_resource(node_ref, executors, cache, opts.dry_run, &opts.run_id, traits);
        result.module_name = node.module_name.clone();

        // Mark resource as seen (for watch_secret first-run detection).
        if result.error.is_none() && result.action != Action::Skipped {
            cache.mark_seen(&node.name);
        }

        // Rollback file on error.
        if result.error.is_some() && !opts.dry_run {
            if FILE_EDITING_TYPES.contains(&node.resource_type.as_str()) {
                if let Some(path) = extract_target_path(node) {
                    warn!(resource = %node.name, path = %path, "rolling back file after error");
                    tracker.rollback(&path);
                }
            }
        }

        // Track failures for dependency propagation.
        if result.error.is_some() {
            failed_resources.insert(node.name.clone());
        }

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

            let mut result = execute_resource(&triggered_node, executors, cache, opts.dry_run, &opts.run_id, traits);
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
    traits: &serde_json::Value,
) -> ResourceResult {
    // Check when expression (agent-side builtins).
    if !node.when.is_empty() {
        match crate::policy::when::parse(&node.when) {
            Ok(expr) => {
                // Evaluate with builtin function resolver and no var substitution
                // (vars already resolved by eval_bundle).
                let var_fn = |_name: &str| false;
                let func_fn = |name: &str, args: &[String]| {
                    crate::policy::when::evaluate_builtin(name, args, traits)
                };
                match crate::policy::when::eval_with_funcs(&expr, &var_fn, Some(&func_fn)) {
                    Ok(false) => {
                        return ResourceResult {
                            module_name: String::new(),
                            resource_type: node.resource_type.clone(),
                            resource_name: node.name.clone(),
                            action: Action::Skipped,
                            changed: false,
                            error: None,
                            duration_ms: 0,
                ..Default::default()
                        };
                    }
                    Err(e) => {
                        // Fail-safe: skip the resource on eval error (unknown
                        // builtins or wrong arity) rather than executing it.
                        warn!(
                            resource = %node.name,
                            error = %e,
                            "when expression eval error, skipping resource (fail-safe)"
                        );
                        return ResourceResult {
                            module_name: String::new(),
                            resource_type: node.resource_type.clone(),
                            resource_name: node.name.clone(),
                            action: Action::Skipped,
                            changed: false,
                            error: None,
                            duration_ms: 0,
                ..Default::default()
                        };
                    }
                    Ok(true) => {} // proceed
                }
            }
            Err(e) => {
                // Fail-safe: skip on parse error too.
                warn!(
                    resource = %node.name,
                    error = %e,
                    "when expression parse error, skipping resource (fail-safe)"
                );
                return ResourceResult {
                    module_name: String::new(),
                    resource_type: node.resource_type.clone(),
                    resource_name: node.name.clone(),
                    action: Action::Skipped,
                    changed: false,
                    error: None,
                    duration_ms: 0,
                ..Default::default()
                };
            }
        }
    }

    // Self-protection guardrails — non-configurable, enforced regardless of server policy.
    if let Some(violation) = crate::guardrails::check_resource(
        &node.name,
        &node.resource_type,
        &node.params,
    ) {
        warn!(
            resource = %node.name,
            resource_type = %node.resource_type,
            reason = %violation.reason,
            "GUARDRAIL BLOCKED resource execution"
        );
        return ResourceResult {
            module_name: String::new(),
            resource_type: node.resource_type.clone(),
            resource_name: node.name.clone(),
            action: Action::None,
            changed: false,
            error: Some(format!("guardrail violation: {}", violation.reason)),
            duration_ms: 0,
                ..Default::default()
        };
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
                ..Default::default()
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

    // Determine execution timeout.
    let default_timeout: u64 = match node.resource_type.as_str() {
        "exec" | "archive" | "custom" => 300,
        _ => 60,
    };
    let timeout_secs: u64 = node
        .params
        .get("timeout")
        .and_then(|v| v.parse().ok())
        .unwrap_or(default_timeout);

    // Execute with timeout using a scoped thread + channel.
    let (tx, rx) = std::sync::mpsc::channel();
    let result = std::thread::scope(|s| {
        s.spawn(|| {
            let r = executor.execute(&params, dry_run);
            let _ = tx.send(r);
        });
        match rx.recv_timeout(std::time::Duration::from_secs(timeout_secs)) {
            Ok(r) => r,
            Err(_) => ResourceResult {
                module_name: String::new(),
                resource_type: node.resource_type.clone(),
                resource_name: node.name.clone(),
                action: Action::None,
                changed: false,
                error: Some(format!("execution timed out after {}s", timeout_secs)),
                duration_ms: timeout_secs as i32 * 1000,
                ..Default::default()
            },
        }
    });

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

    // Cross-platform executors.
    executors.insert("file".to_string(), Box::new(file::FileExecutor::new(snapshot_mgr)));
    executors.insert("directory".to_string(), Box::new(directory::DirectoryExecutor::new()));
    executors.insert("symlink".to_string(), Box::new(symlink::SymlinkExecutor::new()));
    executors.insert("exec".to_string(), Box::new(exec::ExecExecutor::new()));
    executors.insert("process".to_string(), Box::new(process::ProcessExecutor::new()));
    executors.insert("file_line".to_string(), Box::new(file_line::FileLineExecutor));
    executors.insert("host".to_string(), Box::new(host::HostExecutor::new()));
    executors.insert("sshkey".to_string(), Box::new(sshkey::SshKeyExecutor));
    executors.insert("ini".to_string(), Box::new(ini::IniExecutor));
    executors.insert("json_file".to_string(), Box::new(json_file::JsonFileExecutor));
    executors.insert("blockinfile".to_string(), Box::new(blockinfile::BlockInFileExecutor));
    executors.insert("archive".to_string(), Box::new(archive::ArchiveExecutor::new()));
    executors.insert("repository".to_string(), Box::new(repository::RepositoryExecutor::for_os(os_family)));
    executors.insert("git".to_string(), Box::new(git::GitExecutor::new()));
    executors.insert("reboot".to_string(), Box::new(reboot::RebootExecutor::new()));
    executors.insert("pip".to_string(), Box::new(pip::PipExecutor::new()));
    executors.insert("gem".to_string(), Box::new(gem::GemExecutor::new()));
    executors.insert("npm".to_string(), Box::new(npm::NpmExecutor::new()));
    executors.insert("docker_compose".to_string(), Box::new(docker_compose::DockerComposeExecutor::new()));
    executors.insert("docker_container".to_string(), Box::new(docker_container::DockerContainerExecutor::new()));
    executors.insert("docker_image".to_string(), Box::new(docker_image::DockerImageExecutor::new()));
    executors.insert("replace".to_string(), Box::new(replace::ReplaceExecutor));
    executors.insert("field_edit".to_string(), Box::new(field_edit::FieldEditExecutor));

    // Network device executors (SSH-backed, used for gateway proxy convergence).
    executors.insert("ssh_exec".to_string(), Box::new(ssh_exec::SshExecExecutor::new()));
    executors.insert("network_config".to_string(), Box::new(network_config::NetworkConfigExecutor::new()));
    executors.insert("cisco_vlan".to_string(), Box::new(cisco_vlan::CiscoVlanExecutor::new()));
    executors.insert("cisco_interface".to_string(), Box::new(cisco_interface::CiscoInterfaceExecutor::new()));
    executors.insert("cisco_acl".to_string(), Box::new(cisco_acl::CiscoAclExecutor::new()));
    executors.insert("junos_interface".to_string(), Box::new(junos_interface::JunosInterfaceExecutor::new()));
    executors.insert("network_ntp".to_string(), Box::new(network_ntp::NetworkNtpExecutor::new()));
    executors.insert("network_syslog".to_string(), Box::new(network_syslog::NetworkSyslogExecutor::new()));
    executors.insert("network_snmp".to_string(), Box::new(network_snmp::NetworkSnmpExecutor::new()));
    executors.insert("network_user".to_string(), Box::new(network_user::NetworkUserExecutor::new()));
    executors.insert("network_banner".to_string(), Box::new(network_banner::NetworkBannerExecutor::new()));

    // Linux-only executors.
    #[cfg(target_os = "linux")]
    {
        executors.insert("service".to_string(), Box::new(service::ServiceExecutor::new()));
        executors.insert("systemd_dropin".to_string(), Box::new(systemd_dropin::SystemdDropinExecutor::new()));
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));
        executors.insert("hostname".to_string(), Box::new(hostname::HostnameExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone::TimezoneExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl::SysctlExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall::FirewallExecutor::new()));
        executors.insert("kernel_module".to_string(), Box::new(kernel_module::KernelModuleExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount::MountExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap::SwapExecutor::new()));
        executors.insert("tmpfile".to_string(), Box::new(tmpfile::TmpfileExecutor::new()));
        executors.insert("logrotate".to_string(), Box::new(logrotate::LogrotateExecutor));
        executors.insert("alternatives".to_string(), Box::new(alternatives::AlternativesExecutor::new()));
        executors.insert("selinux".to_string(), Box::new(selinux::SelinuxExecutor::new()));

        // Linux package manager detection.
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
    }

    // macOS-native executors.
    #[cfg(target_os = "macos")]
    {
        executors.insert("service".to_string(), Box::new(service_macos::ServiceMacosExecutor::new()));
        executors.insert("user".to_string(), Box::new(user_macos::UserMacosExecutor::new()));
        executors.insert("group".to_string(), Box::new(group_macos::GroupMacosExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_macos::HostnameMacosExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_macos::TimezoneMacosExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

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
    }

    // FreeBSD-native executors.
    #[cfg(target_os = "freebsd")]
    {
        executors.insert("service".to_string(), Box::new(service_freebsd::ServiceFreebsdExecutor::new()));
        executors.insert("user".to_string(), Box::new(user_freebsd::UserFreebsdExecutor::new()));
        executors.insert("group".to_string(), Box::new(group_freebsd::GroupFreebsdExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_bsd::TimezoneBsdExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("kernel_module".to_string(), Box::new(kernel_module_freebsd::KernelModuleFreebsdExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

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
    }

    // OpenBSD-native executors.
    #[cfg(target_os = "openbsd")]
    {
        executors.insert("service".to_string(), Box::new(service_openbsd::ServiceOpenbsdExecutor::new()));
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_bsd::TimezoneBsdExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

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
    }

    // NetBSD-native executors.
    #[cfg(target_os = "netbsd")]
    {
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_bsd::TimezoneBsdExecutor::new()));
        executors.insert("sysctl".to_string(), Box::new(sysctl_bsd::SysctlBsdExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_pf::FirewallPfExecutor::new()));
        executors.insert("mount".to_string(), Box::new(mount_bsd::MountBsdExecutor::new()));
        executors.insert("swap".to_string(), Box::new(swap_bsd::SwapBsdExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

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
    }

    // illumos-native executors.
    #[cfg(target_os = "illumos")]
    {
        executors.insert("service".to_string(), Box::new(service_illumos::ServiceIllumosExecutor::new()));
        executors.insert("user".to_string(), Box::new(user::UserExecutor::new()));
        executors.insert("group".to_string(), Box::new(group::GroupExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_bsd::HostnameBsdExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_illumos::TimezoneIllumosExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron::CronExecutor));

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
    }

    // Windows-native executors.
    #[cfg(windows)]
    {
        executors.insert("service".to_string(), Box::new(service_windows::ServiceWindowsExecutor::new()));
        executors.insert("user".to_string(), Box::new(user_windows::UserWindowsExecutor::new()));
        executors.insert("package".to_string(), Box::new(package_windows::PackageWindowsExecutor::new()));
        executors.insert("cron".to_string(), Box::new(cron_windows::CronWindowsExecutor::new()));
        executors.insert("hostname".to_string(), Box::new(hostname_windows::HostnameWindowsExecutor::new()));
        executors.insert("timezone".to_string(), Box::new(timezone_windows::TimezoneWindowsExecutor::new()));
        executors.insert("firewall".to_string(), Box::new(firewall_windows::FirewallWindowsExecutor::new()));
    }

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

        // Check if executable (mode bits on Unix, extension on Windows).
        if !crate::platform::permissions::is_executable(&path) {
            continue;
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
        let allow_downgrade = params
            .get("allow_downgrade")
            .map(|s| s == "true")
            .unwrap_or(false);

        // Support comma-separated package names.
        let names: Vec<&str> = name_param
            .split(',')
            .map(|s| s.trim())
            .filter(|s| !s.is_empty())
            .collect();

        for name in &names {
            let res = self.execute_one_package(name, state, version, dry_run, allow_downgrade);
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
        allow_downgrade: bool,
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
                        let change_result = if allow_downgrade {
                            self.pkg_mgr.downgrade(name, version)
                        } else {
                            self.pkg_mgr.upgrade(name, version)
                        };
                        if let Err(e) = change_result {
                            let verb = if allow_downgrade { "downgrade" } else { "upgrade" };
                            result.error =
                                Some(format!("{} package {} to {}: {}", verb, name, version, e));
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
                ..Default::default()
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

        let results = converge(&[], &executors, &mut cache, &opts, &serde_json::json!({}));
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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
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
                ..Default::default()
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
                ..Default::default()
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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

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

        converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

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

        converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

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

        converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

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
        let results1 = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
        assert_eq!(results1.len(), 1);
        assert!(results1[0].error.is_none());

        let count_after_first = calls.lock().unwrap().len();
        assert_eq!(count_after_first, 1, "first converge should call executor once");

        // Second converge: cache hit, executor NOT called again.
        let results2 = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
        assert_eq!(results2.len(), 1);
        assert!(results2[0].error.is_none());

        let count_after_second = calls.lock().unwrap().len();
        assert_eq!(
            count_after_second, 1,
            "second converge should hit cache, not call executor again"
        );
    }

    // ── FileTransactionTracker tests ──────────────────────────────────

    #[test]
    fn file_transaction_rollback_on_error() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("rollback.conf");
        let original = "original content\n";
        std::fs::write(&path, original).unwrap();

        let mut tracker = FileTransactionTracker::new();
        tracker.ensure_snapshot(path.to_str().unwrap());

        // Simulate a failed write that corrupts the file.
        std::fs::write(&path, "corrupted\n").unwrap();

        // Rollback should restore original.
        tracker.rollback(path.to_str().unwrap());
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, original);
    }

    #[test]
    fn file_transaction_no_rollback_on_success() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("success.conf");
        std::fs::write(&path, "original\n").unwrap();

        let mut tracker = FileTransactionTracker::new();
        tracker.ensure_snapshot(path.to_str().unwrap());

        // Simulate successful write.
        let new_content = "updated\n";
        std::fs::write(&path, new_content).unwrap();

        // No rollback — file stays updated.
        let content = std::fs::read_to_string(&path).unwrap();
        assert_eq!(content, new_content);
    }

    #[test]
    fn file_transaction_different_files_independent() {
        let dir = tempfile::tempdir().unwrap();
        let path_a = dir.path().join("a.conf");
        let path_b = dir.path().join("b.conf");
        std::fs::write(&path_a, "a-original\n").unwrap();
        std::fs::write(&path_b, "b-original\n").unwrap();

        let mut tracker = FileTransactionTracker::new();
        tracker.ensure_snapshot(path_a.to_str().unwrap());
        tracker.ensure_snapshot(path_b.to_str().unwrap());

        // Modify both files.
        std::fs::write(&path_a, "a-updated\n").unwrap();
        std::fs::write(&path_b, "b-corrupted\n").unwrap();

        // Only rollback file B.
        tracker.rollback(path_b.to_str().unwrap());

        // A stays updated, B is restored.
        assert_eq!(std::fs::read_to_string(&path_a).unwrap(), "a-updated\n");
        assert_eq!(std::fs::read_to_string(&path_b).unwrap(), "b-original\n");
    }

    // ── depends_on failure propagation tests ─────────────────────────

    /// Executor that fails for specific resource names and succeeds for others.
    struct FailableExecutor {
        /// Resource names that should return an error.
        fail_names: HashSet<String>,
        calls: Arc<Mutex<Vec<String>>>,
    }

    impl FailableExecutor {
        fn new(fail_names: Vec<&str>) -> Self {
            Self {
                fail_names: fail_names.into_iter().map(|s| s.to_string()).collect(),
                calls: Arc::new(Mutex::new(Vec::new())),
            }
        }
    }

    impl Executor for FailableExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            let name = params.get("name").map(|s| s.as_str()).unwrap_or("unknown");
            self.calls.lock().unwrap().push(name.to_string());

            if self.fail_names.contains(name) {
                ResourceResult {
                    module_name: String::new(),
                    resource_type: "mock".to_string(),
                    resource_name: name.to_string(),
                    action: Action::None,
                    changed: false,
                    error: Some(format!("{} failed", name)),
                    duration_ms: 1,
                ..Default::default()
                }
            } else {
                ResourceResult {
                    module_name: String::new(),
                    resource_type: "mock".to_string(),
                    resource_name: name.to_string(),
                    action: Action::Created,
                    changed: true,
                    error: None,
                    duration_ms: 1,
                ..Default::default()
                }
            }
        }
    }

    /// A → B → C chain. A fails → B and C should be skipped.
    #[test]
    fn depends_on_failure_skips_dependents() {
        let exec = FailableExecutor::new(vec!["install-nginx"]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            Module {
                name: "pkg".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "install-nginx".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "install-nginx".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "config".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "nginx-conf".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "nginx-conf".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["pkg".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "svc".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "nginx-svc".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "nginx-svc".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["config".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

        // Only install-nginx should have been executed.
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 1, "only failed resource should execute, got: {:?}", *recorded);
        assert_eq!(recorded[0], "install-nginx");

        // All 3 results should be present.
        assert_eq!(results.len(), 3);

        // install-nginx: error.
        let r0 = results.iter().find(|r| r.resource_name == "install-nginx").unwrap();
        assert!(r0.error.is_some());

        // nginx-conf: skipped due to dependency failure.
        let r1 = results.iter().find(|r| r.resource_name == "nginx-conf").unwrap();
        assert_eq!(r1.action, Action::Skipped);
        assert!(r1.error.as_ref().unwrap().contains("dependency"));

        // nginx-svc: skipped (transitive — nginx-conf was skipped/failed).
        let r2 = results.iter().find(|r| r.resource_name == "nginx-svc").unwrap();
        assert_eq!(r2.action, Action::Skipped);
        assert!(r2.error.as_ref().unwrap().contains("dependency"));
    }

    /// A fails, B depends on A, C has no deps. C should still execute.
    #[test]
    fn depends_on_failure_does_not_affect_independent_resources() {
        let exec = FailableExecutor::new(vec!["broken-res"]);
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            Module {
                name: "broken".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "broken-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "broken-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "dependent".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "dep-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "dep-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["broken".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "independent".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "ok-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "ok-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

        // broken-res and ok-res executed; dep-res skipped.
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2, "got: {:?}", *recorded);
        assert!(recorded.contains(&"broken-res".to_string()));
        assert!(recorded.contains(&"ok-res".to_string()));

        // dep-res skipped.
        let dep = results.iter().find(|r| r.resource_name == "dep-res").unwrap();
        assert_eq!(dep.action, Action::Skipped);

        // ok-res succeeded.
        let ok = results.iter().find(|r| r.resource_name == "ok-res").unwrap();
        assert!(ok.error.is_none());
        assert!(ok.changed);
    }

    /// No failures → no skipping. Dependencies pass through cleanly.
    #[test]
    fn depends_on_no_failure_all_execute() {
        let exec = FailableExecutor::new(vec![]); // nothing fails
        let calls = Arc::clone(&exec.calls);

        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(exec));

        let modules = vec![
            Module {
                name: "base".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "base-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "base-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "app".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "app-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "app-res".to_string());
                        p
                    },
                    when: String::new(),
                }],
                depends_on: vec!["base".to_string()],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let mut cache = ResultCache::new(5);
        let opts = RunOptions {
            dry_run: false,
            module_filter: None,
            run_id: "test".into(),
        };

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));

        // Both executed.
        let recorded = calls.lock().unwrap();
        assert_eq!(recorded.len(), 2);

        // Both succeeded.
        assert!(results.iter().all(|r| r.error.is_none()));
        assert!(results.iter().all(|r| r.changed));
    }

    // ── Execution timeout tests ──────────────────────────────────────

    /// Executor that sleeps for a configurable duration before returning.
    struct SlowExecutor {
        sleep_ms: u64,
    }

    impl Executor for SlowExecutor {
        fn execute(
            &self,
            params: &HashMap<String, String>,
            _dry_run: bool,
        ) -> ResourceResult {
            std::thread::sleep(std::time::Duration::from_millis(self.sleep_ms));
            let name = params.get("name").map(|s| s.as_str()).unwrap_or("slow");
            ResourceResult {
                module_name: String::new(),
                resource_type: "mock".to_string(),
                resource_name: name.to_string(),
                action: Action::None,
                changed: false,
                error: None,
                duration_ms: self.sleep_ms as i32,
                ..Default::default()
            }
        }
    }

    #[test]
    fn execution_timeout_triggers_error() {
        // Use a slow executor with 5s sleep and a 1s timeout param.
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(SlowExecutor { sleep_ms: 5000 }));

        let modules = vec![{
            Module {
                name: "slow-mod".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "slow-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "slow-res".to_string());
                        p.insert("timeout".to_string(), "1".to_string());
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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
        assert_eq!(results.len(), 1);
        assert!(results[0].error.is_some());
        assert!(
            results[0].error.as_ref().unwrap().contains("timed out"),
            "expected timeout error, got: {:?}",
            results[0].error
        );
    }

    #[test]
    fn execution_within_timeout_succeeds() {
        // Executor finishes quickly, well within the timeout.
        let mut executors: HashMap<String, Box<dyn Executor>> = HashMap::new();
        executors.insert("mock".to_string(), Box::new(SlowExecutor { sleep_ms: 10 }));

        let modules = vec![{
            Module {
                name: "fast-mod".to_string(),
                resource_type: String::new(),
                resources: vec![Resource {
                    name: "fast-res".to_string(),
                    r#type: "mock".to_string(),
                    params: {
                        let mut p = HashMap::new();
                        p.insert("name".to_string(), "fast-res".to_string());
                        p.insert("timeout".to_string(), "10".to_string());
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

        let results = converge(&modules, &executors, &mut cache, &opts, &serde_json::json!({}));
        assert_eq!(results.len(), 1);
        assert!(results[0].error.is_none());
    }
}
