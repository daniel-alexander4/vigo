#!/usr/bin/env bash
# Stand up AND provision the GCP held-stream load-test environment, end to end:
#   - ensures firewall + Cloud NAT on vigo-vpc
#   - creates vigo-srv (8 vCPU/32 GiB) + vigo-test-harness (8 vCPU/48 GiB),
#     both --no-address (egress via NAT; Artifact Registry via Private Google Access)
#   - auto-detects the server version from the repo VERSION file
#   - builds the fakefleet + vigocli harness binaries
#   - scp's the setup scripts, harness binaries, and a version marker to each VM
#     over IAP, then runs the setup scripts remotely (unless --no-setup)
#
# Run from the repo on a host with gcloud + go authed (danlap). Idempotent:
# re-running skips existing infra and re-copies / re-runs setup.
#
#   ./gcloud/provision-gcp.sh                       # full: provision + remote setup
#   ./gcloud/provision-gcp.sh --no-setup            # provision + copy only
#   ./gcloud/provision-gcp.sh --server-external-ip  # also expose the UI on a public IP
#
# --server-external-ip gives vigo-srv an ephemeral external IP and opens 8443
# inbound to YOUR current public IP only (so you can reach the web UI from a
# laptop). The harness always reaches the server over the internal subnet, so
# this is purely for UI access. Idempotent; re-running refreshes the source IP.
#
# Prereq: the server image must already be in Artifact Registry at the repo's
# VERSION — run ./push2AR.sh first. This script checks and fails early if not.
#
# Tear down:
#   gcloud compute instances delete vigo-srv vigo-test-harness --zone=us-west1-a
#   gcloud compute firewall-rules delete vigo-allow-ui   # if --server-external-ip was used
set -euo pipefail

PROJECT=project-69f2499e-5082-48f0-b19
REGION=us-west1
ZONE=us-west1-a
NETWORK=vigo-vpc
SUBNET=vigo-subnet
IMAGE_FAMILY=ubuntu-2404-lts-amd64
IMAGE_PROJECT=ubuntu-os-cloud
AR_IMAGE_REPO="${REGION}-docker.pkg.dev/${PROJECT}/vigo/vigo"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

RUN_SETUP=1
SERVER_EXTERNAL_IP=0
for arg in "$@"; do
  case "$arg" in
    --no-setup) RUN_SETUP=0 ;;
    --server-external-ip) SERVER_EXTERNAL_IP=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

command -v gcloud >/dev/null || { echo "gcloud not in PATH" >&2; exit 1; }
command -v go     >/dev/null || { echo "go not in PATH (needed to build the harness binaries)" >&2; exit 1; }

# Catppuccin Mocha, 24-bit RGB. Set NO_COLOR=1 to disable.
if [ -z "${NO_COLOR:-}" ]; then
  C_GREEN=$'\033[38;2;166;227;161m' C_BLUE=$'\033[38;2;137;180;250m'
  C_YELLOW=$'\033[38;2;249;226;175m' C_MAUVE=$'\033[38;2;203;166;247m'
  C_TEAL=$'\033[38;2;148;226;213m' C_BOLD=$'\033[1m' C_RST=$'\033[0m'
else
  C_GREEN= C_BLUE= C_YELLOW= C_MAUVE= C_TEAL= C_BOLD= C_RST=
fi

g() { gcloud --project="$PROJECT" "$@"; }

# scp a file to a VM over IAP, retrying while the freshly-booted VM's IAP SSH
# comes up (sshd + IAP tunnel can take ~30-60s after instance create).
scp_iap() {
  local src=$1 vm=$2 tries=0
  until g compute scp "$src" "${vm}:/tmp/" --zone="$ZONE" --tunnel-through-iap --quiet 2>/dev/null; do
    tries=$((tries + 1))
    if [ "$tries" -ge 12 ]; then
      echo "  ! scp $(basename "$src") -> ${vm} failed after ${tries} tries (IAP SSH not ready?)" >&2
      return 1
    fi
    echo "  ... ${vm} not ready for scp yet (try ${tries}); waiting 15s"
    sleep 15
  done
  echo "  ok: $(basename "$src") -> ${vm}:/tmp/"
}

ssh_iap() { g compute ssh "$1" --zone="$ZONE" --tunnel-through-iap --quiet --command="$2"; }

# Give vigo-srv an ephemeral external IP and open 8443 to the caller's IP only.
ensure_server_external_ip() {
  if g compute instances describe vigo-srv --zone="$ZONE" \
       --format="value(networkInterfaces[0].accessConfigs[0].natIP)" 2>/dev/null | grep -q .; then
    echo "  vigo-srv already has an external IP"
  else
    g compute instances add-access-config vigo-srv --zone="$ZONE"
  fi
  # Force IPv4 — the firewall rule guards the server's external IPv4, and an
  # IPv6 source with /32 is rejected. Non-fatal throughout: a UI-firewall hiccup
  # must never abort provisioning the actual test env (the harness uses the
  # internal subnet, not this rule).
  local myip; myip="$(curl -4 -fsS ifconfig.me 2>/dev/null || true)"
  case "$myip" in
    *.*.*.*) : ;;
    *) echo "  ! no IPv4 detected ('${myip}') — open 8443 manually (vigo-allow-ui)" >&2; return 0 ;;
  esac
  if g compute firewall-rules describe vigo-allow-ui >/dev/null 2>&1; then
    g compute firewall-rules update vigo-allow-ui --source-ranges="${myip}/32" \
      || { echo "  ! firewall update failed; open 8443 manually" >&2; return 0; }
  else
    g compute firewall-rules create vigo-allow-ui --network="$NETWORK" \
      --direction=INGRESS --action=ALLOW --rules=tcp:8443 --source-ranges="${myip}/32" \
      || { echo "  ! firewall create failed; open 8443 manually" >&2; return 0; }
  fi
  echo "  8443 open to ${myip}/32"
}

# --- Version + image preflight ------------------------------------------------
VERSION="$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")"
[ -n "$VERSION" ] || { echo "empty VERSION at ${REPO_ROOT}/VERSION" >&2; exit 1; }
IMAGE="${AR_IMAGE_REPO}:${VERSION}"
echo "== Server version ${VERSION} (image ${IMAGE}) =="
g artifacts docker images describe "${IMAGE}" >/dev/null 2>&1 || {
  echo "  ! ${IMAGE} is not in Artifact Registry. Build + push it first:" >&2
  echo "      cd ${REPO_ROOT} && ./push2AR.sh" >&2
  exit 1
}

# --- Build harness binaries (pure-Go static) ---------------------------------
echo "== Building harness binaries =="
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o /tmp/vigo-fakefleet ./test/fakefleet )
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o /tmp/vigocli        ./cli )
echo "$VERSION" > /tmp/vigo-version

echo "== Firewall (idempotent) =="
g compute firewall-rules describe vigo-allow-internal >/dev/null 2>&1 || \
  g compute firewall-rules create vigo-allow-internal --network="$NETWORK" \
    --direction=INGRESS --action=ALLOW --rules=tcp,udp,icmp --source-ranges=10.17.17.0/24
g compute firewall-rules describe vigo-allow-iap >/dev/null 2>&1 || \
  g compute firewall-rules create vigo-allow-iap --network="$NETWORK" \
    --direction=INGRESS --action=ALLOW --rules=tcp:22,tcp:3389,tcp:8443 --source-ranges=35.235.240.0/20

echo "== Cloud NAT (egress for --no-address VMs) =="
g compute routers describe vigo-router --region="$REGION" >/dev/null 2>&1 || \
  g compute routers create vigo-router --network="$NETWORK" --region="$REGION"
g compute routers nats describe vigo-nat --router=vigo-router --region="$REGION" >/dev/null 2>&1 || \
  g compute routers nats create vigo-nat --router=vigo-router --region="$REGION" \
    --auto-allocate-nat-external-ips --nat-all-subnet-ip-ranges

echo "== Server VM: 8 vCPU / 32 GiB, SSD boot (SQLite WAL throughput) =="
g compute instances describe vigo-srv --zone="$ZONE" >/dev/null 2>&1 || \
  g compute instances create vigo-srv --zone="$ZONE" \
    --machine-type=e2-custom-8-32768 \
    --network="$NETWORK" --subnet="$SUBNET" --no-address \
    --image-family="$IMAGE_FAMILY" --image-project="$IMAGE_PROJECT" \
    --boot-disk-size=50GB --boot-disk-type=pd-ssd

echo "== Harness VM: 8 vCPU / 48 GiB =="
g compute instances describe vigo-test-harness --zone="$ZONE" >/dev/null 2>&1 || \
  g compute instances create vigo-test-harness --zone="$ZONE" \
    --machine-type=e2-custom-8-49152 \
    --network="$NETWORK" --subnet="$SUBNET" --no-address \
    --image-family="$IMAGE_FAMILY" --image-project="$IMAGE_PROJECT" \
    --boot-disk-size=30GB --boot-disk-type=pd-balanced

if [ "$SERVER_EXTERNAL_IP" -eq 1 ]; then
  echo "== Server external IP + UI firewall =="
  ensure_server_external_ip
fi

echo "== Copying scripts + binaries to the VMs (via IAP) =="
scp_iap "${SCRIPT_DIR}/setup-server.sh"     vigo-srv
scp_iap "/tmp/vigo-version"                 vigo-srv
scp_iap "${SCRIPT_DIR}/loadtest-unlimited.lic" vigo-srv
# Demo config tree (configcrates/roles/usercrates/hostcrates) for realistic
# bundles — recursive copy to /tmp/demo-stacks on the server.
g compute scp --recurse "${SCRIPT_DIR}/demo-stacks" vigo-srv:/tmp/ \
  --zone="$ZONE" --tunnel-through-iap --quiet \
  && echo "  ok: demo-stacks -> vigo-srv:/tmp/demo-stacks" \
  || echo "  ! demo-stacks copy failed — server will fall back to empty bundles" >&2
scp_iap "${SCRIPT_DIR}/setup-harness.sh" vigo-test-harness
scp_iap "/tmp/vigo-fakefleet"            vigo-test-harness
scp_iap "/tmp/vigocli"                   vigo-test-harness

SRV_IP=$(g compute instances describe vigo-srv --zone="$ZONE" \
  --format="value(networkInterfaces[0].networkIP)")
EXT_IP=$(g compute instances describe vigo-srv --zone="$ZONE" \
  --format="value(networkInterfaces[0].accessConfigs[0].natIP)" 2>/dev/null || true)

if [ "$RUN_SETUP" -eq 1 ]; then
  echo "== Provisioning vigo-srv (Docker + pull ${VERSION} + run vigosrv) =="
  ssh_iap vigo-srv "sudo bash /tmp/setup-server.sh"
  echo "== Provisioning vigo-test-harness (binaries + OS tuning) =="
  ssh_iap vigo-test-harness "sudo bash /tmp/setup-harness.sh"
fi

cat <<EOF

${C_BOLD}${C_GREEN}== Done ==${C_RST}
${C_MAUVE}Server version:${C_RST}     ${C_YELLOW}${VERSION}${C_RST}
${C_MAUVE}Server internal IP:${C_RST} ${C_YELLOW}${SRV_IP}${C_RST}   (harness dials this on :1530 / :8443)
$([ -n "$EXT_IP" ] && printf '%sWeb UI:%s             %s%s%s   (login printed by setup-server.sh above)' "$C_MAUVE" "$C_RST" "$C_BLUE" "https://${EXT_IP}:8443" "$C_RST")
$([ "$RUN_SETUP" -eq 1 ] && echo "Both VMs provisioned: vigosrv running, harness installed." \
                          || echo "Scripts + binaries copied to /tmp/; run setup yourself (see --no-setup).")

${C_BOLD}Run the test from the harness:${C_RST}
  gcloud compute ssh vigo-test-harness --zone=${ZONE} --tunnel-through-iap
  ulimit -n 1048576
  setsid vigo-fakefleet create --count 60000 --server ${SRV_IP}:1530 --skip-publish \\
    >/tmp/create.log 2>&1 < /dev/null &   # DETACHED — wait for it to finish
  vigo-fakefleet run --stream --server ${SRV_IP}:1530 --cadence 1s --report-cadence 1m
  vigo-fakefleet backout --server ${SRV_IP}:1530

Monitor on the server (never loop docker logs/stats under load):
  gcloud compute ssh vigo-srv --zone=${ZONE} --tunnel-through-iap
  watch -n2 'curl -sk https://127.0.0.1:8443/metrics | grep -E "vigo_streams_active|stream_connect|status=\\"shed\\""'
EOF
