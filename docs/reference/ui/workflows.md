
# Workflows

The Workflows page displays multi-step workflow execution history. Available at `/workflows`.

## Layout

### Filter Bar

Status filter buttons:

- **All** -- show all workflows
- **Running** -- currently executing
- **Complete** -- finished successfully
- **Failed** -- finished with failures

### Workflows Table

| Column | Description |
|--------|-------------|
| Workflow | Truncated workflow UUID, clickable link to detail (`/workflows/{id}`) |
| Name | Workflow definition name (from `tasks/*.yaml`) |
| Status | Color-coded badge: pending, running, complete, failed, aborted |
| Steps | Progress indicator showing `current/total` steps |
| Operator | Who initiated the workflow |
| Created | Workflow start timestamp |

The page header shows "{N} workflow runs".

## Key Actions

- **Filter by status**: Click a status button to narrow the list
- **View workflow detail**: Click a workflow ID to see per-step results including step name, linked push run, status, and timing
- **Abort a running workflow**: Available on the workflow detail page

## Data Sources

- Workflow list: Database `ListWorkflowRuns` query
- Workflow detail: Database `GetWorkflowRun` with `workflow_step_results` join
- Each step links to its associated push run (if applicable)
