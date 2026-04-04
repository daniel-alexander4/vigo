
# LLM Tools

The AI assistant has access to 14 read-only tools for querying fleet state. These tools are exposed to the LLM as function definitions, and the assistant calls them iteratively to gather data for answering questions.

All tool results pass through secret redaction before being included in the LLM context.

## Tool Reference

### list_envoys

List enrolled envoys (managed machines).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `status` | string | No | Filter by status: `active`, `revoked`, `stale` |
| `limit` | integer | No | Max envoys to return (default 50) |

### get_envoy

Get details for a single envoy by hostname or ID.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hostname` | string | No | Envoy hostname |
| `id` | string | No | Envoy UUID |

### get_traits

Get the latest discovered traits (facts) for a envoy -- OS, hardware, network, packages, etc.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hostname` | string | Yes | Envoy hostname |

### list_runs

List convergence runs.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hostname` | string | No | Filter runs by envoy hostname |
| `limit` | integer | No | Max runs to return (default 20) |

### get_run

Get full details for a single run including per-resource results (action taken, failed, duration).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `run_id` | string | Yes | Run UUID |

### get_compliance

Get compliance status. If hostname is provided, returns status for that envoy. Otherwise returns fleet-wide summary.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hostname` | string | No | Envoy hostname (omit for fleet summary) |


### get_config

Get the effective resolved configuration for a envoy (secrets redacted). Shows modules, resources, vars.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hostname` | string | Yes | Envoy hostname |

### query_inventory

Query envoys by trait values.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `where` | string[] | No | Filters as `dot.path=value` (e.g., `os.distro=ubuntu`) |
| `show` | string[] | No | Trait dot-paths to include in output |

### list_package_drift

List packages that differ across envoys (installed on some but not others, or different versions). Takes no parameters.

### get_module_definition

Read the raw `.vgo` definition of a module (resources, vars, depends_on).

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `name` | string | Yes | Module name |

### list_modules

List all available module names. Takes no parameters.

### list_roles

List all available role names. Takes no parameters.

### dry_run

Compute a dry-run plan for a envoy -- shows what would change without applying.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `hostname` | string | Yes | Envoy hostname |

### get_compliance_history

Get historical compliance snapshots showing fleet compliance percentage over time.

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `limit` | integer | No | Max snapshots to return (default 20) |

## Tool Dispatch

When the LLM calls a tool, the assistant:

1. Validates the tool name against the known 14 tools
2. Dispatches the call to the appropriate handler (queries the database, config store, or FleetIndex)
3. Strips any `secret:` values from the result
4. Returns the result as JSON to the LLM

The LLM can call multiple tools per round, and the loop continues for up to `max_tool_rounds` (default 10) iterations until the LLM produces a final answer without tool calls.
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
