package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/isogov/isogov/agent/modules"
	pb "github.com/isogov/isogov/gen/proto/isogov"
)

// Runner executes resources in DAG order with notify/subscribe triggers
// and when: conditional evaluation.
type Runner struct {
	// Executors maps resource type names to their executor implementations.
	Executors map[string]modules.Executor

	// Facts are the collected node facts used for when: condition evaluation.
	Facts map[string]any

	// DryRun passes through to executors and disables the notify pass.
	DryRun bool

	// Module filters execution to only resources from the named module.
	// If empty, all resources are executed.
	Module string
}

// NewRunner creates a Runner with the given configuration.
func NewRunner(executors map[string]modules.Executor, facts map[string]any, dryRun bool, module string) *Runner {
	return &Runner{
		Executors: executors,
		Facts:     facts,
		DryRun:    dryRun,
		Module:    module,
	}
}

// Run executes all resources from the provided proto modules in DAG topological
// order. It evaluates when: conditions, records results for each resource, and
// performs a notify pass after the main execution pass for changed resources.
//
// The function returns a slice of ResourceResult for all resources (main pass
// + notify pass). It returns an error only for fatal DAG construction failures
// (e.g., circular dependencies). Individual resource errors are recorded in
// their results and do not abort the run.
func (r *Runner) Run(ctx context.Context, protoModules []*pb.Module) ([]modules.ResourceResult, error) {
	// Build DAG: flatten modules, normalize subscribes -> notify.
	nodes, notifyMap, err := BuildDAG(protoModules)
	if err != nil {
		return nil, fmt.Errorf("build DAG: %w", err)
	}

	// Topological sort.
	sorted, err := TopoSort(nodes)
	if err != nil {
		return nil, fmt.Errorf("topological sort: %w", err)
	}

	// Apply module filter if set.
	if r.Module != "" {
		var filtered []ResourceNode
		for _, n := range sorted {
			if n.ModuleName == r.Module {
				filtered = append(filtered, n)
			}
		}
		sorted = filtered
	}

	var results []modules.ResourceResult

	// Main execution pass.
	for _, node := range sorted {
		result := r.executeResource(ctx, node)
		results = append(results, result)
	}

	// Notify pass (skip during dry-run).
	if !r.DryRun {
		notifyResults := r.executeNotifyPass(ctx, sorted, results, notifyMap, nodes)
		results = append(results, notifyResults...)
	}

	return results, nil
}

// executeResource runs a single resource: evaluates when condition, looks up
// executor, and calls Execute.
func (r *Runner) executeResource(ctx context.Context, node ResourceNode) modules.ResourceResult {
	result := modules.ResourceResult{
		ModuleName:   node.ModuleName,
		ResourceType: node.Type,
		ResourceName: node.Name,
	}

	// Evaluate when condition.
	if node.When != "" {
		shouldRun, err := EvaluateWhen(node.When, r.Facts)
		if err != nil {
			result.Error = fmt.Errorf("evaluate when for %q: %w", node.Name, err)
			return result
		}
		if !shouldRun {
			result.Action = modules.ActionSkipped
			return result
		}
	}

	// Look up executor by resource type.
	executor, ok := r.Executors[node.Type]
	if !ok {
		result.Error = fmt.Errorf("no executor registered for resource type %q", node.Type)
		return result
	}

	// Execute the resource.
	start := time.Now()
	execResult := executor.Execute(ctx, node.Params, r.DryRun)
	elapsed := time.Since(start)

	// Merge executor result with our metadata.
	execResult.ModuleName = node.ModuleName
	execResult.ResourceType = node.Type
	execResult.ResourceName = node.Name
	if execResult.DurationMs == 0 {
		execResult.DurationMs = int32(elapsed.Milliseconds())
	}

	return execResult
}

// executeNotifyPass collects resources that changed without error in the main
// pass, looks up their notify targets, deduplicates, and re-executes each
// target. Notify targets are force-executed even if they would be a no-op.
func (r *Runner) executeNotifyPass(
	ctx context.Context,
	sorted []ResourceNode,
	mainResults []modules.ResourceResult,
	notifyMap map[string][]string,
	allNodes []ResourceNode,
) []modules.ResourceResult {
	// Build set of changed resource names (no error).
	changedSet := map[string]bool{}
	for _, res := range mainResults {
		if res.Changed && res.Error == nil {
			changedSet[res.ResourceName] = true
		}
	}

	if len(changedSet) == 0 {
		return nil
	}

	// Collect unique notify targets in a deterministic order.
	seen := map[string]bool{}
	var notifyTargets []string
	for _, node := range sorted {
		if !changedSet[node.Name] {
			continue
		}
		targets := notifyMap[node.Name]
		for _, target := range targets {
			if !seen[target] {
				seen[target] = true
				notifyTargets = append(notifyTargets, target)
			}
		}
	}

	if len(notifyTargets) == 0 {
		return nil
	}

	// Build name -> node lookup from all nodes (not just sorted/filtered).
	nodeByName := map[string]ResourceNode{}
	for _, n := range allNodes {
		nodeByName[n.Name] = n
	}

	// Re-execute each notify target.
	var results []modules.ResourceResult
	for _, targetName := range notifyTargets {
		node, ok := nodeByName[targetName]
		if !ok {
			results = append(results, modules.ResourceResult{
				ResourceName: targetName,
				Error:        fmt.Errorf("notify target %q not found", targetName),
			})
			continue
		}
		result := r.executeResource(ctx, node)
		results = append(results, result)
	}

	return results
}
