#!/usr/bin/env bash
# Set up vigo-srv: install Docker, authenticate to Artifact Registry, seed
# config, tune for the load test, run the vigosrv container, and create an
# admin web user (mapped to the invoking OS user) so the UI is usable. Prints
# the login + vault passphrase at the end (also saved root-only under /srv/vigo).
#
# Normally run by provision-gcp.sh (which writes /tmp/vigo-version and invokes
# this over IAP). To run standalone:
#   sudo bash /tmp/setup-server.sh                       # version from /tmp/vigo-version
#   sudo IMAGE=...vigo:0.76.x bash /tmp/setup-server.sh  # explicit override
#
# Version auto-detection: the tag comes from /tmp/vigo-version (written by
# provision-gcp.sh from the repo VERSION). It falls back to `latest` only if
# that marker is absent — and AR `latest` may be stale, so prefer the marker.
set -euo pipefail

AR_HOST=us-west1-docker.pkg.dev
AR_IMAGE_REPO="${AR_HOST}/project-69f2499e-5082-48f0-b19/vigo/vigo"
VERSION="$(cat /tmp/vigo-version 2>/dev/null || echo latest)"
IMAGE="${IMAGE:-${AR_IMAGE_REPO}:${VERSION}}"
DATA=/srv/vigo
MAX_CONCURRENT="${MAX_CONCURRENT:-2000}"   # admit rate during the connect storm

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
echo "== Server image: ${IMAGE} =="

echo "== Docker (egress via Cloud NAT) =="
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi

echo "== yq (clean server.yaml patch) =="
if ! command -v yq >/dev/null; then
  curl -fsSL -o /usr/local/bin/yq \
    https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
  chmod +x /usr/local/bin/yq
fi

echo "== Host FD ceiling (one FD per held stream) =="
echo 'fs.file-max=2097152' >/etc/sysctl.d/99-vigo.conf
sysctl --system >/dev/null

echo "== Authenticate Docker to Artifact Registry (VM service-account token) =="
TOKEN=$(curl -s -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" \
  | grep -o '"access_token":"[^"]*' | cut -d'"' -f4)
echo "$TOKEN" | docker login -u oauth2accesstoken --password-stdin "https://$AR_HOST"

echo "== Seed config (no server start) =="
# Mount /host-bin so the seed pass installs vigocli onto the host — we need it
# BEFORE the server starts to initialize the secrets vault (so vigosrv loads
# the master key we create, not a fresh one).
mkdir -p "$DATA" /var/lib/vigo/swarm
docker run --rm -v "$DATA:/srv/vigo" -v /usr/local/bin:/host-bin "$IMAGE" --seed-only

echo "== Tune server.yaml =="
# yq creates the key/parent if absent. trusted_enrollment already covers
# RFC1918 (10.17.17.0/24) by default, so fakefleet enrolls tokenlessly.
yq -i ".checkin.max_concurrent = ${MAX_CONCURRENT}" "$DATA/server.yaml"

echo "== Web-user credentials (persisted root-only for idempotent re-runs) =="
CREDS="${DATA}/.loadtest-creds"
genpass() { printf 'Lt1!%s' "$(openssl rand -hex 8)"; }   # 20 chars: upper+lower+digit+special
if [ -f "$CREDS" ]; then
  # shellcheck disable=SC1090
  . "$CREDS"; FRESH_VAULT=0
else
  OSUSER="${SUDO_USER:-$(logname 2>/dev/null || echo admin)}"
  VAULT_PASS="$(genpass)"; WEB_PASS="$(genpass)"
  ( umask 077; printf 'OSUSER=%q\nVAULT_PASS=%q\nWEB_PASS=%q\n' "$OSUSER" "$VAULT_PASS" "$WEB_PASS" > "$CREDS" )
  FRESH_VAULT=1
fi

echo "== Secrets vault init+unlock (pty-fed; the passphrase reader needs a TTY) =="
# `secrets init` writes the master key AND the unlock sentinel, so one pty call
# covers init+unlock. `script` supplies the TTY term.ReadPassword requires —
# there is no --password-stdin path for the vault passphrase.
if [ "$FRESH_VAULT" -eq 1 ]; then
  printf '%s\n%s\n' "$VAULT_PASS" "$VAULT_PASS" \
    | script -qec "vigocli secrets init --key-file ${DATA}/secrets/.master.key" /dev/null >/dev/null
else
  printf '%s\n' "$VAULT_PASS" | script -qec "vigocli secrets unlock" /dev/null >/dev/null
fi

echo "== OS-user ed25519 keypair (admin webuser maps to ${OSUSER}'s own pubkey) =="
OSHOME="$(getent passwd "$OSUSER" | cut -d: -f6)"
if [ -n "$OSHOME" ] && [ ! -f "${OSHOME}/.ssh/id_ed25519.pub" ]; then
  sudo -u "$OSUSER" install -d -m700 "${OSHOME}/.ssh"
  sudo -u "$OSUSER" ssh-keygen -t ed25519 -N '' -q -f "${OSHOME}/.ssh/id_ed25519"
fi

echo "== Run vigosrv (host net; binds 1530/8443/1531; FD limit lifted) =="
docker rm -f vigo 2>/dev/null || true
docker run -d --name vigo --restart unless-stopped \
  --network host \
  --ulimit nofile=1048576:1048576 \
  -v "$DATA:/srv/vigo" \
  -v /var/lib/vigo/swarm:/var/lib/vigo/swarm \
  -v /usr/local/bin:/host-bin \
  -v /etc/localtime:/etc/localtime:ro \
  "$IMAGE"

echo "== Waiting for the server (admin socket + boot-minted admin token) =="
for _ in $(seq 1 30); do
  [ -S "${DATA}/run/admin.sock" ] && [ -f "${DATA}/cli-admin-token" ] && break
  sleep 2
done
docker logs vigo 2>&1 | grep -iE "max_connections|listening|started" | tail -5 || true

echo "== Admin webuser ${OSUSER} =="
if vigocli webusers list 2>/dev/null | grep -qw "$OSUSER"; then
  echo "  ${OSUSER} already exists — skipping"
else
  printf '%s' "$WEB_PASS" | vigocli webusers create --username "$OSUSER" --role admin --password-stdin \
    && echo "  created"
fi

EXT_IP="$(curl -s -H 'Metadata-Flavor: Google' \
  http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip 2>/dev/null || true)"

echo
echo "== Done =="
[ -n "$EXT_IP" ] && echo "  Web UI:   https://${EXT_IP}:8443"
echo "  Login:    ${OSUSER} / ${WEB_PASS}"
echo "  Vault passphrase: ${VAULT_PASS}   (creds saved root-only at ${CREDS})"
echo "  Metrics:  curl -sk https://127.0.0.1:8443/metrics | grep vigo_streams_active"
