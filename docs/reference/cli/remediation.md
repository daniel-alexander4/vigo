
# vigocli remediation

Manage automated drift remediation.

## Subcommands

### run

Trigger remediation for all non-compliant nodes (relapsed, diverged, or errors).

```bash
vigocli remediation run [flags]
```

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Preview targets without acting |

**Examples:**

```bash
# Preview what would be remediated
vigocli remediation run --dry-run

# Execute remediation
vigocli remediation run
```

### status

Show current fleet remediation posture — how many nodes need remediation.

```bash
vigocli remediation status
```

### list

List recent remediation runs.

```bash
vigocli remediation list
```
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
