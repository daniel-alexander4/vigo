# vigocli

Admin CLI for the Vigo distributed state enforcement engine. Communicates with the server's REST API to manage enrollment, nodes, runs, and operational queries.

- **Binary:** `vigocli`
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
vigocli envoys list

# View envoy traits
vigocli envoys traits web01.example.com
vigocli envoys traits web01.example.com os.distro

# Token-based enrollment (when trusted enrollment is not configured)
vigocli tokens generate
vigocli approved add "*.example.com" --token <token>
```

## Commands

### tokens -- Enrollment token management

```bash
# Generate a one-time enrollment token
vigocli tokens generate
```

### approved -- Approved pattern management

```bash
# Add an approved pattern with a token
vigocli approved add "*.example.com" --token <token>

# List all approved patterns
vigocli approved list

# Revoke a pattern
vigocli approved revoke <id>
```

### envoys -- Envoy management

```bash
# List all enrolled envoys
vigocli envoys list

# Show envoy details
vigocli envoys show <id>

# View collected traits
vigocli envoys traits <hostname>
vigocli envoys traits <hostname> os.distro    # dot-path query

# Revoke a envoy (blocks future check-ins)
vigocli envoys revoke <id>

# Permanently delete a envoy and all history
vigocli envoys delete <id>
```

### envoys push -- Force immediate check-in

```bash
# Force single envoy
vigocli envoys push <hostname-or-uuid>

# Force all envoys
vigocli envoys push --all
```

### runs -- Run history

```bash
# List recent runs (default limit: 20)
vigocli runs list
vigocli runs list --envoy <id> --limit 50

# Show run details
vigocli runs show <run-id>
```

### preview -- Dry-run

```bash
# Preview changes for a envoy
vigocli preview <hostname-or-uuid>
```

### rollback -- Rollback changes

```bash
# Rollback a specific file
vigocli rollback <hostname-or-uuid> --file /etc/nginx/nginx.conf

# Rollback to a specific run
vigocli rollback <hostname-or-uuid> --run <run-id>
```

### webhooks -- Webhook management

```bash
# Register a webhook
vigocli webhooks add https://hooks.example.com/vigo \
  --secret <hmac-secret> \
  --events "run.failure,drift.detected"

# List webhooks
vigocli webhooks list

# Delete a webhook
vigocli webhooks delete <id>
```

### packages -- Package audit

```bash
# List packages across all envoys
vigocli packages list

# Filter by envoy
vigocli packages list --envoy <id>

# Show package drift (installed != desired)
vigocli packages drift
```

### env -- Environment promotion

```bash
# Promote env vars from staging to production
vigocli env promote <module> --from staging --to production
```

### validate -- Config file validation

```bash
# Validate .vgo files
vigocli validate nginx.vgo openssh.vgo
```

### vi -- Config editing

```bash
# Edit with syntax highlighting + validation-on-save
vigocli vi nginx.vgo

# Short relative paths resolve to /srv/vigo/stack/stockpile/
vigocli vi modules/nginx.vgo
```
