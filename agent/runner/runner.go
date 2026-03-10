package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hobnob/hobnob/agent/modules"
	pb "github.com/hobnob/hobnob/gen/proto/hobnob"
	"github.com/hobnob/hobnob/server/conditionals"
)

// whenParseCache caches parsed when expressions to avoid re-parsing the same
// expression string on every resource evaluation. Most resources in a module
// share identical when conditions, so this eliminates 80-90% of parse overhead.
//
// Bounded to maxWhenCacheEntries to prevent unbounded memory growth on
// long-running agents with frequently changing policies.
var (
	whenParseCache   sync.Map // map[string]whenCacheEntry
	whenCacheLen     atomic.Int64
)

const maxWhenCacheEntries = 1024

type whenCacheEntry struct {
	expr conditionals.WhenExpr
	err  error
}

// parseWhenExprCached returns a cached parse result for the given when string.
func parseWhenExprCached(when string) (conditionals.WhenExpr, error) {
	if v, ok := whenParseCache.Load(when); ok {
		entry := v.(whenCacheEntry)
		return entry.expr, entry.err
	}
	expr, err := conditionals.ParseWhenExpr(when)

	// Evict all entries when cache grows too large. The working set
	// rebuilds quickly since a single policy bundle has far fewer
	// unique expressions than the limit.
	if whenCacheLen.Add(1) > maxWhenCacheEntries {
		whenParseCache.Range(func(key, _ any) bool {
			whenParseCache.Delete(key)
			return true
		})
		whenCacheLen.Store(0)
	}

	whenParseCache.Store(when, whenCacheEntry{expr: expr, err: err})
	return expr, err
}

// Runner executes resources in DAG order with notify/subscribe triggers.
// Conditional evaluation happens server-side; the agent receives only
// resources that should run.
type Runner struct {
	// Executors maps resource type names to their executor implementations.
	Executors map[string]modules.Executor

	// Traits are the collected node traits (used for template rendering).
	Traits map[string]any

	// DryRun passes through to executors and disables the notify pass.
	DryRun bool

	// Module filters execution to only resources from the named module.
	// If empty, all resources are executed.
	Module string

	// Cache holds no-op results from previous cycles to skip redundant
	// executor calls. Nil disables caching.
	Cache *ResultCache
}

// NewRunner creates a Runner with the given configuration.
func NewRunner(executors map[string]modules.Executor, traits map[string]any, dryRun bool, module string) *Runner {
	return &Runner{
		Executors: executors,
		Traits:     traits,
		DryRun:    dryRun,
		Module:    module,
	}
}

// Run executes all resources from the provided proto modules in DAG topological
// order. It records results for each resource and performs a notify pass after
// the main execution pass for changed resources.
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

	// Only run topological sort when dependencies exist. Simple configs
	// (the majority) skip this overhead and execute in declaration order.
	sorted := nodes
	if needsTopoSort(nodes) {
		sorted, err = TopoSort(nodes)
		if err != nil {
			return nil, fmt.Errorf("topological sort: %w", err)
		}
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

// executeResource runs a single resource: looks up executor and calls Execute.
// If the resource has a When expression (containing builtins), evaluate it
// agent-side before running. Pure var expressions are resolved server-side.
func (r *Runner) executeResource(ctx context.Context, node ResourceNode) modules.ResourceResult {
	result := modules.ResourceResult{
		ModuleName:   node.ModuleName,
		ResourceType: node.Type,
		ResourceName: node.Name,
	}

	// Evaluate agent-side when expression if present.
	if node.When != "" {
		shouldRun, err := evaluateAgentWhen(ctx, node.When, r.Traits)
		if err != nil {
			result.Error = fmt.Errorf("evaluating when expression: %w", err)
			return result
		}
		if !shouldRun {
			result.Action = modules.ActionSkipped
			return result
		}
	}

	// Check result cache for no-op resources (skip redundant checks).
	if r.Cache != nil && !r.DryRun {
		if cached, ok := r.Cache.Lookup(node.Name, node.Type, node.Params); ok {
			cached.ModuleName = node.ModuleName
			cached.ResourceType = node.Type
			cached.ResourceName = node.Name
			return cached
		}
	}

	// Look up executor by resource type.
	executor, ok := r.Executors[node.Type]
	if !ok {
		result.Error = fmt.Errorf("no executor registered for resource type %q", node.Type)
		return result
	}

	// Inject resource name as fallback "name" param so executors that
	// don't have a type-specific attribute (e.g., repository, cron) can
	// use the resource's own name as an identifier.
	params := node.Params
	if _, hasName := params["name"]; !hasName {
		params = make(map[string]string, len(node.Params)+1)
		for k, v := range node.Params {
			params[k] = v
		}
		params["name"] = node.Name
	}

	// Execute the resource.
	start := time.Now()
	execResult := executor.Execute(ctx, params, r.DryRun)
	elapsed := time.Since(start)

	// Merge executor result with our metadata.
	execResult.ModuleName = node.ModuleName
	execResult.ResourceType = node.Type
	execResult.ResourceName = node.Name
	if execResult.DurationMs == 0 {
		execResult.DurationMs = int32(elapsed.Milliseconds())
	}

	// Store no-op results in cache for future cycles.
	if r.Cache != nil && !r.DryRun {
		r.Cache.Store(node.Name, node.Type, node.Params, execResult)
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

// evaluateAgentWhen evaluates a when expression on the agent side. Only
// builtin function calls should remain in agent-side expressions; var
// references have been substituted to true/false by the server.
func evaluateAgentWhen(ctx context.Context, when string, traits map[string]any) (bool, error) {
	expr, err := parseWhenExprCached(when)
	if err != nil {
		return false, fmt.Errorf("parsing when expression %q: %w", when, err)
	}

	return conditionals.EvalWhenExprWithFuncs(expr, func(name string) bool {
		// All vars should be substituted; unknown vars evaluate to false.
		return false
	}, func(name string, args []string) (bool, error) {
		switch name {
		case "file_exists":
			if len(args) != 1 {
				return false, fmt.Errorf("file_exists requires exactly 1 argument")
			}
			_, err := os.Stat(args[0])
			return err == nil, nil
		case "command_succeeds":
			if len(args) != 1 {
				return false, fmt.Errorf("command_succeeds requires exactly 1 argument")
			}
			parts := strings.Fields(args[0])
			cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
			return cmd.Run() == nil, nil

		// Time-based conditionals
		case "hour_range":
			if len(args) != 2 {
				return false, fmt.Errorf("hour_range requires 2 arguments (start, end)")
			}
			start, err1 := strconv.Atoi(args[0])
			end, err2 := strconv.Atoi(args[1])
			if err1 != nil || err2 != nil {
				return false, fmt.Errorf("hour_range arguments must be integers")
			}
			h := time.Now().Hour()
			if start <= end {
				return h >= start && h <= end, nil
			}
			return h >= start || h <= end, nil // wraps midnight
		case "day_of_week":
			if len(args) != 1 {
				return false, fmt.Errorf("day_of_week requires exactly 1 argument")
			}
			today := strings.ToLower(time.Now().Weekday().String())
			return strings.EqualFold(args[0], today) || strings.EqualFold(args[0], today[:3]), nil
		case "day_of_month":
			if len(args) != 1 {
				return false, fmt.Errorf("day_of_month requires exactly 1 argument")
			}
			d, err := strconv.Atoi(args[0])
			if err != nil {
				return false, fmt.Errorf("day_of_month argument must be integer")
			}
			return time.Now().Day() == d, nil

		// Persistent flags
		case "flag_set":
			if len(args) != 1 {
				return false, fmt.Errorf("flag_set requires exactly 1 argument")
			}
			flagPath := filepath.Join("/etc/hobnob-node/flags", args[0])
			_, err := os.Stat(flagPath)
			return err == nil, nil

		// Process checks
		case "process_running":
			if len(args) != 1 {
				return false, fmt.Errorf("process_running requires exactly 1 argument")
			}
			cmd := exec.CommandContext(ctx, "pgrep", "-x", args[0])
			return cmd.Run() == nil, nil

		// Directory check
		case "dir_exists":
			if len(args) != 1 {
				return false, fmt.Errorf("dir_exists requires exactly 1 argument")
			}
			info, err := os.Stat(args[0])
			return err == nil && info.IsDir(), nil

		// Traits-based builtins
		case "os_family":
			if len(args) != 1 {
				return false, fmt.Errorf("os_family requires exactly 1 argument")
			}
			return strings.EqualFold(traitStr(traits, "os", "family"), args[0]), nil
		case "distro":
			if len(args) != 1 {
				return false, fmt.Errorf("distro requires exactly 1 argument")
			}
			return strings.EqualFold(traitStr(traits, "os", "distro"), args[0]), nil
		case "arch":
			if len(args) != 1 {
				return false, fmt.Errorf("arch requires exactly 1 argument")
			}
			return strings.EqualFold(traitStr(traits, "os", "arch"), args[0]), nil
		case "version_ge":
			if len(args) != 1 {
				return false, fmt.Errorf("version_ge requires exactly 1 argument")
			}
			return compareVersionGE(traitStr(traits, "os", "version"), args[0]), nil
		case "in_group":
			if len(args) != 1 {
				return false, fmt.Errorf("in_group requires exactly 1 argument")
			}
			return userInGroup(traits, args[0]), nil

		default:
			return false, fmt.Errorf("unknown builtin function %q", name)
		}
	})
}

// traitStr retrieves a nested trait value as a string.
// e.g., traitStr(traits, "os", "family") → traits["os"]["family"]
func traitStr(traits map[string]any, keys ...string) string {
	var current any = traits
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[key]
	}
	s, _ := current.(string)
	return s
}

// compareVersionGE returns true if actual >= required using simple
// numeric segment comparison (e.g., "22.04" >= "20.04").
func compareVersionGE(actual, required string) bool {
	if actual == "" {
		return false
	}
	aParts := strings.Split(actual, ".")
	rParts := strings.Split(required, ".")

	maxLen := len(aParts)
	if len(rParts) > maxLen {
		maxLen = len(rParts)
	}

	for i := 0; i < maxLen; i++ {
		a, r := 0, 0
		if i < len(aParts) {
			a, _ = strconv.Atoi(aParts[i])
		}
		if i < len(rParts) {
			r, _ = strconv.Atoi(rParts[i])
		}
		if a > r {
			return true
		}
		if a < r {
			return false
		}
	}
	return true // equal
}

// userInGroup checks if the current user belongs to the specified group.
// Reads from identity traits (identity.groups) if available, otherwise
// falls back to checking /etc/group.
func userInGroup(traits map[string]any, group string) bool {
	// Check identity traits first.
	if identity, ok := traits["identity"].(map[string]any); ok {
		// Primary group.
		if g, ok := identity["group"].(string); ok && g == group {
			return true
		}
		// Supplementary groups list.
		if groups, ok := identity["groups"].([]string); ok {
			for _, g := range groups {
				if g == group {
					return true
				}
			}
		}
	}

	// Fallback: check /etc/group for current user.
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	user := traitStr(traits, "identity", "user")
	if user == "" {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 4 || parts[0] != group {
			continue
		}
		for _, member := range strings.Split(parts[3], ",") {
			if strings.TrimSpace(member) == user {
				return true
			}
		}
	}
	return false
}
