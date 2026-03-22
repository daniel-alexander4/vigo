
# vigogo workflow

Run and monitor multi-step workflows that chain tasks with conditional branching.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `run` | Start a workflow execution |
| `status` | Show status and step details for a workflow run |
| `list` | List recent workflow runs |
| `abort` | Abort a running workflow |

## Usage

```bash
vigogo workflow [subcommand] [flags]
```

## run

Start a named workflow. Optionally wait for completion with polling.

```bash
vigogo workflow run <name> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--operator` | | current user | Operator name for audit |
| `--wait` | | `false` | Wait for workflow to complete |

### Examples

#### Start a workflow (fire and forget)

```bash
vigogo workflow run deploy-web
```

```
Workflow deploy-web started (id: w1a2b3c4)
```

#### Start a workflow and wait for completion

```bash
vigogo workflow run deploy-web --wait
```

```
Workflow deploy-web started (id: w1a2b3c4)
  Step 3/3 running...
Workflow: w1a2b3c4
Name:     deploy-web
Status:   complete
Steps:    3/3
Operator: admin
Created:  2026-03-13 12:30
Started:  2026-03-13 12:30
Finished: 2026-03-13 12:35

STEP  NAME              STATUS     TASK RUN
0     stop-services     complete   t1a2b3c4
1     deploy-code       complete   t5e6f7g8
2     start-services    complete   t9h0i1j2
```

## status

Show status and step details for a workflow run.

```bash
vigogo workflow status <id>
```

### Example

```bash
vigogo workflow status w1a2b3c4
```

```
Workflow: w1a2b3c4
Name:     deploy-web
Status:   running
Steps:    2/3
Operator: admin
Created:  2026-03-13 12:30
Started:  2026-03-13 12:30

STEP  NAME              STATUS     TASK RUN
0     stop-services     complete   t1a2b3c4
1     deploy-code       complete   t5e6f7g8
2     start-services    running    t9h0i1j2
```

## list

List recent workflow runs.

```bash
vigogo workflow list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--limit` | | `20` | Maximum number of workflows to display |

### Example

```bash
vigogo workflow list
```

```
ID          NAME          STATUS     STEPS  CREATED
w1a2b3c4    deploy-web    complete   3/3    2026-03-13 12:30
w5e6f7g8    rotate-certs  failed     2/4    2026-03-12 09:00
```

## abort

Abort a running workflow.

```bash
vigogo workflow abort <id>
```

### Example

```bash
vigogo workflow abort w1a2b3c4
```

```
Workflow w1a2b3c4: aborted
```

## Auto-Triggered Workflows

Workflows can fire automatically when security traits change. Add a `trigger:` field to the workflow YAML:

```yaml
name: rootkit-detected
trigger:
  on: trait_change
  trait_key: security_scan.chkrootkit.infected_count
  value: "1"
steps:
  - name: isolate
    command: "iptables -I OUTPUT -j DROP"
    target: "{{.TriggerHostname}}"
```

| Trigger Field | Description |
|---------------|-------------|
| `on` | Trigger type — currently only `trait_change` |
| `trait_key` | Flattened trait key to watch (e.g., `security_scan.trivy.critical`) |
| `value` | Optional — only fire when trait changes TO this value. Omit to fire on any change. |

Trigger evaluation runs during agent trait reporting, in a goroutine — no impact on check-in latency. The workflow starts with `operator: "trait-trigger:<envoy_id>:<hostname>"`.

See `examples/workflows/` for 10 pre-built security and operational workflow definitions.

## Related

- [task](task.md) -- dispatch individual ad-hoc tasks
- [runs](runs.md) -- view convergence run history
