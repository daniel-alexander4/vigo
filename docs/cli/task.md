# hobnobcli task

Dispatch and monitor ad-hoc commands or named tasks on thralls.

## Subcommands

| Subcommand | Description |
|------------|-------------|
| `run` | Run a command or named task on targeted thralls |
| `status` | Show status and results for a task run |
| `list` | List recent task runs |

## Usage

```bash
hobnobcli task [subcommand] [flags]
```

## run

Run an ad-hoc command or a named task on targeted thralls. The CLI polls until the task completes, then displays results.

```bash
hobnobcli task run <command-or-task-name> [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--target` | | | Target spec: hostname, glob (`*.web.*`), UUID, or trait filter (`os.distro=ubuntu`) |
| `--timeout` | | `60` | Command timeout in seconds |
| `--operator` | | current user | Operator name for audit |
| `--batch-size` | | | Batch size: count (`5`) or percentage (`25%`) |
| `--max-failures` | | | Max failures before abort: count (`2`) or percentage (`10%`) |
| `--health-check` | | | Shell command to run between batches (abort if exit != 0) |
| `--dry-run` | | `false` | Display batch plan without executing |
| `--param` | | | Task parameter (`key=value`), repeatable |

### Examples

#### Run an ad-hoc command

```bash
hobnobcli task run "uptime" --target "*.web.*"
```

```
Task t1a2b3c4 dispatched
--- web-01.example.com [complete] exit=0 42ms
 12:30:01 up 45 days,  3:22,  0 users,  load average: 0.15, 0.10, 0.05
--- web-02.example.com [complete] exit=0 38ms
 12:30:01 up 30 days,  1:15,  0 users,  load average: 0.22, 0.18, 0.12
```

#### Run a named task with parameters

```bash
hobnobcli task run restart-app --target "*.web.*" --param env=staging
```

#### Rolling execution with batch control

```bash
hobnobcli task run "systemctl restart nginx" --target "*.web.*" \
  --batch-size 25% --max-failures 2 \
  --health-check "curl -sf http://lb.example.com/health"
```

#### Dry-run to preview batch plan

```bash
hobnobcli task run "uptime" --target "*.web.*" --batch-size 2 --dry-run
```

```
Dry-run batch plan:

  Batch 0: web-01.example.com, web-02.example.com
  Batch 1: web-03.example.com, web-04.example.com

2 batch(es), 4 target(s), 0 offline
```

## status

Show status and results for an existing task run.

```bash
hobnobcli task status <id>
```

### Example

```bash
hobnobcli task status t1a2b3c4
```

```
Task:     t1a2b3c4
Command:  uptime
Target:   *.web.*
Operator: admin
Status:   complete
Targets:  2
Created:  2026-03-13 12:30
Started:  2026-03-13 12:30
Finished: 2026-03-13 12:30

--- web-01.example.com [complete] exit=0 42ms
 12:30:01 up 45 days,  3:22,  0 users,  load average: 0.15, 0.10, 0.05
```

## list

List recent task runs.

```bash
hobnobcli task list [flags]
```

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--limit` | | `20` | Maximum number of tasks to display |

### Example

```bash
hobnobcli task list --limit 5
```

```
ID          COMMAND            TARGET      STATUS     TARGETS  CREATED
t1a2b3c4    uptime             *.web.*     complete   4        2026-03-13 12:30
t5e6f7g8    systemctl resta... *.web.*     complete   4        2026-03-13 11:00
```

## Related

- [workflow](workflow.md) -- multi-step workflow orchestration
- [query](query.md) -- query trait values from thralls
- [runs](runs.md) -- view convergence run history
