#!/usr/bin/env bash
set -euo pipefail

# Hobnob deployment script — run on the Docker server after rsync.
# Usage: cd /path/to/hobnob && ./deploy.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

HOBNOB_DIR="/etc/hobnob"
SERVER_CONFIGS_DIR="${HOBNOB_DIR}/server-configs"
CLIENT_CONFIGS_DIR="${HOBNOB_DIR}/client-configs"
CERTS_DIR="${SERVER_CONFIGS_DIR}/certs"
MODULES_DIR="${CLIENT_CONFIGS_DIR}/modules"
ROLES_DIR="${CLIENT_CONFIGS_DIR}/roles"
TEMPLATES_DIR="${CLIENT_CONFIGS_DIR}/templates"
ENV_FILE="${SERVER_CONFIGS_DIR}/.env"

# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

info()  { printf '\033[1;34m[INFO]\033[0m  %s\n' "$*"; }
ok()    { printf '\033[1;32m[OK]\033[0m    %s\n' "$*"; }
warn()  { printf '\033[1;33m[WARN]\033[0m  %s\n' "$*"; }
fail()  { printf '\033[1;31m[FAIL]\033[0m  %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Step 1: Create /etc/hobnob directory structure
# ---------------------------------------------------------------------------

info "Creating ${HOBNOB_DIR} directory structure"

sudo mkdir -p "$CLIENT_CONFIGS_DIR" "$MODULES_DIR" "$ROLES_DIR" "$TEMPLATES_DIR" "$CERTS_DIR"

ok "Directory structure ready"

# ---------------------------------------------------------------------------
# Step 2: Generate TLS certs (if missing) and copy to server-configs/certs
# ---------------------------------------------------------------------------

info "Checking TLS certificates"

if [ -f "${CERTS_DIR}/server.crt" ] && [ -f "${CERTS_DIR}/server.key" ] && [ -f "${CERTS_DIR}/ca.crt" ]; then
  echo 'Certs already exist — skipping'
else
  if [ ! -f server-configs/certs/server.crt ] || [ ! -f server-configs/certs/server.key ] || [ ! -f server-configs/certs/ca.crt ]; then
    echo 'Generating self-signed CA + server certs...'
    make certs
  fi
  sudo cp server-configs/certs/server.crt server-configs/certs/server.key server-configs/certs/ca.crt "$CERTS_DIR/"
  echo "Copied certs to ${CERTS_DIR}"
fi

ok "TLS certificates ready"

# ---------------------------------------------------------------------------
# Step 3: Write .env file (if missing)
# ---------------------------------------------------------------------------

info "Checking .env file"

if [ -f "$ENV_FILE" ]; then
  echo "${ENV_FILE} already exists — skipping"
else
  sudo tee "$ENV_FILE" > /dev/null <<'ENVFILE'
# =============================================================================
# Hobnob Environment Configuration
# =============================================================================
# The StaticProvider reads these values to resolve secret: references in
# server-configs/server.yaml. Every secret:path/to/key maps to an env var:
#   secret:hobnob/db/dsn  ->  HOBNOB_DB_DSN
#   secret:hobnob/tls/cert -> HOBNOB_TLS_CERT
# Rule: uppercase, slashes become underscores.
#
# REQUIRED — server will not start without these:
# =============================================================================

HOBNOB_DB_DSN=postgres://hobnob:secret@postgres:5432/hobnob?sslmode=disable
HOBNOB_TLS_CERT=certs/server.crt
HOBNOB_TLS_KEY=certs/server.key
HOBNOB_TLS_CA=certs/ca.crt

# =============================================================================
# OPTIONAL — uncomment and edit as needed:
# =============================================================================

# Server config file path (default: server-configs/server.yaml)
# HOBNOB_CONFIG_FILE=server-configs/server.yaml

# Client configs directory (default: client-configs)
# HOBNOB_CLIENT_CONFIGS_DIR=client-configs

# Migrations directory (default: migrations)
# HOBNOB_MIGRATIONS_DIR=migrations

# Server version string reported to agents for self-update (default: dev)
# HOBNOB_VERSION=1.0.0

# Directory containing cross-compiled agent binaries for distribution
# Used by GET /api/v1/agent/download/{os}/{arch} (default: dist/agent)
# HOBNOB_AGENT_DIR=dist/agent

# Webhook secret for HMAC-SHA256 payload signing
# HOBNOB_WEBHOOK_SECRET=change-me-to-a-random-string

# .env file path override (default: server-configs/.env)
# HOBNOB_ENV_FILE=server-configs/.env
ENVFILE
  echo "${ENV_FILE} created with defaults"
fi

ok ".env ready"

# ---------------------------------------------------------------------------
# Step 4: Copy config files to /etc/hobnob/config
# ---------------------------------------------------------------------------

info "Populating ${CLIENT_CONFIGS_DIR}"

# Server config
sudo mkdir -p "$SERVER_CONFIGS_DIR"
if [ -f "${SERVER_CONFIGS_DIR}/server.yaml" ]; then
  echo 'server.yaml already exists — skipping'
else
  sudo cp server-configs/server.yaml "$SERVER_CONFIGS_DIR/"
  echo 'Copied server.yaml'
fi

# clients.hob
if [ -f "${CLIENT_CONFIGS_DIR}/clients.hob" ]; then
  echo 'clients.hob already exists — skipping'
else
  sudo cp client-configs/clients.hob "$CLIENT_CONFIGS_DIR/"
  echo 'Copied clients.hob'
fi

# Module definitions — copy any that don't already exist
NEW_MODULES=0
for src in client-configs/modules/*.hob; do
  [ -f "$src" ] || continue
  dest="${MODULES_DIR}/$(basename "$src")"
  if [ ! -f "$dest" ]; then
    sudo cp "$src" "$dest"
    NEW_MODULES=$((NEW_MODULES + 1))
  fi
done
MODULE_COUNT=$(find "$MODULES_DIR" -name '*.hob' 2>/dev/null | wc -l)
echo "Module definitions: ${MODULE_COUNT} total (${NEW_MODULES} new)"

# Role definitions — copy any that don't already exist
NEW_ROLES=0
for src in client-configs/roles/*.hob; do
  [ -f "$src" ] || continue
  dest="${ROLES_DIR}/$(basename "$src")"
  if [ ! -f "$dest" ]; then
    sudo cp "$src" "$dest"
    NEW_ROLES=$((NEW_ROLES + 1))
  fi
done
ROLE_COUNT=$(find "$ROLES_DIR" -name '*.hob' 2>/dev/null | wc -l)
echo "Role definitions: ${ROLE_COUNT} total (${NEW_ROLES} new)"

# Template files — recursively copy preserving directory structure
if [ -d "client-configs/templates" ]; then
  NEW_TEMPLATES=0
  while IFS= read -r src; do
    rel="${src#client-configs/templates/}"
    dest="${TEMPLATES_DIR}/${rel}"
    dest_dir="$(dirname "$dest")"
    if [ ! -f "$dest" ]; then
      sudo mkdir -p "$dest_dir"
      sudo cp "$src" "$dest"
      NEW_TEMPLATES=$((NEW_TEMPLATES + 1))
    fi
  done < <(find client-configs/templates -type f)
  TEMPLATE_COUNT=$(find "$TEMPLATES_DIR" -type f 2>/dev/null | wc -l)
  echo "Template files: ${TEMPLATE_COUNT} total (${NEW_TEMPLATES} new)"
fi

ok "Config files ready"

# ---------------------------------------------------------------------------
# Step 5: Build and start containers
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
# Step 6: Print status and next steps
# ---------------------------------------------------------------------------

cat <<DONE

============================================================
  Hobnob deployed successfully
============================================================

Config directory: ${HOBNOB_DIR}
  ${HOBNOB_DIR}/server-configs/server.yaml
  ${CLIENT_CONFIGS_DIR}/clients.hob
  ${MODULES_DIR}/*.hob
  ${ROLES_DIR}/*.hob
  ${TEMPLATES_DIR}/
  ${CERTS_DIR}/
  ${ENV_FILE}

Services:
  - PostgreSQL:  port 5432 (internal)
  - gRPC (mTLS): port 9090 (agent traffic)
  - REST + UI:   port 8085 (hobcli, dashboard, bootstrap)

Next steps:

  1. Open the dashboard:
     http://<server-ip>:8085/

  2. Generate an enrollment token:
     docker compose exec server hobcli tokens generate \\
       --pattern "*.example.com"

  3. On each managed node, run the printed curl command:
     curl http://<server-ip>:8085/bootstrap?token=<TOKEN> | bash

  4. Edit configs in ${CLIENT_CONFIGS_DIR}/
     (modules, roles, clients.hob — server hot-reloads automatically)
     Use 'hobvi' for syntax highlighting + idempotency validation on save.

  5. For production:
     - Replace ${CERTS_DIR}/ with real TLS certificates
     - Change POSTGRES_PASSWORD in docker-compose.yml + ${ENV_FILE}
     - Set HOBNOB_WEBHOOK_SECRET in ${ENV_FILE}
     - Set HOBNOB_VERSION to your release tag

============================================================
DONE
