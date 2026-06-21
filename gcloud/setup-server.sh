#!/usr/bin/env bash
# Set up vigo-srv: install Docker, authenticate to Artifact Registry, seed
# config, tune for the load test, and run the vigosrv container.
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
mkdir -p "$DATA" /var/lib/vigo/swarm
docker run --rm -v "$DATA:/srv/vigo" "$IMAGE" --seed-only

echo "== Tune server.yaml =="
# yq creates the key/parent if absent. trusted_enrollment already covers
# RFC1918 (10.17.17.0/24) by default, so fakefleet enrolls tokenlessly.
yq -i ".checkin.max_concurrent = ${MAX_CONCURRENT}" "$DATA/server.yaml"

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

echo "== Waiting for startup; admission cap should be ~60k on a 32 GiB box =="
sleep 6
docker logs vigo 2>&1 | grep -iE "max_connections|listening|started" | tail -5 || docker logs vigo --tail 20
echo
echo "Metrics check:  curl -sk https://127.0.0.1:8443/metrics | grep vigo_streams_active"
