#!/usr/bin/env bash
set -euo pipefail

# Factory reset the vigo envoy (agent) on this host so it can be
# re-bootstrapped from scratch: stop and remove the systemd service, the
# agent binary, its config/identity (/etc/vigo-envoy) and its local state
# (/var/lib/vigo). Linux/systemd only.
#
# Server state under /srv/vigo is NEVER touched, so this is safe on a
# server host that also runs its own envoy. To reset a server, follow
# "Resetting the Server" in docs/howto/operate/troubleshoot.md.
#
# Usage:
#   sudo scripts/reset-envoy.sh

usage() {
  echo "Usage: sudo $0" >&2
  echo "Resets the local vigo envoy. Takes no options; server state in /srv/vigo is never touched." >&2
}

if [ "$#" -ne 0 ]; then
  case "$1" in
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

echo "Stopping and removing the vigo-envoy service..."
systemctl stop vigo-envoy 2>/dev/null || true
systemctl disable vigo-envoy 2>/dev/null || true
rm -f /etc/systemd/system/vigo-envoy.service
systemctl daemon-reload
systemctl reset-failed vigo-envoy 2>/dev/null || true

echo "Removing the agent binary..."
rm -f /usr/local/sbin/vigo
rm -f /usr/local/bin/git-remote-gitback

echo "Removing agent config and identity (/etc/vigo-envoy)..."
rm -rf /etc/vigo-envoy

echo "Removing agent state (/var/lib/vigo)..."
rm -rf /var/lib/vigo

echo "Done. The envoy is clean and ready to re-bootstrap. /srv/vigo was not touched."
