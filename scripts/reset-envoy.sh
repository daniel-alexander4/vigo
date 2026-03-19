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
systemctl stop hobnob-thrall 2>/dev/null || true
systemctl disable hobnob-thrall 2>/dev/null || true
rm -f /etc/systemd/system/hobnob-thrall.service
systemctl daemon-reload
systemctl reset-failed hobnob-thrall 2>/dev/null || true

# Also clean up old isogov/hobnob service names if present.
for old in hobnob isogov isogov-agent; do
  systemctl stop "$old" 2>/dev/null || true
  systemctl disable "$old" 2>/dev/null || true
  rm -f "/etc/systemd/system/${old}.service"
  systemctl reset-failed "$old" 2>/dev/null || true
done
systemctl daemon-reload

echo "Removing agent binaries (current + legacy)..."
rm -f /usr/local/sbin/hobnob
rm -f /usr/local/bin/isogov-agent

echo "Removing agent data (/etc/hobnob-thrall)..."
rm -rf /etc/hobnob-thrall

echo "Removing agent LMDB state (/var/lib/hobnob)..."
rm -rf /var/lib/hobnob

echo "Removing server data (/srv/hobnob), preserving license..."
find /srv/hobnob -mindepth 1 \
  -path /srv/hobnob/license -prune -o \
  -print0 | xargs -0 rm -rf

echo "Done. Thrall is clean — ready to re-bootstrap."
