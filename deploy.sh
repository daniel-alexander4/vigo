#!/usr/bin/env bash
set -euo pipefail

# Isogov deployment script — run on the Docker server after rsync.
# Usage: cd /path/to/isogov && ./deploy.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

ISOGOV_DIR="/etc/isogov"
CONFIG_DIR="${ISOGOV_DIR}/config"
CERTS_DIR="${ISOGOV_DIR}/certs"
CLIENTS_DIR="${CONFIG_DIR}/clients"
ENV_FILE="${ISOGOV_DIR}/.env"

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

sudo mkdir -p "$CONFIG_DIR" "$CLIENTS_DIR" "$CERTS_DIR"

ok "Directory structure ready"

# ---------------------------------------------------------------------------
# Step 2: Generate TLS certs (if missing) and copy to /etc/isogov/certs
# ---------------------------------------------------------------------------

info "Checking TLS certificates"

if [ -f "${CERTS_DIR}/server.crt" ] && [ -f "${CERTS_DIR}/server.key" ] && [ -f "${CERTS_DIR}/ca.crt" ]; then
  echo 'Certs already exist in /etc/isogov/certs — skipping'
else
  if [ ! -f certs/server.crt ] || [ ! -f certs/server.key ] || [ ! -f certs/ca.crt ]; then
    echo 'Generating self-signed CA + server certs...'
    make certs
  fi
  sudo cp certs/server.crt certs/server.key certs/ca.crt "$CERTS_DIR/"
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
  echo "${ENV_FILE} created with defaults"
fi

ok ".env ready"

# ---------------------------------------------------------------------------
# Step 4: Copy config files to /etc/isogov/config
# ---------------------------------------------------------------------------

info "Populating ${CONFIG_DIR}"

# Server config
if [ -f "${CONFIG_DIR}/server.yaml" ]; then
  echo 'server.yaml already exists — skipping'
else
  sudo cp config/server.yaml "$CONFIG_DIR/"
  echo 'Copied server.yaml'
fi

# Classification rules
if [ -f "${CONFIG_DIR}/classification.yaml" ]; then
  echo 'classification.yaml already exists — skipping'
else
  sudo cp config/classification.yaml "$CONFIG_DIR/"
  echo 'Copied classification.yaml'
fi

# Client configs — copy any that don't already exist in destination
NEW_CLIENTS=0
for src in config/clients/*.yaml; do
  [ -f "$src" ] || continue
  dest="${CLIENTS_DIR}/$(basename "$src")"
  if [ ! -f "$dest" ]; then
    sudo cp "$src" "$dest"
    NEW_CLIENTS=$((NEW_CLIENTS + 1))
  fi
done
CLIENT_COUNT=$(find "$CLIENTS_DIR" -name '*.yaml' 2>/dev/null | wc -l)
echo "Client configs: ${CLIENT_COUNT} total (${NEW_CLIENTS} new)"

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
  ${CONFIG_DIR}/server.yaml
  ${CONFIG_DIR}/classification.yaml
  ${CLIENTS_DIR}/*.yaml
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

  4. Edit client configs in ${CLIENTS_DIR}/
     (server hot-reloads automatically)

  5. For production:
     - Replace ${CERTS_DIR}/ with real TLS certificates
     - Change POSTGRES_PASSWORD in docker-compose.yml + ${ENV_FILE}
     - Set ISOGOV_WEBHOOK_SECRET in ${ENV_FILE}
     - Set ISOGOV_VERSION to your release tag

============================================================
DONE
