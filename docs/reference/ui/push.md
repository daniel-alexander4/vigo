
# Pushes

The Pushes page shows ad-hoc push dispatch history and results. Available at `/push`.

## Layout

### Filter Bar

Status filter buttons:

- **All** -- show all pushes
- **Running** -- currently executing
- **Complete** -- finished successfully
- **Failed** -- finished with failures

### Pushes Table

| Column | Description |
|--------|-------------|
| Push | Truncated push UUID, clickable link to push detail (`/push/{id}`) |
| Command | Truncated command string (full command shown on hover) |
| Target | Hostname glob or trait filter used for targeting |
| Status | Color-coded badge: running, complete, failed, cancelled |
| Targets | Number of envoys targeted |
| Operator | Who initiated the push |
| Created | Push creation timestamp |

The page header shows "{N} push runs".

## Key Actions

- **Filter by status**: Click a status button to narrow the list
- **View push detail**: Click a push ID to see per-target results including exit code, stdout, stderr, and duration
- **Monitor running pushes**: Running pushes show live progress

## Data Sources

- Push list: Database `ListPushRuns` query
- Push detail: Database `GetPushRun` with `push_run_targets` join
- Per-target results include: hostname, status, exit code, stdout, stderr, duration, error message
