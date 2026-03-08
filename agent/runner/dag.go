// Package runner provides the convergence runner, DAG ordering, and supporting utilities.
package runner

import (
	"fmt"
	"sort"
	"strings"

	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
)

// ResourceNode represents a single resource in the execution DAG.
type ResourceNode struct {
	Name       string
	Type       string
	Params     map[string]string
	DependsOn  []string
	Notify     []string
	Subscribes []string
	ModuleName string
	When       string
}

// BuildDAG flattens proto modules into a list of ResourceNodes, normalizes
// subscribes into notify relationships, and returns the sorted resource list
// along with a notify map (resource name -> list of names to notify on change).
func BuildDAG(modules []*pb.Module) ([]ResourceNode, map[string][]string, error) {
	if len(modules) == 0 {
		return nil, map[string][]string{}, nil
	}

	var nodes []ResourceNode

	// Map module name -> list of resource names in that module
	moduleResources := map[string][]string{}

	for _, mod := range modules {
		for _, res := range mod.GetResources() {
			node := ResourceNode{
				Name:       res.GetName(),
				Type:       res.GetType(),
				Params:     res.GetParams(),
				ModuleName: mod.GetName(),
				Notify:     copyStrings(mod.GetNotify()),
				Subscribes: copyStrings(mod.GetSubscribes()),
				When:       res.GetWhen(),
			}

			// Module-level depends_on references module names. We need to
			// resolve these to resource names after all nodes are collected.
			// For now store the raw module-level depends_on.
			node.DependsOn = copyStrings(mod.GetDependsOn())

			nodes = append(nodes, node)
			moduleResources[mod.GetName()] = append(moduleResources[mod.GetName()], res.GetName())
		}
	}

	// Resolve module-level DependsOn: replace module names with resource names.
	// Each resource in a module depends on ALL resources of the depended-on module.
	for i := range nodes {
		var resolved []string
		for _, dep := range nodes[i].DependsOn {
			if resNames, ok := moduleResources[dep]; ok {
				resolved = append(resolved, resNames...)
			} else {
				// Not a module name -- keep as-is (might be a resource name).
				resolved = append(resolved, dep)
			}
		}
		nodes[i].DependsOn = resolved
	}

	// Build notify map.
	notifyMap := map[string][]string{}

	// First, resolve module-level Notify: for each resource in the notifying module,
	// add all resources from the notified module as notify targets.
	for i := range nodes {
		for _, target := range nodes[i].Notify {
			if resNames, ok := moduleResources[target]; ok {
				for _, srcRes := range moduleResources[nodes[i].ModuleName] {
					notifyMap[srcRes] = appendUnique(notifyMap[srcRes], resNames...)
				}
			}
		}
	}

	// Normalize subscribes -> notify: if resource B subscribes to module A,
	// all resources in A should notify all resources in B's module.
	for i := range nodes {
		for _, sub := range nodes[i].Subscribes {
			if resNames, ok := moduleResources[sub]; ok {
				for _, subRes := range resNames {
					bResources := moduleResources[nodes[i].ModuleName]
					notifyMap[subRes] = appendUnique(notifyMap[subRes], bResources...)
				}
			}
		}
	}

	return nodes, notifyMap, nil
}

// TopoSort performs a topological sort of resource nodes using Kahn's algorithm.
// Returns an error if a circular dependency is detected or if a dependency
// references an unknown resource.
func TopoSort(nodes []ResourceNode) ([]ResourceNode, error) {
	if len(nodes) == 0 {
		return nil, nil
	}

	// Build name -> node index map.
	nameIndex := map[string]int{}
	for i, n := range nodes {
		nameIndex[n.Name] = i
	}

	// Validate all dependencies exist.
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			if _, ok := nameIndex[dep]; !ok {
				return nil, fmt.Errorf("resource %q depends on unknown resource %q", n.Name, dep)
			}
		}
	}

	// Compute in-degrees and adjacency list.
	inDegree := make([]int, len(nodes))
	// adj[i] = list of node indices that depend on node i (i -> adj[i])
	adj := make([][]int, len(nodes))
	for i, n := range nodes {
		for _, dep := range n.DependsOn {
			depIdx := nameIndex[dep]
			adj[depIdx] = append(adj[depIdx], i)
			inDegree[i]++
		}
	}

	// Start with zero-in-degree nodes, maintaining input order for stability.
	var queue []int
	for i := range nodes {
		if inDegree[i] == 0 {
			queue = append(queue, i)
		}
	}

	var sorted []ResourceNode
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		sorted = append(sorted, nodes[idx])

		// Collect neighbors whose in-degree drops to zero, then sort for stability.
		var newZero []int
		for _, neighbor := range adj[idx] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				newZero = append(newZero, neighbor)
			}
		}
		// Sort new zero-degree nodes by their original index for stable ordering.
		sort.Ints(newZero)
		queue = append(queue, newZero...)
	}

	if len(sorted) != len(nodes) {
		// Cycle detected: find all nodes still with in-degree > 0.
		var cycleNodes []string
		for i, deg := range inDegree {
			if deg > 0 {
				cycleNodes = append(cycleNodes, nodes[i].Name)
			}
		}
		return nil, fmt.Errorf("circular dependency detected among resources: [%s]", strings.Join(cycleNodes, ", "))
	}

	return sorted, nil
}

// needsTopoSort returns true if any node has dependencies that require
// topological sorting. When no depends_on relationships exist, resources
// can be executed in declaration order, saving the sort overhead.
func needsTopoSort(nodes []ResourceNode) bool {
	for i := range nodes {
		if len(nodes[i].DependsOn) > 0 {
			return true
		}
	}
	return false
}

// copyStrings returns a copy of a string slice (nil-safe).
func copyStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// appendUnique appends values to a slice, skipping duplicates.
func appendUnique(slice []string, vals ...string) []string {
	existing := map[string]bool{}
	for _, s := range slice {
		existing[s] = true
	}
	for _, v := range vals {
		if !existing[v] {
			slice = append(slice, v)
			existing[v] = true
		}
	}
	return slice
}
