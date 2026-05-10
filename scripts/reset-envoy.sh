#!/usr/bin/env bash
set -euo pipefail

# Factory reset a vigo envoy: stop service, remove all agent and server state,
# purge envoy history from the database, so the envoy can be re-bootstrapped from scratch.
#
# Usage:
#   sudo scripts/reset-node.sh              # reset local envoy + purge DB
#   sudo scripts/reset-node.sh --no-db      # reset local envoy only (skip DB)

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

echo "Stopping and removing vigo service..."
systemctl stop vigo-envoy 2>/dev/null || true
systemctl disable vigo-envoy 2>/dev/null || true
rm -f /etc/systemd/system/vigo-envoy.service
systemctl daemon-reload
systemctl reset-failed vigo-envoy 2>/dev/null || true

# Also clean up old isogov/vigo service names if present.
for old in vigo isogov isogov-agent; do
  systemctl stop "$old" 2>/dev/null || true
  systemctl disable "$old" 2>/dev/null || true
  rm -f "/etc/systemd/system/${old}.service"
  systemctl reset-failed "$old" 2>/dev/null || true
done
systemctl daemon-reload

echo "Removing agent binaries (current + legacy)..."
rm -f /usr/local/sbin/vigo
rm -f /usr/local/bin/git-remote-swarmgit
rm -f /usr/local/bin/isogov-agent

echo "Removing agent data (/etc/vigo-envoy)..."
rm -rf /etc/vigo-envoy

echo "Removing agent LMDB state (/var/lib/vigo)..."
rm -rf /var/lib/vigo

echo "Removing server data (/srv/vigo), preserving license..."
find /srv/vigo -mindepth 1 \
  -path /srv/vigo/license -prune -o \
  -print0 | xargs -0 rm -rf

echo "Done. Envoy is clean — ready to re-bootstrap."
