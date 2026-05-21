// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! DAG builder and topological sorter for resource execution ordering.
//!
//! Flattens proto modules into [`ResourceNode`]s, normalizes subscribes into
//! notify relationships, and produces a topologically sorted execution order.

use crate::proto::Module;
use std::collections::{HashMap, HashSet};

/// A single resource in the execution DAG.
#[derive(Debug, Clone)]
pub struct ResourceNode {
    pub name: String,
    pub resource_type: String,
    pub params: HashMap<String, String>,
    pub depends_on: Vec<String>,
    pub notify: Vec<String>,
    pub subscribes: Vec<String>,
    pub module_name: String,
    pub when: String,
}

/// Maps a resource name → list of resource names to notify when it changes.
pub type NotifyMap = HashMap<String, Vec<String>>;

/// Build the execution DAG from proto modules.
///
/// Returns (resource_nodes, notify_map) where notify_map maps resource name →
/// list of resource names to notify when it changes.
pub fn build_dag(modules: &[Module]) -> Result<(Vec<ResourceNode>, NotifyMap), String> {
    if modules.is_empty() {
        return Ok((Vec::new(), HashMap::new()));
    }

    let mut nodes = Vec::new();
    let mut module_resources: HashMap<String, Vec<String>> = HashMap::new();

    for module in modules {
        for res in &module.resources {
            let node = ResourceNode {
                name: res.name.clone(),
                resource_type: res.r#type.clone(),
                params: res.params.clone(),
                depends_on: module.depends_on.clone(),
                notify: module.notify.clone(),
                subscribes: module.subscribes.clone(),
                module_name: module.name.clone(),
                when: res.when.clone(),
            };
            nodes.push(node);
            module_resources
                .entry(module.name.clone())
                .or_default()
                .push(res.name.clone());
        }
    }

    // Resolve module-level DependsOn: replace module names with resource names.
    for node in &mut nodes {
        let mut resolved = Vec::new();
        for dep in &node.depends_on {
            if let Some(res_names) = module_resources.get(dep) {
                resolved.extend(res_names.iter().cloned());
            } else {
                // Not a module name — keep as-is (might be a resource name).
                resolved.push(dep.clone());
            }
        }
        node.depends_on = resolved;
    }

    // Build a set of all resource names for resource-level notify/subscribes.
    let all_resource_names: HashSet<String> = nodes.iter().map(|n| n.name.clone()).collect();

    // Build notify map.
    let mut notify_map: HashMap<String, Vec<String>> = HashMap::new();

    // Resolve Notify: try module name first, then resource name.
    for node in &nodes {
        for target in &node.notify {
            if let Some(target_resources) = module_resources.get(target) {
                // Module-level: all resources in this module notify all resources in target module.
                if let Some(source_resources) = module_resources.get(&node.module_name) {
                    for src in source_resources {
                        append_unique(
                            notify_map.entry(src.clone()).or_default(),
                            target_resources,
                        );
                    }
                }
            } else if all_resource_names.contains(target) {
                // Resource-level: this resource's module resources notify the target resource.
                if let Some(source_resources) = module_resources.get(&node.module_name) {
                    let target_vec = vec![target.clone()];
                    for src in source_resources {
                        append_unique(
                            notify_map.entry(src.clone()).or_default(),
                            &target_vec,
                        );
                    }
                }
            }
        }
    }

    // Normalize subscribes → notify: try module name first, then resource name.
    for node in &nodes {
        for sub in &node.subscribes {
            if let Some(sub_resources) = module_resources.get(sub) {
                // Module-level: all resources in subscribed module notify all resources in this module.
                if let Some(subscriber_resources) = module_resources.get(&node.module_name) {
                    for sub_res in sub_resources {
                        append_unique(
                            notify_map.entry(sub_res.clone()).or_default(),
                            subscriber_resources,
                        );
                    }
                }
            } else if all_resource_names.contains(sub) {
                // Resource-level: the subscribed resource notifies all resources in this module.
                if let Some(subscriber_resources) = module_resources.get(&node.module_name) {
                    append_unique(
                        notify_map.entry(sub.clone()).or_default(),
                        subscriber_resources,
                    );
                }
            }
        }
    }

    Ok((nodes, notify_map))
}

/// Topological sort using Kahn's algorithm.
///
/// Returns error on circular dependency or unknown dependency reference.
pub fn topo_sort(nodes: Vec<ResourceNode>) -> Result<Vec<ResourceNode>, String> {
    if nodes.is_empty() {
        return Ok(Vec::new());
    }

    // Build name → index map.
    let name_index: HashMap<&str, usize> = nodes
        .iter()
        .enumerate()
        .map(|(i, n)| (n.name.as_str(), i))
        .collect();

    // Validate all dependencies exist.
    for node in &nodes {
        for dep in &node.depends_on {
            if !name_index.contains_key(dep.as_str()) {
                return Err(format!(
                    "resource {:?} depends on unknown resource {:?}",
                    node.name, dep
                ));
            }
        }
    }

    let len = nodes.len();
    let mut in_degree = vec![0usize; len];
    let mut adj: Vec<Vec<usize>> = vec![Vec::new(); len];

    for (i, node) in nodes.iter().enumerate() {
        for dep in &node.depends_on {
            let dep_idx = name_index[dep.as_str()];
            adj[dep_idx].push(i);
            in_degree[i] += 1;
        }
    }

    // Start with zero-in-degree nodes.
    let mut queue: Vec<usize> = (0..len).filter(|&i| in_degree[i] == 0).collect();

    let mut sorted = Vec::with_capacity(len);
    while let Some(idx) = queue.first().copied() {
        queue.remove(0);
        sorted.push(idx);

        // Collect neighbors whose in-degree drops to zero, sort for stability.
        let mut new_zero: Vec<usize> = Vec::new();
        for &neighbor in &adj[idx] {
            in_degree[neighbor] -= 1;
            if in_degree[neighbor] == 0 {
                new_zero.push(neighbor);
            }
        }
        new_zero.sort();
        queue.extend(new_zero);
    }

    if sorted.len() != len {
        let cycle_nodes: Vec<&str> = in_degree
            .iter()
            .enumerate()
            .filter(|(_, &deg)| deg > 0)
            .map(|(i, _)| nodes[i].name.as_str())
            .collect();
        return Err(format!(
            "circular dependency detected among resources: [{}]",
            cycle_nodes.join(", ")
        ));
    }

    // Reorder nodes by sorted indices.
    let result: Vec<ResourceNode> = sorted.into_iter().map(|i| nodes[i].clone()).collect();
    Ok(result)
}

/// Returns true if any node has dependencies requiring topological sorting.
pub fn needs_topo_sort(nodes: &[ResourceNode]) -> bool {
    nodes.iter().any(|n| !n.depends_on.is_empty())
}

/// Append values to a vec, skipping duplicates.
fn append_unique(target: &mut Vec<String>, values: &[String]) {
    let existing: HashSet<String> = target.iter().cloned().collect();
    for v in values {
        if !existing.contains(v) {
            target.push(v.clone());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{Module, Resource};

    fn make_resource(name: &str, rtype: &str) -> Resource {
        Resource {
            name: name.to_string(),
            r#type: rtype.to_string(),
            params: HashMap::new(),
            when: String::new(),
        }
    }

    #[test]
    fn topo_sort_no_deps() {
        let nodes = vec![
            ResourceNode {
                name: "a".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "b".into(),
                resource_type: "package".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "c".into(),
                resource_type: "service".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
        ];

        let sorted = topo_sort(nodes).unwrap();
        assert_eq!(sorted.len(), 3);
        assert_eq!(sorted[0].name, "a");
        assert_eq!(sorted[1].name, "b");
        assert_eq!(sorted[2].name, "c");
    }

    #[test]
    fn topo_sort_linear_chain() {
        let nodes = vec![
            ResourceNode {
                name: "c".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["b".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "a".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "b".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["a".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
        ];

        let sorted = topo_sort(nodes).unwrap();
        let names: Vec<&str> = sorted.iter().map(|n| n.name.as_str()).collect();
        assert_eq!(names, vec!["a", "b", "c"]);
    }

    #[test]
    fn topo_sort_diamond() {
        let nodes = vec![
            ResourceNode {
                name: "d".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["b".into(), "c".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "b".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["a".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "c".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["a".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "a".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
        ];

        let sorted = topo_sort(nodes).unwrap();
        let index_of: HashMap<&str, usize> = sorted
            .iter()
            .enumerate()
            .map(|(i, n)| (n.name.as_str(), i))
            .collect();

        assert!(index_of["a"] < index_of["b"]);
        assert!(index_of["a"] < index_of["c"]);
        assert!(index_of["b"] < index_of["d"]);
        assert!(index_of["c"] < index_of["d"]);
    }

    #[test]
    fn topo_sort_circular() {
        let nodes = vec![
            ResourceNode {
                name: "a".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["c".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "b".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["a".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "c".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec!["b".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
        ];

        let err = topo_sort(nodes).unwrap_err();
        assert!(err.contains("circular dependency"));
        assert!(err.contains("a"));
        assert!(err.contains("b"));
        assert!(err.contains("c"));
    }

    #[test]
    fn topo_sort_unknown_dep() {
        let nodes = vec![ResourceNode {
            name: "a".into(),
            resource_type: "file".into(),
            params: HashMap::new(),
            depends_on: vec!["x".into()],
            notify: vec![],
            subscribes: vec![],
            module_name: "m".into(),
            when: String::new(),
        }];

        let err = topo_sort(nodes).unwrap_err();
        assert!(err.contains("unknown resource"));
        assert!(err.contains("x"));
    }

    #[test]
    fn build_dag_subscribes_normalization() {
        let modules = vec![
            Module {
                name: "a".into(),
                resource_type: "file".into(),
                resources: vec![make_resource("config-file", "file")],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "b".into(),
                resource_type: "service".into(),
                resources: vec![make_resource("app-service", "service")],
                depends_on: vec!["a".into()],
                notify: vec![],
                subscribes: vec!["a".into()],
            },
        ];

        let (nodes, notify_map) = build_dag(&modules).unwrap();
        assert_eq!(nodes.len(), 2);

        // A's resource should notify B's resource.
        let targets = notify_map.get("config-file").expect("notify entry for config-file");
        assert!(targets.contains(&"app-service".to_string()));
    }

    #[test]
    fn build_dag_module_level_notify() {
        let modules = vec![
            Module {
                name: "base".into(),
                resource_type: "file".into(),
                resources: vec![make_resource("base-config", "file")],
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
            },
            Module {
                name: "app".into(),
                resource_type: "file".into(),
                resources: vec![make_resource("app-config", "file")],
                depends_on: vec!["base".into()],
                notify: vec!["svc".into()],
                subscribes: vec![],
            },
            Module {
                name: "svc".into(),
                resource_type: "service".into(),
                resources: vec![make_resource("app-svc", "service")],
                depends_on: vec!["app".into()],
                notify: vec![],
                subscribes: vec![],
            },
        ];

        let (nodes, notify_map) = build_dag(&modules).unwrap();
        assert_eq!(nodes.len(), 3);

        // app-config should notify app-svc.
        let targets = notify_map.get("app-config").expect("notify entry for app-config");
        assert!(targets.contains(&"app-svc".to_string()));
    }

    #[test]
    fn build_dag_intra_module_notify() {
        // notify: [restart-sshd] on the module — "restart-sshd" is a resource, not a module.
        let modules = vec![Module {
            name: "ssh-hardening".into(),
            resource_type: "file".into(),
            resources: vec![
                make_resource("sshd-config", "file"),
                make_resource("restart-sshd", "service"),
            ],
            depends_on: vec![],
            notify: vec!["restart-sshd".into()],
            subscribes: vec![],
        }];

        let (_, notify_map) = build_dag(&modules).unwrap();

        // sshd-config should notify restart-sshd.
        let targets = notify_map.get("sshd-config").expect("notify entry for sshd-config");
        assert!(targets.contains(&"restart-sshd".to_string()));
    }

    #[test]
    fn build_dag_intra_module_subscribes() {
        // subscribes: [sshd-config] on the module — "sshd-config" is a resource, not a module.
        let modules = vec![Module {
            name: "ssh-hardening".into(),
            resource_type: "file".into(),
            resources: vec![
                make_resource("sshd-config", "file"),
                make_resource("restart-sshd", "service"),
            ],
            depends_on: vec![],
            notify: vec![],
            subscribes: vec!["sshd-config".into()],
        }];

        let (_, notify_map) = build_dag(&modules).unwrap();

        // sshd-config should notify restart-sshd (subscribes is inverse of notify).
        let targets = notify_map.get("sshd-config").expect("notify entry for sshd-config");
        assert!(targets.contains(&"restart-sshd".to_string()));
    }

    #[test]
    fn build_dag_empty() {
        let (nodes, notify_map) = build_dag(&[]).unwrap();
        assert!(nodes.is_empty());
        assert!(notify_map.is_empty());
    }

    #[test]
    fn build_dag_multiple_resources_per_module() {
        let modules = vec![Module {
            name: "configs".into(),
            resource_type: "file".into(),
            resources: vec![
                make_resource("nginx-conf", "file"),
                make_resource("app-conf", "file"),
            ],
            depends_on: vec![],
            notify: vec![],
            subscribes: vec![],
        }];

        let (nodes, _) = build_dag(&modules).unwrap();
        assert_eq!(nodes.len(), 2);
        assert!(nodes.iter().all(|n| n.module_name == "configs"));
    }

    #[test]
    fn needs_topo_sort_no_deps() {
        let nodes = vec![
            ResourceNode {
                name: "a".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
        ];
        assert!(!needs_topo_sort(&nodes));
    }

    #[test]
    fn needs_topo_sort_with_deps() {
        let nodes = vec![
            ResourceNode {
                name: "a".into(),
                resource_type: "file".into(),
                params: HashMap::new(),
                depends_on: vec![],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
            ResourceNode {
                name: "b".into(),
                resource_type: "package".into(),
                params: HashMap::new(),
                depends_on: vec!["a".into()],
                notify: vec![],
                subscribes: vec![],
                module_name: "m".into(),
                when: String::new(),
            },
        ];
        assert!(needs_topo_sort(&nodes));
    }
}
