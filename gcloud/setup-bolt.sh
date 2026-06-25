#!/usr/bin/env bash
# Set up a spanner BOLT: Docker + AR auth + seed config, write the spanner block
# (mode=spanner, shared spanner_id, this bolt's hostname-pattern partition) into
# server.yaml, init+unlock the vault, install the unlimited load-test license
# (spanner refuses the built-in free-100 license; the community "free-100000000"
# license is NOT the free one, so it's accepted), run vigosrv, install vigocli.
#
# The spanner ceremony (init / invite / join) is driven AFTER this by the operator
# — see provision-spanner.sh next-steps.
#
#   sudo SPANNER_ID=alexander4-test BOLT_PATTERNS='*.a.test' bash /tmp/setup-bolt.sh
set -euo pipefail

AR_HOST=us-west1-docker.pkg.dev
AR_IMAGE_REPO="${AR_HOST}/project-69f2499e-5082-48f0-b19/vigo/vigo"
VERSION="$(cat /tmp/vigo-version 2>/dev/null || echo latest)"
IMAGE="${IMAGE:-${AR_IMAGE_REPO}:${VERSION}}"
DATA=/srv/vigo
SPANNER_ID="${SPANNER_ID:-alexander4-test}"
BOLT_PATTERNS="${BOLT_PATTERNS:?set BOLT_PATTERNS, e.g. '*.a.test'}"
CHECKIN_INTERVAL="${CHECKIN_INTERVAL:-30s}"

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }

echo "== Bolt image: ${IMAGE} · spanner_id=${SPANNER_ID} · patterns=${BOLT_PATTERNS} =="

echo "== Docker (egress via Cloud NAT) =="
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
echo "== yq =="
if ! command -v yq >/dev/null; then
  curl -fsSL -o /usr/local/bin/yq https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64
  chmod +x /usr/local/bin/yq
fi

echo "== Host FD ceiling =="
echo 'fs.file-max=2097152' >/etc/sysctl.d/99-vigo.conf
sysctl --system >/dev/null

echo "== Short hostname (the bolt's own name; not a managed envoy) =="
SHORT_HOST="$(hostname -s)"
hostnamectl set-hostname "$SHORT_HOST"
mkdir -p /etc/cloud/cloud.cfg.d
echo 'preserve_hostname: true' > /etc/cloud/cloud.cfg.d/99-vigo-preserve-hostname.cfg

echo "== Authenticate Docker to Artifact Registry =="
TOKEN=$(curl -s -H "Metadata-Flavor: Google" \
  "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token" \
  | grep -o '"access_token":"[^"]*' | cut -d'"' -f4)
echo "$TOKEN" | docker login -u oauth2accesstoken --password-stdin "https://$AR_HOST"

echo "== Seed config (no server start) =="
mkdir -p "$DATA" /var/lib/vigo/swarm
docker run --rm -v "$DATA:/srv/vigo" -v /usr/local/bin:/host-bin "$IMAGE" --seed-only

echo "== Patch server.yaml: spanner block + check-in cadence + hostname =="
yq -i ".checkin.max_concurrent = 0"                 "$DATA/server.yaml"
yq -i ".checkin.interval = \"${CHECKIN_INTERVAL}\"" "$DATA/server.yaml"
yq -i ".server.hostname = \"${SHORT_HOST}\""        "$DATA/server.yaml"
yq -i '.spanner.mode = "spanner"'                   "$DATA/server.yaml"
yq -i ".spanner.spanner_id = \"${SPANNER_ID}\""     "$DATA/server.yaml"
yq -i ".spanner.patterns = [\"${BOLT_PATTERNS}\"]"  "$DATA/server.yaml"
yq -i '.spanner.snapshot.tier1_interval = "30s"'    "$DATA/server.yaml"
yq -i '.spanner.snapshot.tier2_interval = "1m"'     "$DATA/server.yaml"   # multicast is dead on GCP → REST Tier-2 is the convergence path; tighten it for the test
yq -i '.spanner.transport.multicast_group = "224.0.0.45"' "$DATA/server.yaml"
yq -i '.spanner.transport.multicast_port = 1534'    "$DATA/server.yaml"
yq -i '.spanner.fallback_admin = "local"'           "$DATA/server.yaml"
yq -i '.license.mode = "local"'                     "$DATA/server.yaml"   # each bolt counts its own; aggregate needs all bolts gossiping first

echo "== Secrets vault init+unlock (pty-fed) =="
CREDS="${DATA}/.bolt-creds"
genpass() { printf 'Lt1!%s' "$(openssl rand -hex 8)"; }
if [ -f "$CREDS" ]; then . "$CREDS"; else VAULT_PASS="$(genpass)"; ( umask 077; printf 'VAULT_PASS=%q\n' "$VAULT_PASS" > "$CREDS" ); fi
printf '%s\n%s\n' "$VAULT_PASS" "$VAULT_PASS" \
  | script -qec "vigocli secrets init --key-file ${DATA}/secrets/.master.key" /dev/null >/dev/null 2>&1 \
  || printf '%s\n' "$VAULT_PASS" | script -qec "vigocli secrets unlock" /dev/null >/dev/null 2>&1 || true

echo "== Install unlimited load-test license (spanner needs a non-free license) =="
if [ -f /tmp/loadtest-unlimited.lic ]; then
  cp /tmp/loadtest-unlimited.lic "${DATA}/license/"; chmod 600 "${DATA}/license/loadtest-unlimited.lic"
  echo "  installed"
else
  echo "  ! /tmp/loadtest-unlimited.lic missing — spanner will refuse to start on the free license" >&2
fi

echo "== Run vigosrv (host net; spanner mode) =="
docker rm -f vigo 2>/dev/null || true
docker run -d --name vigo --restart unless-stopped \
  --network host --ulimit nofile=1048576:1048576 \
  -v "$DATA:/srv/vigo" -v /var/lib/vigo/swarm:/var/lib/vigo/swarm \
  -v /usr/local/bin:/host-bin -v /etc/localtime:/etc/localtime:ro \
  "$IMAGE"

echo "== Waiting for the bolt (admin socket + boot-minted admin token) =="
for _ in $(seq 1 30); do
  [ -S "${DATA}/run/admin.sock" ] && [ -f "${DATA}/cli-admin-token" ] && break
  sleep 2
done
docker logs vigo 2>&1 | grep -iE "spanner|bolt|max_connections|listening|started|refus|fail" | tail -8 || true
echo "== Bolt ready (run 'vigocli spanner init' on bolt-1, then invite/join the rest) =="
