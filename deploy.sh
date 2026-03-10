#!/usr/bin/env bash
set -euo pipefail

# Hobnob deployment — build and start.
# All config seeding happens inside the container entrypoint.

cd "$(dirname "$0")"

echo "Building and starting containers..."
docker compose build
docker compose up -d

echo "Waiting for services..."
sleep 5
docker compose ps

cat <<'DONE'

============================================================
  Hobnob deployed
============================================================

All config lives inside the hobnob-data Docker volume at /data.
On first run, the entrypoint seeds default configs and generates
TLS certificates automatically.

To edit configs:
  docker compose exec hobnob sh
  vi /data/node-configs/modules/nginx.hob
  # Server hot-reloads .hob files automatically.

Services:
  - gRPC (mTLS): port 1530 (agent traffic)
  - REST + UI:   port 8181 (hobcli, dashboard, bootstrap)

Next steps:
  1. Open the dashboard: http://<server-ip>:8181/
  2. Bootstrap nodes:    curl -sSf http://<server-ip>:8181/bootstrap | sh

============================================================
DONE
