# hobnobcli remediation

Manage automated drift remediation.

## Subcommands

### run

Trigger remediation for all non-compliant nodes (recurring drift or errors).

```bash
hobnobcli remediation run [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Preview targets without acting |

**Examples:**

```bash
# Preview what would be remediated
hobnobcli remediation run --dry-run

# Execute remediation
hobnobcli remediation run
```

### status

Show current fleet remediation posture — how many nodes need remediation.

```bash
hobnobcli remediation status
```

### list

List recent remediation runs.

```bash
hobnobcli remediation list
```
