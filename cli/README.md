# vigogo

Admin CLI for the Vigo distributed state enforcement engine. Communicates with the server's REST API to manage enrollment, nodes, runs, and operational queries.

- **Binary:** `vigogo` (symlink `hnbvi` for config editing)
- **Transport:** REST API (default: `https://localhost:8443`)
- **All operations are read-only or administrative** -- never writes config files
- **Global flag:** `--server <url>` (default: `https://localhost:8443`)

## Quickstart

```bash
# Build
make build

# Point at your server
export VIGO_SERVER=https://vigosrv.example.com:8443
# Or use --server flag on every command

# List envoys
vigogo envoys list

# View envoy traits
vigogo envoys traits web01.example.com
vigogo envoys traits web01.example.com os.distro

# Token-based enrollment (when trusted enrollment is not configured)
vigogo tokens generate
vigogo approved add "*.example.com" --token <token>
```

## Commands

### tokens -- Enrollment token management

```bash
# Generate a one-time enrollment token
vigogo tokens generate
```

### approved -- Approved pattern management

```bash
# Add an approved pattern with a token
vigogo approved add "*.example.com" --token <token>

# List all approved patterns
vigogo approved list

# Revoke a pattern
vigogo approved revoke <id>
```

### envoys -- Envoy management

```bash
# List all enrolled envoys
vigogo envoys list

# Show envoy details
vigogo envoys show <id>

# View collected traits
vigogo envoys traits <hostname>
vigogo envoys traits <hostname> os.distro    # dot-path query

# Revoke a envoy (blocks future check-ins)
vigogo envoys revoke <id>

# Permanently delete a envoy and all history
vigogo envoys delete <id>
```

### push -- Force immediate check-in

```bash
# Force single envoy
vigogo push <hostname-or-uuid>

# Force all envoys
vigogo push --all
```

### runs -- Run history

```bash
# List recent runs (default limit: 20)
vigogo runs list
vigogo runs list --envoy <id> --limit 50

# Show run details
vigogo runs show <run-id>
```

### preview -- Dry-run

```bash
# Preview changes for a envoy
vigogo preview <hostname-or-uuid>
```

### rollback -- Rollback changes

```bash
# Rollback a specific file
vigogo rollback <hostname-or-uuid> --file /etc/nginx/nginx.conf

# Rollback to a specific run
vigogo rollback <hostname-or-uuid> --run <run-id>
```

### webhooks -- Webhook management

```bash
# Register a webhook
vigogo webhooks add https://hooks.example.com/vigo \
  --secret <hmac-secret> \
  --events "run.failure,drift.detected"

# List webhooks
vigogo webhooks list

# Delete a webhook
vigogo webhooks delete <id>
```

### packages -- Package audit

```bash
# List packages across all envoys
vigogo packages list

# Filter by envoy
vigogo packages list --envoy <id>

# Show package drift (installed != desired)
vigogo packages drift
```

### env -- Environment promotion

```bash
# Promote env vars from staging to production
vigogo env promote <module> --from staging --to production
```

### validate -- Config file validation

```bash
# Validate .vgo files
vigogo validate nginx.vgo openssh.vgo
```

### vi -- Config editing

```bash
# Edit with syntax highlighting + validation-on-save
vigogo vi nginx.vgo

# Or via symlink
hnbvi nginx.vgo
```
