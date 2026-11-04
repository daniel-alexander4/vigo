#!/usr/bin/env bash
# Set up vigo-srv: install Docker, authenticate to Artifact Registry, set a
# short hostname, seed config, tune server.yaml (max_concurrent, 1s check-in,
# server.hostname), run vigosrv, and publish the demo fakefleet config tree
# (<stage>/demo-stacks → configcrates/roles/usercrates/hostcrates) so synthetic
# envoys converge against real bundles. Initializes + unlocks the secrets vault
# so the operator can create a web user immediately, then prints the UI URL,
# where the vault passphrase is kept, and create-user commands. Web-user
# creation is left to you.
#
# Normally run by provision-gcp.sh, which stages this script, vigo-version and
# demo-stacks/ into ~/vigo-stage (mode 0700) and invokes this over IAP. Inputs
# are read only from that private directory, never from world-writable /tmp,
# because this runs as root. To run standalone:
#   sudo bash ~/vigo-stage/setup-server.sh                       # version from ~/vigo-stage/vigo-version
#   sudo IMAGE=...vigo:0.76.x bash ~/vigo-stage/setup-server.sh  # explicit override
#   sudo STAGE_DIR=/some/private/dir bash .../setup-server.sh     # other stage dir
#
# Version auto-detection: the tag comes from <stage>/vigo-version (written by
# provision-gcp.sh from the repo VERSION). It falls back to `latest` only if
# that marker is absent — and AR `latest` may be stale, so prefer the marker.
set -euo pipefail

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }

# The stage dir must belong to root or the invoking user and be writable by
# nobody else, or a local user could plant the image tag or the config tree.
if [ -z "${STAGE_DIR:-}" ]; then
  STAGE_HOME=""
  [ -n "${SUDO_USER:-}" ] && STAGE_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6 || true)"
  STAGE_DIR="${STAGE_HOME:-$HOME}/vigo-stage"
fi
if [ -e "$STAGE_DIR" ]; then
  stage_owner="$(stat -c %U "$STAGE_DIR")"
  if [ "$stage_owner" != "root" ] && [ "$stage_owner" != "${SUDO_USER:-root}" ]; then
    echo "refusing stage dir ${STAGE_DIR}: owned by ${stage_owner}" >&2; exit 1
  fi
  if [ -n "$(find "$STAGE_DIR" -maxdepth 0 -perm /022)" ]; then
    echo "refusing stage dir ${STAGE_DIR}: group- or world-writable" >&2; exit 1
  fi
fi

AR_HOST=us-west1-docker.pkg.dev
AR_IMAGE_REPO="${AR_HOST}/project-69f2499e-5082-48f0-b19/vigo/vigo"
VERSION="$(cat "${STAGE_DIR}/vigo-version" 2>/dev/null || echo latest)"
IMAGE="${IMAGE:-${AR_IMAGE_REPO}:${VERSION}}"
DATA=/srv/vigo
# 0 = the CPU-scaled default (max(128, GOMAXPROCS*16)) — the check-in admission
# governor that protects CPU/UI by shedding excess bundle-builds. Representative
# of how a real server runs; a high override (e.g. 2000) disables that protection
# and lets a fast-cadence fleet CPU-saturate, which is NOT how to size.
MAX_CONCURRENT="${MAX_CONCURRENT:-0}"

# Catppuccin Mocha, 24-bit RGB. Set NO_COLOR=1 to disable.
if [ -z "${NO_COLOR:-}" ]; then
  C_GREEN=$'\033[38;2;166;227;161m'   # success / headings
  C_BLUE=$'\033[38;2;137;180;250m'    # URL
  C_YELLOW=$'\033[38;2;249;226;175m'  # credentials
  C_MAUVE=$'\033[38;2;203;166;247m'   # labels
  C_TEAL=$'\033[38;2;148;226;213m'    # hints
  C_BOLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_GREEN= C_BLUE= C_YELLOW= C_MAUVE= C_TEAL= C_BOLD= C_RST=
fi

echo "== Server image: ${IMAGE} =="

echo "== Docker (egress via Cloud NAT) =="
# The distro's docker.io package: signed by the Ubuntu archive and versioned
# with the image, rather than piping an unpinned remote script into a root sh.
if ! command -v docker >/dev/null; then
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io
  systemctl enable --now docker
fi

echo "== yq (clean server.yaml patch) =="
# Pinned release, verified against the SHA-256 in that release's checksums file.
YQ_VERSION=v4.44.3
YQ_SHA256=a2c097180dd884a8d50c956ee16a9cec070f30a7947cf4ebf87d5f36213e9ed7
if ! command -v yq >/dev/null; then
  yq_tmp="$(mktemp)"
  curl -fsSL -o "$yq_tmp" \
    "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64"
  echo "${YQ_SHA256}  ${yq_tmp}" | sha256sum -c --quiet - \
    || { echo "yq ${YQ_VERSION} checksum mismatch" >&2; rm -f "$yq_tmp"; exit 1; }
  install -m 0755 "$yq_tmp" /usr/local/bin/yq
  rm -f "$yq_tmp"
fi

echo "== Host FD ceiling (one FD per held stream) =="
echo 'fs.file-max=2097152' >/etc/sysctl.d/99-vigo.conf
sysctl --system >/dev/null

echo "== Short hostname (so the local agent enrolls short, not GCP's FQDN) =="
# GCP sets the kernel hostname to the FQDN via metadata + cloud-init
# (preserve_hostname: false). Set it short and stop cloud-init/guest-agent
# resetting it — BEFORE vigosrv starts (server.hostname must match) and before
# any loopback agent bootstrap. `hostname -s` is the first label of the FQDN.
SHORT_HOST="$(hostname -s)"
hostnamectl set-hostname "$SHORT_HOST"
mkdir -p /etc/cloud/cloud.cfg.d
echo 'preserve_hostname: true' > /etc/cloud/cloud.cfg.d/99-vigo-preserve-hostname.cfg
echo "  hostname set to ${SHORT_HOST}"

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
yq -i '.checkin.interval = "1s"' "$DATA/server.yaml"               # fast cadence for load testing
yq -i ".server.hostname = \"${SHORT_HOST}\"" "$DATA/server.yaml"   # match the short OS hostname

echo "== Web-user credentials (persisted root-only for idempotent re-runs) =="
CREDS="${DATA}/.loadtest-creds"
genpass() { printf 'Lt1!%s' "$(openssl rand -hex 8)"; }   # 20 chars: upper+lower+digit+special
if [ -f "$CREDS" ]; then
  # shellcheck disable=SC1090
  . "$CREDS"; FRESH_VAULT=0
else
  OSUSER="${SUDO_USER:-$(logname 2>/dev/null || echo admin)}"
  VAULT_PASS="$(genpass)"
  ( umask 077; printf 'OSUSER=%q\nVAULT_PASS=%q\n' "$OSUSER" "$VAULT_PASS" > "$CREDS" )
  FRESH_VAULT=1
fi

echo "== Secrets vault init+unlock (pty-fed; the passphrase reader needs a TTY) =="
# `secrets init` writes the master key and the unlock verifier but does NOT open
# the unlock window, so a fresh vault needs init AND unlock. `script` supplies
# the TTY term.ReadPassword requires — there is no --password-stdin path for the
# vault passphrase.
if [ "$FRESH_VAULT" -eq 1 ]; then
  printf '%s\n%s\n' "$VAULT_PASS" "$VAULT_PASS" \
    | script -qec "vigocli secrets init --key-file ${DATA}/secrets/.master.key" /dev/null >/dev/null
fi
printf '%s\n' "$VAULT_PASS" | script -qec "vigocli secrets unlock" /dev/null >/dev/null
# Report what the gate actually says, not what we expect it to say.
VAULT_STATE="locked"
[ -f "${DATA}/.secrets-unlocked" ] && VAULT_STATE="unlocked"

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

echo "== Publish demo fakefleet config (configcrates/roles/usercrates/hostcrates) =="
# Replace the seeded starter stacks with the demo tree so synthetic envoys match
# a hostcrate and converge against a real (non-empty) bundle — exercising the
# CPU-heavy bundle-build path the load test is meant to stress. Then publish.
if [ -d "${STAGE_DIR}/demo-stacks" ]; then
  rm -rf "${DATA}/stacks"/*
  cp -a "${STAGE_DIR}/demo-stacks/." "${DATA}/stacks/"
  vigocli config publish && echo "  demo config published" \
    || echo "  ! demo config publish failed — see output above" >&2
else
  echo "  (no ${STAGE_DIR}/demo-stacks staged — synthetic envoys will get empty bundles)"
fi

echo "== Server ready — web-user creation left to the operator =="

EXT_IP="$(curl -s -H 'Metadata-Flavor: Google' \
  http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip 2>/dev/null || true)"

echo
printf '%s%s== Web access ==%s\n' "$C_BOLD" "$C_GREEN" "$C_RST"
[ -n "$EXT_IP" ] && printf '  %sWeb UI:%s   %s%s%s\n' "$C_MAUVE" "$C_RST" "$C_BLUE" "https://${EXT_IP}:8443" "$C_RST"
printf '  %sVault:%s    %s  %s(passphrase is root-only in %s)%s\n' \
  "$C_MAUVE" "$C_RST" "$VAULT_STATE" "$C_TEAL" "$CREDS" "$C_RST"
printf '%s  Create your web user:%s\n' "$C_BOLD" "$C_RST"
printf '    %sviewer%s (enough to watch the run):  %svigocli webusers create --username %s --role viewer%s\n' \
  "$C_GREEN" "$C_RST" "$C_YELLOW" "$OSUSER" "$C_RST"
printf '    %sadmin%s: server.hostname is already set to %s%s%s — just enroll the local agent, then create:\n' \
  "$C_GREEN" "$C_RST" "$C_YELLOW" "$SHORT_HOST" "$C_RST"
printf '          %scurl -sSfk https://127.0.0.1:8443/bootstrap | sudo sh%s\n' "$C_TEAL" "$C_RST"
printf '          %svigocli webusers create --username %s --role admin%s\n' "$C_TEAL" "$OSUSER" "$C_RST"
printf '  %sMetrics:%s  %scurl -sk https://127.0.0.1:8443/metrics | grep vigo_streams_active%s\n' \
  "$C_MAUVE" "$C_RST" "$C_TEAL" "$C_RST"
