#!/usr/bin/env bash
set -euo pipefail

# Isogov deployment script — run on the Docker server after rsync.
# Usage: cd /path/to/isogov && ./deploy.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

info()  { printf '\033[1;34m[INFO]\033[0m  %s\n' "$*"; }
ok()    { printf '\033[1;32m[OK]\033[0m    %s\n' "$*"; }
warn()  { printf '\033[1;33m[WARN]\033[0m  %s\n' "$*"; }
fail()  { printf '\033[1;31m[FAIL]\033[0m  %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Step 2: Generate TLS certs (if missing)
# ---------------------------------------------------------------------------

info "Checking TLS certificates"

if [ -f certs/server.crt ] && [ -f certs/server.key ] && [ -f certs/ca.crt ]; then
  echo 'Certs already exist — skipping generation'
else
  echo 'Generating self-signed CA + server certs...'
  make certs
fi

ok "TLS certificates ready"

# ---------------------------------------------------------------------------
# Step 3: Write .env file (if missing)
# ---------------------------------------------------------------------------

info "Checking .env file"

if [ -f .env ]; then
  echo '.env already exists — skipping'
else
  cat > .env <<'ENVFILE'
# =============================================================================
# Isogov Environment Configuration
# =============================================================================
# The StaticProvider reads these values to resolve secret: references in
# config/server.yaml. Every secret:path/to/key maps to an env var:
#   secret:isogov/db/dsn  ->  ISOGOV_DB_DSN
#   secret:isogov/tls/cert -> ISOGOV_TLS_CERT
# Rule: uppercase, slashes become underscores.
#
# REQUIRED — server will not start without these:
# =============================================================================

ISOGOV_DB_DSN=postgres://isogov:secret@postgres:5432/isogov?sslmode=disable
ISOGOV_TLS_CERT=certs/server.crt
ISOGOV_TLS_KEY=certs/server.key
ISOGOV_TLS_CA=certs/ca.crt

# =============================================================================
# OPTIONAL — uncomment and edit as needed:
# =============================================================================

# Server config file path (default: config/server.yaml)
# ISOGOV_CONFIG_FILE=config/server.yaml

# Client configs directory (default: config/clients)
# ISOGOV_CLIENTS_DIR=config/clients

# Classification rules file (default: config/classification.yaml)
# ISOGOV_CLASSIFICATION_FILE=config/classification.yaml

# Migrations directory (default: migrations)
# ISOGOV_MIGRATIONS_DIR=migrations

# Server version string reported to agents for self-update (default: dev)
# ISOGOV_VERSION=1.0.0

# Directory containing cross-compiled agent binaries for distribution
# Used by GET /api/v1/agent/download/{os}/{arch} (default: dist/agent)
# ISOGOV_AGENT_DIR=dist/agent

# Webhook secret for HMAC-SHA256 payload signing
# ISOGOV_WEBHOOK_SECRET=change-me-to-a-random-string

# .env file path override (default: .env — this file)
# ISOGOV_ENV_FILE=.env
ENVFILE
  echo '.env created with defaults'
fi

ok ".env ready"

# ---------------------------------------------------------------------------
# Step 4: Write example client configs (if clients dir is empty)
# ---------------------------------------------------------------------------

info "Checking client configs"

CLIENT_COUNT=$(find config/clients -name '*.yaml' 2>/dev/null | wc -l)
if [ "$CLIENT_COUNT" -gt 0 ]; then
  echo "Found ${CLIENT_COUNT} client config(s) — skipping examples"
else
  mkdir -p config/clients

  # Example 1: Explicit client config for a web server
  cat > config/clients/web01.example.com.yaml <<'CLIENTFILE'
# Explicit client config — matched by exact hostname.
# This takes precedence over classification rules.
client: web01.example.com
environment: production

vars:
  app_port: \"8085\"
  log_level: \"info\"
  doc_root: \"/var/www/html\"

env_vars:
  production:
    app_port: \"443\"
    max_workers: \"8\"
  staging:
    app_port: \"8443\"
    max_workers: \"2\"

modules:
  - name: webserver
    resources:
      - name: install-nginx
        type: package
        package: nginx
        state: present

      - name: nginx-config
        type: file
        path: /etc/nginx/sites-available/default
        content: |
          server {
              listen {{ .Vars.app_port }};
              root {{ .Vars.doc_root }};
              access_log /var/log/nginx/access.log;
          }
        owner: root
        group: root
        mode: \"0644\"
        depends_on: [install-nginx]
        notify: [restart-nginx]

      - name: restart-nginx
        type: service
        service: nginx
        enabled: true
        state: running
        subscribes: [nginx-config]
CLIENTFILE

  # Example 2: Database server
  cat > config/clients/db01.example.com.yaml <<'CLIENTFILE'
# Database server — manages PostgreSQL installation and config.
client: db01.example.com
environment: production

vars:
  pg_version: \"16\"
  max_connections: \"200\"
  shared_buffers: \"256MB\"

modules:
  - name: postgres
    resources:
      - name: install-postgres
        type: package
        package: postgresql
        state: present

      - name: pg-config
        type: file
        path: /etc/postgresql/16/main/postgresql.conf
        content: |
          max_connections = {{ .Vars.max_connections }}
          shared_buffers = {{ .Vars.shared_buffers }}
          listen_addresses = '*'
        owner: postgres
        group: postgres
        mode: \"0644\"
        depends_on: [install-postgres]
        notify: [restart-postgres]

      - name: restart-postgres
        type: service
        service: postgresql
        enabled: true
        state: running
        subscribes: [pg-config]
CLIENTFILE

  echo 'Created 2 example client configs'
fi

ok "Client configs ready"

# ---------------------------------------------------------------------------
# Step 5: Write classification rules (if default/empty)
# ---------------------------------------------------------------------------

info "Checking classification rules"

# Only overwrite if it still has the example modules/ references that don't exist
if grep -q 'source: modules/' config/classification.yaml 2>/dev/null; then
  cat > config/classification.yaml <<'CLASSFILE'
# Classification rules — auto-assign modules to clients by hostname pattern.
# First matching rule wins. Explicit client YAML (config/clients/*.yaml)
# always takes precedence over classification.
#
# Clients that match no rule and have no explicit config get an empty
# module list and run cleanly with no changes.

rules:
  # Web servers: any hostname starting with 'web-'
  - match: \"web-*\"
    environment: production
    modules:
      - name: webserver
        resources:
          - name: install-nginx
            type: package
            package: nginx
            state: present
          - name: ensure-running
            type: service
            service: nginx
            enabled: true
            state: running
            depends_on: [install-nginx]

  # Database servers: any hostname starting with 'db-'
  - match: \"db-*\"
    environment: production
    modules:
      - name: database
        resources:
          - name: install-postgres
            type: package
            package: postgresql
            state: present
          - name: ensure-running
            type: service
            service: postgresql
            enabled: true
            state: running
            depends_on: [install-postgres]

  # Staging: anything under *.staging.example.com
  - match: \"*.staging.example.com\"
    environment: staging
    modules: []
CLASSFILE
  echo 'Updated classification.yaml with working examples'
else
  echo 'classification.yaml already customized — skipping'
fi

ok "Classification rules ready"

# ---------------------------------------------------------------------------
# Step 6: Build and start containers
# ---------------------------------------------------------------------------

info "Building and starting containers"

docker compose build
docker compose up -d
echo '---'
echo 'Waiting for services to be healthy...'
sleep 5
docker compose ps

ok "Containers running"

# ---------------------------------------------------------------------------
# Step 7: Print status and next steps
# ---------------------------------------------------------------------------

cat <<'DONE'

============================================================
  Isogov deployed successfully
============================================================

Services:
  - PostgreSQL:  port 5432 (internal)
  - gRPC (mTLS): port 9090 (agent traffic)
  - REST + UI:   port 8085 (gvctl, dashboard, bootstrap)

Next steps:

  1. Open the dashboard:
     http://<server-ip>:8080/

  2. Generate an enrollment token:
     docker compose exec server gvctl tokens generate \
       --pattern "*.example.com"

  3. On each managed node, run the printed curl command:
     curl http://<server-ip>:8080/bootstrap?token=<TOKEN> | bash

  4. Edit client configs in config/clients/*.yaml
     (server hot-reloads automatically)

  5. For production:
     - Replace certs/ with real TLS certificates
     - Change POSTGRES_PASSWORD in docker-compose.yml + .env
     - Set ISOGOV_WEBHOOK_SECRET in .env
     - Set ISOGOV_VERSION to your release tag

============================================================
DONE
