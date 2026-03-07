#!/usr/bin/env bash
set -euo pipefail

# Isogov deployment script — run on the Docker server after rsync.
# Usage: cd /path/to/isogov && ./deploy.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

ISOGOV_DIR="/etc/isogov"
CLIENT_CONFIGS_DIR="${ISOGOV_DIR}/client-configs"
CERTS_DIR="${ISOGOV_DIR}/server-configs/certs"
MODULES_DIR="${CLIENT_CONFIGS_DIR}/modules"
ROLES_DIR="${CLIENT_CONFIGS_DIR}/roles"
SERVER_CONFIGS_DIR="${ISOGOV_DIR}/server-configs"
ENV_FILE="${ISOGOV_DIR}/server-configs/.env"

# ---------------------------------------------------------------------------
# Helper functions
# ---------------------------------------------------------------------------

info()  { printf '\033[1;34m[INFO]\033[0m  %s\n' "$*"; }
ok()    { printf '\033[1;32m[OK]\033[0m    %s\n' "$*"; }
warn()  { printf '\033[1;33m[WARN]\033[0m  %s\n' "$*"; }
fail()  { printf '\033[1;31m[FAIL]\033[0m  %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Step 1: Create /etc/isogov directory structure
# ---------------------------------------------------------------------------

info "Creating ${ISOGOV_DIR} directory structure"

sudo mkdir -p "$CLIENT_CONFIGS_DIR" "$MODULES_DIR" "$ROLES_DIR" "$CERTS_DIR"

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
# Isogov Environment Configuration
# =============================================================================
# The StaticProvider reads these values to resolve secret: references in
# server-configs/server.yaml. Every secret:path/to/key maps to an env var:
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

# Server config file path (default: server-configs/server.yaml)
# ISOGOV_CONFIG_FILE=server-configs/server.yaml

# Client configs directory (default: client-configs)
# ISOGOV_CLIENT_CONFIGS_DIR=client-configs

# Migrations directory (default: migrations)
# ISOGOV_MIGRATIONS_DIR=migrations

# Server version string reported to agents for self-update (default: dev)
# ISOGOV_VERSION=1.0.0

# Directory containing cross-compiled agent binaries for distribution
# Used by GET /api/v1/agent/download/{os}/{arch} (default: dist/agent)
# ISOGOV_AGENT_DIR=dist/agent

# Webhook secret for HMAC-SHA256 payload signing
# ISOGOV_WEBHOOK_SECRET=change-me-to-a-random-string

# .env file path override (default: server-configs/.env)
# ISOGOV_ENV_FILE=server-configs/.env
ENVFILE
  echo "${ENV_FILE} created with defaults"
fi

ok ".env ready"

# ---------------------------------------------------------------------------
# Step 4: Copy config files to /etc/isogov/config
# ---------------------------------------------------------------------------

info "Populating ${CLIENT_CONFIGS_DIR}"

# Server config
SERVER_CONFIGS_DIR="${ISOGOV_DIR}/server-configs"
sudo mkdir -p "$SERVER_CONFIGS_DIR"
if [ -f "${SERVER_CONFIGS_DIR}/server.yaml" ]; then
  echo 'server.yaml already exists — skipping'
else
  sudo cp server-configs/server.yaml "$SERVER_CONFIGS_DIR/"
  echo 'Copied server.yaml'
fi

# clients.yaml
if [ -f "${CLIENT_CONFIGS_DIR}/clients.yaml" ]; then
  echo 'clients.yaml already exists — skipping'
else
  sudo cp client-configs/clients.yaml "$CLIENT_CONFIGS_DIR/"
  echo 'Copied clients.yaml'
fi

# Module definitions — copy any that don't already exist
NEW_MODULES=0
for src in client-configs/modules/*.yaml; do
  [ -f "$src" ] || continue
  dest="${MODULES_DIR}/$(basename "$src")"
  if [ ! -f "$dest" ]; then
    sudo cp "$src" "$dest"
    NEW_MODULES=$((NEW_MODULES + 1))
  fi
done
MODULE_COUNT=$(find "$MODULES_DIR" -name '*.yaml' 2>/dev/null | wc -l)
echo "Module definitions: ${MODULE_COUNT} total (${NEW_MODULES} new)"

# Role definitions — copy any that don't already exist
NEW_ROLES=0
for src in client-configs/roles/*.yaml; do
  [ -f "$src" ] || continue
  dest="${ROLES_DIR}/$(basename "$src")"
  if [ ! -f "$dest" ]; then
    sudo cp "$src" "$dest"
    NEW_ROLES=$((NEW_ROLES + 1))
  fi
done
ROLE_COUNT=$(find "$ROLES_DIR" -name '*.yaml' 2>/dev/null | wc -l)
echo "Role definitions: ${ROLE_COUNT} total (${NEW_ROLES} new)"

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
  Isogov deployed successfully
============================================================

Config directory: ${ISOGOV_DIR}
  ${ISOGOV_DIR}/server-configs/server.yaml
  ${CLIENT_CONFIGS_DIR}/clients.yaml
  ${MODULES_DIR}/*.yaml
  ${ROLES_DIR}/*.yaml
  ${CERTS_DIR}/
  ${ENV_FILE}

Services:
  - PostgreSQL:  port 5432 (internal)
  - gRPC (mTLS): port 9090 (agent traffic)
  - REST + UI:   port 8085 (gvctl, dashboard, bootstrap)

Next steps:

  1. Open the dashboard:
     http://<server-ip>:8080/

  2. Generate an enrollment token:
     docker compose exec server gvctl tokens generate \\
       --pattern "*.example.com"

  3. On each managed node, run the printed curl command:
     curl http://<server-ip>:8080/bootstrap?token=<TOKEN> | bash

  4. Edit configs in ${CLIENT_CONFIGS_DIR}/
     (modules, roles, clients.yaml — server hot-reloads automatically)

  5. For production:
     - Replace ${CERTS_DIR}/ with real TLS certificates
     - Change POSTGRES_PASSWORD in docker-compose.yml + ${ENV_FILE}
     - Set ISOGOV_WEBHOOK_SECRET in ${ENV_FILE}
     - Set ISOGOV_VERSION to your release tag

============================================================
DONE
