// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: BUSL-1.1

package ai

// toolDefs returns the 14 tool definitions the LLM can call.
func toolDefs() []ToolDef {
	return []ToolDef{
		{
			Name:        "list_thralls",
			Description: "List enrolled thralls (managed machines). Optionally filter by status (active/revoked/stale) and limit results.",
			Parameters: jsonSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"status": map[string]any{"type": "string", "enum": []string{"active", "revoked", "stale"}, "description": "Filter by thrall status"},
					"limit":  map[string]any{"type": "integer", "description": "Max thralls to return (default 50)"},
				},
			}),
		},
		{
			Name:        "get_thrall",
			Description: "Get details for a single thrall by hostname or ID.",
			Parameters: jsonSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hostname": map[string]any{"type": "string", "description": "Thrall hostname"},
					"id":       map[string]any{"type": "string", "description": "Thrall UUID"},
				},
			}),
		},
		{
			Name:        "get_traits",
			Description: "Get the latest discovered traits (facts) for a thrall — OS, hardware, network, packages, etc.",
			Parameters: jsonSchema(map[string]any{
				"type":     "object",
				"properties": map[string]any{
					"hostname": map[string]any{"type": "string", "description": "Thrall hostname"},
				},
				"required": []string{"hostname"},
			}),
		},
		{
			Name:        "list_runs",
			Description: "List convergence runs. Optionally filter by thrall hostname and limit results.",
			Parameters: jsonSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hostname": map[string]any{"type": "string", "description": "Filter runs by thrall hostname"},
					"limit":    map[string]any{"type": "integer", "description": "Max runs to return (default 20)"},
				},
			}),
		},
		{
			Name:        "get_run",
			Description: "Get full details for a single run including per-resource results (action taken, errors, duration).",
			Parameters: jsonSchema(map[string]any{
				"type":     "object",
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string", "description": "Run UUID"},
				},
				"required": []string{"run_id"},
			}),
		},
		{
			Name:        "get_compliance",
			Description: "Get compliance status. If hostname is provided, returns status for that thrall. Otherwise returns fleet-wide summary.",
			Parameters: jsonSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hostname": map[string]any{"type": "string", "description": "Thrall hostname (omit for fleet summary)"},
				},
			}),
		},
		{
			Name:        "get_config",
			Description: "Get the effective resolved configuration for a thrall (secrets redacted). Shows modules, resources, vars.",
			Parameters: jsonSchema(map[string]any{
				"type":     "object",
				"properties": map[string]any{
					"hostname": map[string]any{"type": "string", "description": "Thrall hostname"},
				},
				"required": []string{"hostname"},
			}),
		},
		{
			Name:        "query_inventory",
			Description: "Query thralls by trait values. Use 'where' filters like 'os.distro=ubuntu' and 'show' to select which trait paths to display.",
			Parameters: jsonSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"where": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Filters as 'dot.path=value' (e.g. 'os.distro=ubuntu', 'kernel.version=5.15')",
					},
					"show": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Trait dot-paths to include in output (e.g. 'os.distro', 'hardware.memory_mb')",
					},
				},
			}),
		},
		{
			Name:        "list_package_drift",
			Description: "List packages that differ across thralls (installed on some but not others, or different versions).",
			Parameters: jsonSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
		},
		{
			Name:        "get_module_definition",
			Description: "Read the raw .hnb definition of a module (resources, vars, depends_on).",
			Parameters: jsonSchema(map[string]any{
				"type":     "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Module name"},
				},
				"required": []string{"name"},
			}),
		},
		{
			Name:        "list_modules",
			Description: "List all available module names.",
			Parameters: jsonSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
		},
		{
			Name:        "list_roles",
			Description: "List all available role names.",
			Parameters: jsonSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
		},
		{
			Name:        "dry_run",
			Description: "Compute a dry-run plan for a thrall — shows what would change without applying.",
			Parameters: jsonSchema(map[string]any{
				"type":     "object",
				"properties": map[string]any{
					"hostname": map[string]any{"type": "string", "description": "Thrall hostname"},
				},
				"required": []string{"hostname"},
			}),
		},
		{
			Name:        "get_compliance_history",
			Description: "Get historical compliance snapshots showing fleet compliance percentage over time.",
			Parameters: jsonSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer", "description": "Max snapshots to return (default 20)"},
				},
			}),
		},
	}
}

// jsonSchema wraps a map to pass through as JSON Schema.
func jsonSchema(m map[string]any) map[string]any {
	return m
}
