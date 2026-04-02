// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daniel-alexander4/vigo/server/convergence"
	"github.com/daniel-alexander4/vigo/server/config"
	"github.com/daniel-alexander4/vigo/server/db"
)

// toolDeps holds the dependencies needed to execute tool calls.
type toolDeps struct {
	queries      *db.Queries
	configStore  *config.ConfigStore
	runStore     db.RunStore
	baseInterval time.Duration
}

// dispatchTool executes a tool call and returns the result as a JSON string.
func (d *toolDeps) dispatchTool(ctx context.Context, name, argsJSON string) (string, error) {
	var args map[string]any
	if argsJSON != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("invalid tool arguments: %w", err)
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	switch name {
	case "list_envoys":
		return d.listEnvoys(ctx, args)
	case "get_envoy":
		return d.getEnvoy(ctx, args)
	case "get_traits":
		return d.getTraits(ctx, args)
	case "list_runs":
		return d.listRuns(ctx, args)
	case "get_run":
		return d.getRun(ctx, args)
	case "get_compliance":
		return d.getCompliance(ctx, args)
	case "get_config":
		return d.getConfig(ctx, args)
	case "query_inventory":
		return d.queryInventory(ctx, args)
	case "list_package_drift":
		return d.listPackageDrift(ctx, args)
	case "get_module_definition":
		return d.getModuleDefinition(args)
	case "list_modules":
		return d.listModules()
	case "list_roles":
		return d.listRoles()
	case "dry_run":
		return d.dryRun(ctx, args)
	case "get_convergence_history":
		return d.getConvergenceHistory(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

func (d *toolDeps) listEnvoys(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil {
		return marshalResult(map[string]any{"count": 0, "envoys": []any{}})
	}
	envoys, err := d.queries.ListEnvoys(ctx)
	if err != nil {
		return "", err
	}

	status, _ := args["status"].(string)
	limit := intArg(args, "limit", 50)

	type envoyInfo struct {
		ID         string  `json:"id"`
		Hostname   string  `json:"hostname"`
		IP         string  `json:"ip_address"`
		EnrolledAt string  `json:"enrolled_at"`
		LastSeen   string  `json:"last_seen,omitempty"`
		Status     string  `json:"status"`
	}

	var results []envoyInfo
	for _, t := range envoys {
		ts := "active"
		if t.Revoked != 0 {
			ts = "revoked"
		} else if t.LastSeen.Valid && time.Since(t.LastSeen.Time) > 2*d.baseInterval {
			ts = "stale"
		}

		if status != "" && ts != status {
			continue
		}

		info := envoyInfo{
			ID:         t.ID,
			Hostname:   t.Hostname,
			IP:         t.IpAddress,
			EnrolledAt: t.EnrolledAt.Format(time.RFC3339),
			Status:     ts,
		}
		if t.LastSeen.Valid {
			info.LastSeen = t.LastSeen.Time.Format(time.RFC3339)
		}
		results = append(results, info)
		if len(results) >= limit {
			break
		}
	}

	return marshalResult(map[string]any{
		"count":   len(results),
		"envoys": results,
	})
}

func (d *toolDeps) getEnvoy(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil {
		return "", fmt.Errorf("database not available")
	}
	hostname, _ := args["hostname"].(string)
	id, _ := args["id"].(string)

	var t db.Envoy
	var err error
	if hostname != "" {
		t, err = d.queries.GetEnvoyByHostname(ctx, hostname)
	} else if id != "" {
		t, err = d.queries.GetEnvoyByID(ctx, id)
	} else {
		return "", fmt.Errorf("either hostname or id is required")
	}
	if err != nil {
		return "", fmt.Errorf("envoy not found: %w", err)
	}

	result := map[string]any{
		"id":          t.ID,
		"hostname":    t.Hostname,
		"ip_address":  t.IpAddress,
		"enrolled_at": t.EnrolledAt.Format(time.RFC3339),
		"revoked":     t.Revoked != 0,
		"force_push":  t.ForcePush != 0,
	}
	if t.LastSeen.Valid {
		result["last_seen"] = t.LastSeen.Time.Format(time.RFC3339)
	}
	return marshalResult(result)
}

func (d *toolDeps) getTraits(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil {
		return "", fmt.Errorf("database not available")
	}
	hostname, _ := args["hostname"].(string)
	if hostname == "" {
		return "", fmt.Errorf("hostname is required")
	}

	t, err := d.queries.GetEnvoyByHostname(ctx, hostname)
	if err != nil {
		return "", fmt.Errorf("envoy not found: %w", err)
	}

	traits, err := d.queries.GetLatestEnvoyTraits(ctx, t.ID)
	if err != nil {
		return "", fmt.Errorf("no traits available: %w", err)
	}

	// Return raw traits JSON (already a JSON string from DB).
	return traits.Traits, nil
}

func (d *toolDeps) listRuns(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil || d.runStore == nil {
		return marshalResult(map[string]any{"count": 0, "runs": []any{}})
	}
	limit := int64(intArg(args, "limit", 20))
	hostname, _ := args["hostname"].(string)

	var runs []db.Run
	var err error
	if hostname != "" {
		t, tErr := d.queries.GetEnvoyByHostname(ctx, hostname)
		if tErr != nil {
			return "", fmt.Errorf("envoy not found: %w", tErr)
		}
		runs, err = d.runStore.ListRunsByEnvoy(ctx, db.ListRunsByEnvoyParams{
			EnvoyID: t.ID,
			Limit:    limit,
		})
	} else {
		runs, err = d.runStore.ListAllRuns(ctx, limit)
	}
	if err != nil {
		return "", err
	}

	type runInfo struct {
		ID        string `json:"id"`
		EnvoyID  string `json:"envoy_id"`
		Status    string `json:"status"`
		StartedAt string `json:"started_at"`
		Changed   int64  `json:"changed"`
		Failed    int64  `json:"failed"`
		Modules   int64  `json:"modules"`
	}

	results := make([]runInfo, 0, len(runs))
	for _, r := range runs {
		results = append(results, runInfo{
			ID:        r.ID,
			EnvoyID:  r.EnvoyID,
			Status:    r.Status,
			StartedAt: r.StartedAt.Format(time.RFC3339),
			Changed:   r.ChangedCount,
			Failed:    r.FailedCount,
			Modules:   r.ModuleCount,
		})
	}

	return marshalResult(map[string]any{
		"count": len(results),
		"runs":  results,
	})
}

func (d *toolDeps) getRun(ctx context.Context, args map[string]any) (string, error) {
	if d.runStore == nil {
		return "", fmt.Errorf("run store not available")
	}
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return "", fmt.Errorf("run_id is required")
	}

	run, err := d.runStore.GetRunByID(ctx, runID)
	if err != nil {
		return "", fmt.Errorf("run not found: %w", err)
	}

	results, err := d.runStore.ListRunResults(ctx, runID)
	if err != nil {
		return "", err
	}

	type resultInfo struct {
		Module    string `json:"module"`
		Type      string `json:"type"`
		Name      string `json:"name"`
		Action    string `json:"action"`
		Changed   bool   `json:"changed"`
		Error     string `json:"error,omitempty"`
		DurationMs int64 `json:"duration_ms"`
	}

	rr := make([]resultInfo, 0, len(results))
	for _, r := range results {
		ri := resultInfo{
			Module:     r.ModuleName,
			Type:       r.ResourceType,
			Name:       r.ResourceName,
			Action:     r.ActionTaken,
			Changed:    r.Changed != 0,
			DurationMs: r.DurationMs,
		}
		if r.ErrorMessage.Valid {
			ri.Error = r.ErrorMessage.String
		}
		rr = append(rr, ri)
	}

	return marshalResult(map[string]any{
		"id":         run.ID,
		"envoy_id":  run.EnvoyID,
		"status":     run.Status,
		"started_at": run.StartedAt.Format(time.RFC3339),
		"modules":    run.ModuleCount,
		"changed":    run.ChangedCount,
		"failed":     run.FailedCount,
		"skipped":    run.SkippedCount,
		"results":    rr,
	})
}

func (d *toolDeps) getCompliance(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil {
		return marshalResult(map[string]any{"total": 0, "summary": map[string]int{}})
	}
	hostname, _ := args["hostname"].(string)

	if hostname != "" {
		t, err := d.queries.GetEnvoyByHostname(ctx, hostname)
		if err != nil {
			return "", fmt.Errorf("envoy not found: %w", err)
		}

		latestRuns, err := d.runStore.LatestRunPerEnvoy(ctx, []string{t.ID})
		if err != nil {
			return "", err
		}

		var recent []convergence.RunInfo
		if run, ok := latestRuns[t.ID]; ok {
			recent = []convergence.RunInfo{{Status: run.Status, ChangedCount: run.ChangedCount}}
		}
		lastSeen := time.Time{}
		if t.LastSeen.Valid {
			lastSeen = t.LastSeen.Time
		}
		status := convergence.ComputeConvergence(recent, lastSeen, d.baseInterval)

		return marshalResult(map[string]any{
			"hostname": hostname,
			"status":   status,
		})
	}

	// Fleet-wide summary.
	envoys, err := d.queries.ListEnvoys(ctx)
	if err != nil {
		return "", err
	}

	var ids []string
	envoyMap := make(map[string]db.Envoy)
	for _, t := range envoys {
		if t.Revoked != 0 {
			continue
		}
		ids = append(ids, t.ID)
		envoyMap[t.ID] = t
	}

	if len(ids) == 0 {
		return marshalResult(map[string]any{"total": 0, "summary": map[string]int{}})
	}

	latestRuns, err := d.runStore.LatestRunPerEnvoy(ctx, ids)
	if err != nil {
		return "", err
	}

	var statuses []string
	for _, id := range ids {
		t := envoyMap[id]
		var recent []convergence.RunInfo
		if run, ok := latestRuns[id]; ok {
			recent = []convergence.RunInfo{{Status: run.Status, ChangedCount: run.ChangedCount}}
		}
		lastSeen := time.Time{}
		if t.LastSeen.Valid {
			lastSeen = t.LastSeen.Time
		}
		statuses = append(statuses, convergence.ComputeConvergence(recent, lastSeen, d.baseInterval))
	}

	return marshalResult(map[string]any{
		"total":   len(ids),
		"summary": convergence.Summary(statuses),
	})
}

func (d *toolDeps) getConfig(_ context.Context, args map[string]any) (string, error) {
	if d.configStore == nil {
		return "", fmt.Errorf("config store not available")
	}
	hostname, _ := args["hostname"].(string)
	if hostname == "" {
		return "", fmt.Errorf("hostname is required")
	}

	yamlStr, err := d.configStore.EffectiveConfigYAML(hostname)
	if err != nil {
		return "", err
	}
	return yamlStr, nil
}

func (d *toolDeps) queryInventory(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil {
		return marshalResult(map[string]any{"count": 0, "envoys": []any{}})
	}
	whereRaw, _ := args["where"].([]any)
	showRaw, _ := args["show"].([]any)

	var filters []filter
	for _, w := range whereRaw {
		s, ok := w.(string)
		if !ok {
			continue
		}
		parts := strings.SplitN(s, "=", 2)
		if len(parts) != 2 {
			continue
		}
		filters = append(filters, filter{path: parts[0], value: parts[1]})
	}

	var showFields []string
	for _, s := range showRaw {
		if str, ok := s.(string); ok {
			showFields = append(showFields, str)
		}
	}

	all, err := d.queries.GetAllLatestTraits(ctx)
	if err != nil {
		return "", err
	}

	type entry struct {
		Hostname string         `json:"hostname"`
		IP       string         `json:"ip_address"`
		Traits   map[string]any `json:"traits,omitempty"`
	}

	var results []entry
	for _, t := range all {
		var traits map[string]any
		if err := json.Unmarshal([]byte(t.Traits), &traits); err != nil {
			continue
		}
		if !matchFilters(traits, filters) {
			continue
		}

		e := entry{Hostname: t.Hostname, IP: t.IPAddress}
		if len(showFields) > 0 {
			e.Traits = make(map[string]any)
			for _, f := range showFields {
				if v := getNestedValue(traits, f); v != nil {
					e.Traits[f] = v
				}
			}
		} else {
			e.Traits = traits
		}
		results = append(results, e)
	}

	return marshalResult(map[string]any{
		"count":   len(results),
		"envoys": results,
	})
}

func (d *toolDeps) listPackageDrift(ctx context.Context, _ map[string]any) (string, error) {
	if d.queries == nil {
		return marshalResult(map[string]any{"count": 0, "package_drift": []any{}})
	}
	drift, err := d.queries.ListPackageDrift(ctx)
	if err != nil {
		return "", err
	}

	type driftInfo struct {
		Package          string `json:"package"`
		Hostname         string `json:"hostname"`
		InstalledVersion string `json:"installed_version,omitempty"`
		DesiredVersion   string `json:"desired_version,omitempty"`
	}

	results := make([]driftInfo, 0, len(drift))
	for _, row := range drift {
		info := driftInfo{
			Package:  row.PackageName,
			Hostname: row.Hostname,
		}
		if row.InstalledVersion.Valid {
			info.InstalledVersion = row.InstalledVersion.String
		}
		if row.DesiredVersion.Valid {
			info.DesiredVersion = row.DesiredVersion.String
		}
		results = append(results, info)
	}

	return marshalResult(map[string]any{
		"count":         len(results),
		"package_drift": results,
	})
}

func (d *toolDeps) getModuleDefinition(args map[string]any) (string, error) {
	if d.configStore == nil {
		return "", fmt.Errorf("config store not available")
	}
	name, _ := args["name"].(string)
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	modulesDir := filepath.Join(d.configStore.Dir(), "modules")
	var found string
	for _, ext := range []string{".vgo", ".yaml", ".yml"} {
		candidate := filepath.Join(modulesDir, name+ext)
		if _, err := os.Stat(candidate); err == nil {
			found = candidate
			break
		}
	}

	// Try subdirectories if not at top level.
	if found == "" {
		filepath.WalkDir(modulesDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if base == name {
				found = path
				return filepath.SkipAll
			}
			return nil
		})
	}

	if found == "" {
		return "", fmt.Errorf("module %q not found", name)
	}

	data, err := os.ReadFile(found)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (d *toolDeps) listModules() (string, error) {
	if d.configStore == nil {
		return marshalResult(map[string]any{"count": 0, "modules": []string{}})
	}
	names := d.configStore.ListModuleNames()
	return marshalResult(map[string]any{
		"count":   len(names),
		"modules": names,
	})
}

func (d *toolDeps) listRoles() (string, error) {
	if d.configStore == nil {
		return marshalResult(map[string]any{"count": 0, "roles": []string{}})
	}
	names := d.configStore.ListRoleNames()
	return marshalResult(map[string]any{
		"count": len(names),
		"roles": names,
	})
}

func (d *toolDeps) dryRun(_ context.Context, args map[string]any) (string, error) {
	if d.configStore == nil {
		return "", fmt.Errorf("config store not available")
	}
	hostname, _ := args["hostname"].(string)
	if hostname == "" {
		return "", fmt.Errorf("hostname is required")
	}

	cfg := d.configStore.Get(hostname)
	if cfg == nil || len(cfg.Modules) == 0 {
		return marshalResult(map[string]any{
			"hostname": hostname,
			"modules":  0,
			"message":  "no config found for this envoy",
		})
	}

	type moduleInfo struct {
		Name      string   `json:"name"`
		Resources int      `json:"resources"`
		DependsOn []string `json:"depends_on,omitempty"`
	}

	modules := make([]moduleInfo, 0, len(cfg.Modules))
	for _, m := range cfg.Modules {
		modules = append(modules, moduleInfo{
			Name:      m.Name,
			Resources: len(m.Resources),
			DependsOn: m.DependsOn,
		})
	}

	return marshalResult(map[string]any{
		"hostname":    hostname,
		"environment": cfg.Environment,
		"modules":     modules,
		"total_resources": countResources(cfg),
	})
}

func (d *toolDeps) getConvergenceHistory(ctx context.Context, args map[string]any) (string, error) {
	if d.queries == nil {
		return marshalResult(map[string]any{"count": 0, "snapshots": []any{}})
	}
	limit := int64(intArg(args, "limit", 20))

	history, err := d.queries.ListConvergenceHistory(ctx, limit)
	if err != nil {
		return "", err
	}

	type snapshot struct {
		RecordedAt  string `json:"recorded_at"`
		Converged   int64  `json:"converged"`
		Relapsed    int64  `json:"relapsed"`
		Diverged    int64  `json:"diverged"`
		Failed      int64  `json:"failed"`
		LostContact int64  `json:"lost_contact"`
		NoData      int64  `json:"no_data"`
		Total       int64  `json:"total"`
	}

	results := make([]snapshot, 0, len(history))
	for _, h := range history {
		results = append(results, snapshot{
			RecordedAt:  h.RecordedAt,
			Converged:   h.Converged,
			Relapsed:    h.Relapsed,
			Diverged:    h.Diverged,
			Failed:      h.Failed,
			LostContact: h.LostContact,
			NoData:      h.NoData,
			Total:       h.Total,
		})
	}

	return marshalResult(map[string]any{
		"count":     len(results),
		"snapshots": results,
	})
}

// --- helpers ---

type filter struct {
	path  string
	value string
}

func matchFilters(traits map[string]any, filters []filter) bool {
	for _, f := range filters {
		v := getNestedValue(traits, f.path)
		if v == nil || fmt.Sprintf("%v", v) != f.value {
			return false
		}
	}
	return true
}

func getNestedValue(m map[string]any, dotPath string) any {
	parts := strings.Split(dotPath, ".")
	var current any = m
	for _, p := range parts {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = cm[p]
	}
	return current
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return def
	}
}

func countResources(cfg *config.EnvoyConfig) int {
	n := 0
	for _, m := range cfg.Modules {
		n += len(m.Resources)
	}
	return n
}

func marshalResult(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
