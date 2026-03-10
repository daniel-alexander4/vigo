#!/usr/bin/env bash
set -euo pipefail

# Factory reset a hobnob node: stop service, remove all agent and server state,
# purge node history from the database, so the node can be re-bootstrapped from scratch.
#
# Usage:
#   sudo scripts/reset-node.sh              # reset local node + purge DB
#   sudo scripts/reset-node.sh --no-db      # reset local node only (skip DB)

if [ "$(id -u)" -ne 0 ]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

SKIP_DB=false
for arg in "$@"; do
  case "$arg" in
    --no-db) SKIP_DB=true ;;
  esac
done

# Preserve server config across reset.
echo "Backing up /etc/hobnob-server to /tmp/hobnob-server-backup..."
rm -rf /tmp/hobnob-server-backup
cp -a /etc/hobnob-server /tmp/hobnob-server-backup

# Purge DB while postgres is still running.
if [ "$SKIP_DB" = false ]; then
  echo "Purging nodes and history from database..."
  docker compose exec -T postgres psql -U hobnob -d hobnob -q <<'SQL'
TRUNCATE nodes CASCADE;
TRUNCATE compliance_history;
TRUNCATE secret_rotation_events;
TRUNCATE env_promotions;
SQL
  echo "Database purged."
fi

echo "Stopping and removing hobnob-node service..."
systemctl stop hobnob-node 2>/dev/null || true
systemctl disable hobnob-node 2>/dev/null || true
rm -f /etc/systemd/system/hobnob-node.service
systemctl daemon-reload
systemctl reset-failed hobnob-node 2>/dev/null || true

echo "Removing agent binary..."
rm -f /usr/local/bin/hobnob

echo "Removing agent data (/etc/hobnob-node)..."
rm -rf /etc/hobnob-node

echo "Removing server data (/etc/hobnob-server)..."
rm -rf /etc/hobnob-server
mkdir -p /etc/hobnob-server

echo "Restoring server config from backup..."
cp -a /tmp/hobnob-server-backup/. /etc/hobnob-server/
rm -rf /tmp/hobnob-server-backup

echo "Done. Node is clean — ready to re-bootstrap."
