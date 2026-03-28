
# vigogo remediation

Manage automated drift remediation.

## Subcommands

### run

Trigger remediation for all non-compliant nodes (recurring drift or errors).

```bash
vigogo remediation run [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Preview targets without acting |

**Examples:**

```bash
# Preview what would be remediated
vigogo remediation run --dry-run

# Execute remediation
vigogo remediation run
```

### status

Show current fleet remediation posture — how many nodes need remediation.

```bash
vigogo remediation status
```

### list

List recent remediation runs.

```bash
vigogo remediation list
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
