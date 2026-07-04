//! DAG topological sort for capability depends_on ordering.
//! Uses Kahn's algorithm with clear cycle detection.

use anyhow::{bail, Result};
use std::collections::{HashMap, HashSet, VecDeque};

/// Given a map of capability_name → depends_on list, return a topologically
/// sorted execution order. Errors on cycles.
pub fn topological_sort(deps: &HashMap<String, Vec<String>>) -> Result<Vec<String>> {
    // Build adjacency list and in-degree count.
    let mut in_degree: HashMap<&str, usize> = HashMap::new();
    let mut adjacency: HashMap<&str, Vec<&str>> = HashMap::new();

    // Initialize all nodes.
    for name in deps.keys() {
        in_degree.entry(name.as_str()).or_insert(0);
        adjacency.entry(name.as_str()).or_default();
    }

    // Build edges: if B depends_on A, then A → B (A must run before B).
    for (name, dep_list) in deps {
        for dep in dep_list {
            if !deps.contains_key(dep) {
                bail!("capability '{}' depends on '{}' which is not in the capability set", name, dep);
            }
            adjacency.entry(dep.as_str()).or_default().push(name.as_str());
            *in_degree.entry(name.as_str()).or_insert(0) += 1;
        }
    }

    // Kahn's algorithm.
    let mut queue: VecDeque<&str> = VecDeque::new();
    for (node, &degree) in &in_degree {
        if degree == 0 {
            queue.push_back(node);
        }
    }

    let mut sorted: Vec<String> = Vec::new();

    while let Some(node) = queue.pop_front() {
        sorted.push(node.to_string());
        if let Some(neighbors) = adjacency.get(node) {
            for &neighbor in neighbors {
                if let Some(deg) = in_degree.get_mut(neighbor) {
                    *deg -= 1;
                    if *deg == 0 {
                        queue.push_back(neighbor);
                    }
                }
            }
        }
    }

    if sorted.len() != deps.len() {
        // Find nodes involved in the cycle for the error message.
        let sorted_set: HashSet<&str> = sorted.iter().map(|s| s.as_str()).collect();
        let cycle_nodes: Vec<&str> = deps.keys()
            .map(|s| s.as_str())
            .filter(|n| !sorted_set.contains(n))
            .collect();
        bail!("dependency cycle detected among: {}", cycle_nodes.join(", "));
    }

    Ok(sorted)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn deps(pairs: &[(&str, &[&str])]) -> HashMap<String, Vec<String>> {
        pairs.iter()
            .map(|(k, v)| (k.to_string(), v.iter().map(|s| s.to_string()).collect()))
            .collect()
    }

    #[test]
    fn empty_graph() {
        let result = topological_sort(&deps(&[])).unwrap();
        assert!(result.is_empty());
    }

    #[test]
    fn single_node_no_deps() {
        let result = topological_sort(&deps(&[("a", &[])])).unwrap();
        assert_eq!(result, vec!["a"]);
    }

    #[test]
    fn linear_chain() {
        let result = topological_sort(&deps(&[
            ("a", &[]),
            ("b", &["a"]),
            ("c", &["b"]),
        ])).unwrap();

        let pos = |name: &str| result.iter().position(|s| s == name).unwrap();
        assert!(pos("a") < pos("b"));
        assert!(pos("b") < pos("c"));
    }

    #[test]
    fn diamond_dependency() {
        let result = topological_sort(&deps(&[
            ("a", &[]),
            ("b", &["a"]),
            ("c", &["a"]),
            ("d", &["b", "c"]),
        ])).unwrap();

        let pos = |name: &str| result.iter().position(|s| s == name).unwrap();
        assert!(pos("a") < pos("b"));
        assert!(pos("a") < pos("c"));
        assert!(pos("b") < pos("d"));
        assert!(pos("c") < pos("d"));
    }

    #[test]
    fn independent_nodes() {
        let result = topological_sort(&deps(&[
            ("a", &[]),
            ("b", &[]),
            ("c", &[]),
        ])).unwrap();
        assert_eq!(result.len(), 3);
    }

    #[test]
    fn cycle_detected() {
        let err = topological_sort(&deps(&[
            ("a", &["b"]),
            ("b", &["a"]),
        ]));
        assert!(err.is_err());
        let msg = err.unwrap_err().to_string();
        assert!(msg.contains("cycle"), "expected cycle error, got: {msg}");
    }

    #[test]
    fn three_node_cycle() {
        let err = topological_sort(&deps(&[
            ("a", &["c"]),
            ("b", &["a"]),
            ("c", &["b"]),
        ]));
        assert!(err.is_err());
        assert!(err.unwrap_err().to_string().contains("cycle"));
    }

    #[test]
    fn missing_dependency_rejected() {
        let err = topological_sort(&deps(&[
            ("a", &["nonexistent"]),
        ]));
        assert!(err.is_err());
        assert!(err.unwrap_err().to_string().contains("not in the capability set"));
    }
}
