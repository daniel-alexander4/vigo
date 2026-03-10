# hobnobcli

Admin CLI for the Hobnob configuration management system. Communicates with the server's REST API to manage enrollment, nodes, runs, and operational queries.

- **Binary:** `hobnobcli` (symlink `hnbvi` for config editing)
- **Transport:** REST API (default: `http://localhost:8181`)
- **All operations are read-only or administrative** -- never writes config files
- **Global flag:** `--server <url>` (default: `http://localhost:8181`)

## Quickstart

```bash
# Build
make build

# Point at your server
export HOBNOB_SERVER=http://hobd.example.com:8181
# Or use --server flag on every command

# List nodes
hobnobcli nodes list

# View node traits
hobnobcli nodes traits web01.example.com
hobnobcli nodes traits web01.example.com os.distro

# Token-based enrollment (when trusted enrollment is not configured)
hobnobcli tokens generate
hobnobcli approved add "*.example.com" --token <token>
```

## Commands

### tokens -- Enrollment token management

```bash
# Generate a one-time enrollment token
hobnobcli tokens generate
```

### approved -- Approved pattern management

```bash
# Add an approved pattern with a token
hobnobcli approved add "*.example.com" --token <token>

# List all approved patterns
hobnobcli approved list

# Revoke a pattern
hobnobcli approved revoke <id>
```

### nodes -- Node management

```bash
# List all enrolled nodes
hobnobcli nodes list

# Show node details
hobnobcli nodes show <id>

# View collected traits
hobnobcli nodes traits <hostname>
hobnobcli nodes traits <hostname> os.distro    # dot-path query

# Revoke a node (blocks future check-ins)
hobnobcli nodes revoke <id>

# Permanently delete a node and all history
hobnobcli nodes delete <id>
```

### push -- Force immediate check-in

```bash
# Force single node
hobnobcli push <hostname-or-uuid>

# Force all nodes
hobnobcli push --all
```

### runs -- Run history

```bash
# List recent runs (default limit: 20)
hobnobcli runs list
hobnobcli runs list --node <id> --limit 50

# Show run details
hobnobcli runs show <run-id>
```

### preview -- Dry-run

```bash
# Preview changes for a node
hobnobcli preview <hostname-or-uuid>
```

### rollback -- Rollback changes

```bash
# Rollback a specific file
hobnobcli rollback <hostname-or-uuid> --file /etc/nginx/nginx.conf

# Rollback to a specific run
hobnobcli rollback <hostname-or-uuid> --run <run-id>
```

### webhooks -- Webhook management

```bash
# Register a webhook
hobnobcli webhooks add https://hooks.example.com/hobnob \
  --secret <hmac-secret> \
  --events "run.failure,drift.detected"

# List webhooks
hobnobcli webhooks list

# Delete a webhook
hobnobcli webhooks delete <id>
```

### packages -- Package audit

```bash
# List packages across all nodes
hobnobcli packages list

# Filter by node
hobnobcli packages list --node <id>

# Show package drift (installed != desired)
hobnobcli packages drift
```

### env -- Environment promotion

```bash
# Promote env vars from staging to production
hobnobcli env promote <module> --from staging --to production
```

### validate -- Config file validation

```bash
# Validate .hnb files
hobnobcli validate nginx.hnb openssh.hnb

# Allow non-idempotent resources
hobnobcli validate --dangerous custom.hnb
```

### vi -- Config editing

```bash
# Edit with syntax highlighting + validation-on-save
hobnobcli vi nginx.hnb

# Or via symlink
hnbvi nginx.hnb
```
