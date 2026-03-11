#!/usr/bin/env bash
set -euo pipefail

# Factory reset a hobnob thrall: stop service, remove all agent and server state,
# purge thrall history from the database, so the thrall can be re-bootstrapped from scratch.
#
# Usage:
#   sudo scripts/reset-node.sh              # reset local thrall + purge DB
#   sudo scripts/reset-node.sh --no-db      # reset local thrall only (skip DB)

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

echo "Stopping and removing hobnob service..."
systemctl stop hobnob 2>/dev/null || true
systemctl disable hobnob 2>/dev/null || true
rm -f /etc/systemd/system/hobnob.service
systemctl daemon-reload
systemctl reset-failed hobnob 2>/dev/null || true

echo "Removing agent binary..."
rm -f /usr/local/bin/hobnob

echo "Removing agent data (/etc/hobnob-thrall)..."
rm -rf /etc/hobnob-thrall

echo "Removing server data (/etc/hobnob-server), preserving license..."
find /etc/hobnob-server -mindepth 1 \
  -path /etc/hobnob-server/license -prune -o \
  -print0 | xargs -0 rm -rf

echo "Done. Thrall is clean — ready to re-bootstrap."
