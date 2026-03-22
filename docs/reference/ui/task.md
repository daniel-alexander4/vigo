
# Tasks

The Tasks page shows ad-hoc task dispatch history and results. Available at `/task`.

## Layout

### Filter Bar

Status filter buttons:

- **All** -- show all tasks
- **Running** -- currently executing
- **Complete** -- finished successfully
- **Failed** -- finished with failures

### Tasks Table

| Column | Description |
|--------|-------------|
| Task | Truncated task UUID, clickable link to task detail (`/task/{id}`) |
| Command | Truncated command string (full command shown on hover) |
| Target | Hostname glob or trait filter used for targeting |
| Status | Color-coded badge: running, complete, failed, cancelled |
| Targets | Number of envoys targeted |
| Operator | Who initiated the task |
| Created | Task creation timestamp |

The page header shows "{N} task runs".

## Key Actions

- **Filter by status**: Click a status button to narrow the list
- **View task detail**: Click a task ID to see per-target results including exit code, stdout, stderr, and duration
- **Monitor running tasks**: Running tasks show live progress

## Data Sources

- Task list: Database `ListTaskRuns` query
- Task detail: Database `GetTaskRun` with `task_run_targets` join
- Per-target results include: hostname, status, exit code, stdout, stderr, duration, error message
