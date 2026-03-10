#!/usr/bin/env bash
set -euo pipefail

# Factory reset a hobnob node: stop service, remove all agent and server state,
# so the node can be re-bootstrapped from scratch.

if [ "$(id -u)" -ne 0 ]; then
  echo "Run as root: sudo $0" >&2
  exit 1
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

echo "Done. Node is clean — ready to re-bootstrap."
