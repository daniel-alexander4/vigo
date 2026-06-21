#!/usr/bin/env bash
# Set up vigo-srv: install Docker, authenticate to Artifact Registry, seed
# config, tune for the load test, and run the vigosrv container.
#
# Copy to the server and run as root:
#   gcloud compute scp setup-server.sh vigo-srv:/tmp/ --zone=us-west1-a --tunnel-through-iap
#   gcloud compute ssh vigo-srv --zone=us-west1-a --tunnel-through-iap
#   sudo IMAGE=us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:0.76.16 bash /tmp/setup-server.sh
#
# IMPORTANT: the AR `vigo` image is stale at 0.65.6. Build + push a current
# 0.76.x from danlap first, and pass its tag via IMAGE=, or this runs a
# months-old server missing all the held-stream work.
set -euo pipefail

IMAGE="${IMAGE:-us-west1-docker.pkg.dev/project-69f2499e-5082-48f0-b19/vigo/vigo:latest}"
AR_HOST=us-west1-docker.pkg.dev
DATA=/srv/vigo
MAX_CONCURRENT="${MAX_CONCURRENT:-2000}"   # admit rate during the connect storm

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }

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
