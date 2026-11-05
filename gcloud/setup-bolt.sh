#!/usr/bin/env bash
# Set up a spanner BOLT: Docker + AR auth + seed config, write the spanner block
# (mode=spanner, shared spanner_id, this bolt's hostname-pattern partition) into
# server.yaml, init+unlock the vault, run vigosrv, install vigocli.
#
# Founder: run 'vigocli spanner init' once after this. Joiners: set SPANNER_PEERS to
# the founder's address and stage the shared CA (/tmp/fleet-ca{,_key}) — the joiner
# auto-admits on boot, no manual invite/join ceremony. (The legacy ceremony still
# works if SPANNER_PEERS is unset — see provision-spanner.sh next-steps.)
#
#   # founder (lists the joiners as its who-may-join allowlist):
#   sudo SPANNER_ID=alexander4-test BOLT_PATTERNS='*.a.test' SPANNER_PEERS='10.0.0.3' bash /tmp/setup-bolt.sh
#   # joiner (points at the founder + adopts its CA):
#   sudo SPANNER_ID=alexander4-test BOLT_PATTERNS='*.b.test' SPANNER_PEERS='10.0.0.2' bash /tmp/setup-bolt.sh
set -euo pipefail

IMAGE_REPO="ghcr.io/daniel-alexander4/vigo"
VERSION="$(cat /tmp/vigo-version 2>/dev/null || echo latest)"
IMAGE="${IMAGE:-${IMAGE_REPO}:${VERSION}}"
DATA=/srv/vigo
SPANNER_ID="${SPANNER_ID:-alexander4-test}"
BOLT_PATTERNS="${BOLT_PATTERNS:?set BOLT_PATTERNS, e.g. '*.a.test'}"
CHECKIN_INTERVAL="${CHECKIN_INTERVAL:-30s}"

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }

echo "== Bolt image: ${IMAGE} · spanner_id=${SPANNER_ID} · patterns=${BOLT_PATTERNS} =="

echo "== Docker (egress via Cloud NAT) =="
# Distro docker.io (signed by the Ubuntu archive), not an unpinned remote script.
if ! command -v docker >/dev/null; then
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io
  systemctl enable --now docker
fi
echo "== yq =="
# Pinned release, verified against the SHA-256 in that release's checksums file.
YQ_VERSION=v4.44.3
YQ_SHA256=a2c097180dd884a8d50c956ee16a9cec070f30a7947cf4ebf87d5f36213e9ed7
if ! command -v yq >/dev/null; then
  yq_tmp="$(mktemp)"
  curl -fsSL -o "$yq_tmp" "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64"
  echo "${YQ_SHA256}  ${yq_tmp}" | sha256sum -c --quiet - \
    || { echo "yq ${YQ_VERSION} checksum mismatch" >&2; rm -f "$yq_tmp"; exit 1; }
  install -m 0755 "$yq_tmp" /usr/local/bin/yq
  rm -f "$yq_tmp"
fi

echo "== Host FD ceiling =="
echo 'fs.file-max=2097152' >/etc/sysctl.d/99-vigo.conf
sysctl --system >/dev/null

echo "== Short hostname (the bolt's own name; not a managed envoy) =="
SHORT_HOST="$(hostname -s)"
hostnamectl set-hostname "$SHORT_HOST"
mkdir -p /etc/cloud/cloud.cfg.d
echo 'preserve_hostname: true' > /etc/cloud/cloud.cfg.d/99-vigo-preserve-hostname.cfg

echo "== Pull ${IMAGE} (public on ghcr.io, no login) =="
docker pull "$IMAGE"

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
# Config-driven hands-off peering (ADR-026): on the founder, SPANNER_PEERS is the
# joiners' who-may-join allowlist; on a joiner, point it at the founder. Comma-
# separated. Joiners auto-admit on boot — no manual invite/join ceremony.
if [ -n "${SPANNER_PEERS:-}" ]; then
  peers_json="[\"$(echo "$SPANNER_PEERS" | sed 's/[[:space:]]//g; s/,/","/g')\"]"
  yq -i ".spanner.peers = ${peers_json}" "$DATA/server.yaml"
  echo "  spanner.peers = ${peers_json}"
fi

# Shared fleet CA (off-wire): if the founder's CA material was staged here, drop it
# into the bolt's tls/ BEFORE boot. vigosrv's EnsureTLSCerts re-signs this bolt a
# correct server cert from the shared CA on boot (the staged SANs won't match), so
# peer mTLS and routed-envoy check-in both verify against one CA.
if [ -f /tmp/fleet-ca ] && [ -f /tmp/fleet-ca_key ]; then
  echo "== Adopt shared fleet CA (staged from the founder) =="
  install -m 644 /tmp/fleet-ca     "${DATA}/tls/ca"
  install -m 600 /tmp/fleet-ca_key "${DATA}/tls/ca_key"
  rm -f "${DATA}/tls/cert" "${DATA}/tls/key"   # force EnsureTLSCerts to re-sign from the shared CA
fi

echo "== Secrets vault init+unlock (pty-fed) =="
CREDS="${DATA}/.bolt-creds"
genpass() { printf 'Lt1!%s' "$(openssl rand -hex 8)"; }
if [ -f "$CREDS" ]; then . "$CREDS"; else VAULT_PASS="$(genpass)"; ( umask 077; printf 'VAULT_PASS=%q\n' "$VAULT_PASS" > "$CREDS" ); fi
printf '%s\n%s\n' "$VAULT_PASS" "$VAULT_PASS" \
  | script -qec "vigocli secrets init --key-file ${DATA}/secrets/.master.key" /dev/null >/dev/null 2>&1 \
  || printf '%s\n' "$VAULT_PASS" | script -qec "vigocli secrets unlock" /dev/null >/dev/null 2>&1 || true

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
if [ -n "${SPANNER_PEERS:-}" ]; then
  echo "== Bolt ready (config-driven peering: founder runs 'vigocli spanner init'; joiners auto-admit on boot) =="
else
  echo "== Bolt ready (run 'vigocli spanner init' on bolt-1, then invite/join the rest) =="
fi
