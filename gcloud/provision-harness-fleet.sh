#!/usr/bin/env bash
# Stand up + provision N harness VMs for the MULTI-BOX load test — the rig that
# drives 250k+ envoys past the ~64k connections a single harness box can hold
# (one source IP → one server :1530 is capped by the ephemeral-port 4-tuple).
#
# Each VM runs the SAME create'd manifest with a different `--slice i/N` (added
# to fakefleet for exactly this), so the N VMs tile the fleet disjointly and
# together drive every envoy once. This script only CREATES + PROVISIONS the
# VMs (binaries + OS tuning); the fleet `create` and the per-slice `run` are the
# measurement steps you drive afterward — printed as next-steps and specified in
# docs/internal.docs/loadtest-per-cadence-runbook.md.
#
# Pairs with provision-gcp.sh (which stands up the vigo-srv server VM + the VPC
# firewall/NAT). Run that first (or at least once) so the server + network exist;
# this script re-ensures the firewall/NAT idempotently but does NOT create the
# server. For 250k you want the server resized to ≥128 GiB — see the runbook.
#
#   ./gcloud/provision-harness-fleet.sh                  # 5 VMs, e2-custom-8-32768, + remote setup
#   ./gcloud/provision-harness-fleet.sh 8                # 8 VMs
#   ./gcloud/provision-harness-fleet.sh 5 --machine-type e2-custom-8-49152
#   ./gcloud/provision-harness-fleet.sh --no-setup       # create + copy only
#
# Tear down:
#   gcloud compute instances delete $(seq -f 'vigo-harness-%g' 1 5) --zone=us-west1-a
set -euo pipefail

PROJECT=project-69f2499e-5082-48f0-b19
REGION=us-west1
ZONE=us-west1-a
NETWORK=vigo-vpc
SUBNET=vigo-subnet
IMAGE_FAMILY=ubuntu-2404-lts-amd64
IMAGE_PROJECT=ubuntu-os-cloud

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

COUNT=5
MACHINE=e2-custom-8-32768   # 8 vCPU / 32 GiB — comfortable for ~50k conns/VM
DISK=30GB
RUN_SETUP=1

while [ $# -gt 0 ]; do
  case "$1" in
    --machine-type) MACHINE="$2"; shift 2 ;;
    --disk)         DISK="$2"; shift 2 ;;
    --no-setup)     RUN_SETUP=0; shift ;;
    [0-9]*)         COUNT="$1"; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
[ "$COUNT" -ge 1 ] 2>/dev/null || { echo "VM count must be a positive integer (got '$COUNT')" >&2; exit 2; }

command -v gcloud >/dev/null || { echo "gcloud not in PATH" >&2; exit 1; }
command -v go     >/dev/null || { echo "go not in PATH (needed to build the harness binaries)" >&2; exit 1; }

if [ -z "${NO_COLOR:-}" ]; then
  C_GREEN=$'\033[38;2;166;227;161m' C_BLUE=$'\033[38;2;137;180;250m'
  C_YELLOW=$'\033[38;2;249;226;175m' C_TEAL=$'\033[38;2;148;226;213m'
  C_BOLD=$'\033[1m' C_RST=$'\033[0m'
else
  C_GREEN='' C_BLUE='' C_YELLOW='' C_TEAL='' C_BOLD='' C_RST=''
fi

g() { gcloud --project="$PROJECT" "$@"; }

# scp a file to a VM over IAP, retrying while the freshly-booted VM's IAP SSH
# comes up (sshd + IAP tunnel can take ~30-60s after instance create).
scp_iap() {
  local src="$1" vm="$2" tries=0
  until g compute scp "$src" "${vm}:/tmp/" --zone="$ZONE" --tunnel-through-iap --quiet 2>/dev/null; do
    tries=$((tries + 1))
    if [ "$tries" -ge 12 ]; then
      echo "  ! scp $(basename "$src") -> ${vm} failed after ${tries} tries (IAP SSH not ready?)" >&2
      return 1
    fi
    echo "  ... ${vm} not ready for scp yet (try ${tries}); waiting 15s"
    sleep 15
  done
}
ssh_iap() { g compute ssh "$1" --zone="$ZONE" --tunnel-through-iap --quiet --command="$2"; }

vm_name() { printf 'vigo-harness-%g' "$1"; }

# --- Build harness binaries (pure-Go static) ---------------------------------
echo "${C_BOLD}== Building harness binaries ==${C_RST}"
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o /tmp/vigo-fakefleet ./test/fakefleet )
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o /tmp/vigocli        ./cli )

# --- Firewall + NAT (idempotent; normally already set by provision-gcp.sh) ---
echo "${C_BOLD}== Firewall + Cloud NAT (idempotent) ==${C_RST}"
g compute firewall-rules describe vigo-allow-internal >/dev/null 2>&1 || \
  g compute firewall-rules create vigo-allow-internal --network="$NETWORK" \
    --direction=INGRESS --action=ALLOW --rules=tcp,udp,icmp --source-ranges=10.17.17.0/24
g compute firewall-rules describe vigo-allow-iap >/dev/null 2>&1 || \
  g compute firewall-rules create vigo-allow-iap --network="$NETWORK" \
    --direction=INGRESS --action=ALLOW --rules=tcp:22 --source-ranges=35.235.240.0/20
g compute routers describe vigo-router --region="$REGION" >/dev/null 2>&1 || \
  g compute routers create vigo-router --network="$NETWORK" --region="$REGION"
g compute routers nats describe vigo-nat --router=vigo-router --region="$REGION" >/dev/null 2>&1 || \
  g compute routers nats create vigo-nat --router=vigo-router --region="$REGION" \
    --auto-allocate-nat-external-ips --nat-all-subnet-ip-ranges

# --- Create the N harness VMs (in parallel) ----------------------------------
echo "${C_BOLD}== Creating ${COUNT} harness VMs (${MACHINE}, --no-address) ==${C_RST}"
pids=()
for i in $(seq 1 "$COUNT"); do
  vm="$(vm_name "$i")"
  if g compute instances describe "$vm" --zone="$ZONE" >/dev/null 2>&1; then
    echo "  ${C_TEAL}${vm}${C_RST} already exists — skipping create"
    continue
  fi
  echo "  creating ${C_TEAL}${vm}${C_RST}"
  g compute instances create "$vm" --zone="$ZONE" \
    --machine-type="$MACHINE" \
    --network="$NETWORK" --subnet="$SUBNET" --no-address \
    --image-family="$IMAGE_FAMILY" --image-project="$IMAGE_PROJECT" \
    --boot-disk-size="$DISK" --boot-disk-type=pd-balanced --quiet >/dev/null &
  pids+=($!)
done
for p in "${pids[@]:-}"; do [ -n "$p" ] && wait "$p"; done
echo "  ${C_GREEN}all VMs present${C_RST}"

# --- Provision each VM: copy binaries + setup-harness.sh, then run it ---------
echo "${C_BOLD}== Copying binaries + provisioning each VM (via IAP) ==${C_RST}"
for i in $(seq 1 "$COUNT"); do
  vm="$(vm_name "$i")"
  echo "  ${C_BLUE}${vm}${C_RST}: copying"
  scp_iap "${SCRIPT_DIR}/setup-harness.sh" "$vm"
  scp_iap "/tmp/vigo-fakefleet"            "$vm"
  scp_iap "/tmp/vigocli"                   "$vm"
  if [ "$RUN_SETUP" -eq 1 ]; then
    echo "  ${C_BLUE}${vm}${C_RST}: running setup-harness.sh"
    ssh_iap "$vm" "sudo bash /tmp/setup-harness.sh" >/dev/null
  fi
done

SRV_IP=$(g compute instances describe vigo-srv --zone="$ZONE" \
  --format='get(networkInterfaces[0].networkIP)' 2>/dev/null || true)

cat <<EOF

${C_GREEN}${C_BOLD}== ${COUNT} harness VMs provisioned ==${C_RST}
VMs: $(seq -f "$(vm_name '%g')" 1 "$COUNT" | tr '\n' ' ')
Server internal IP: ${C_TEAL}${SRV_IP:-<run provision-gcp.sh first>}${C_RST}

${C_BOLD}Next — drive the multi-box fleet (full recipe in docs/internal.docs/loadtest-per-cadence-runbook.md):${C_RST}

1. ${C_YELLOW}create ONCE${C_RST} on harness-1 (enrolls every envoy + writes the shared manifest):
   gcloud compute ssh $(vm_name 1) --zone=${ZONE} --tunnel-through-iap
   STATE=/var/lib/vigo-fakefleet/state.json
   setsid vigo-fakefleet create --count 250000 --skip-publish \\
     --server ${SRV_IP:-SRV_IP}:1530 --api https://${SRV_IP:-SRV_IP}:8443 \\
     --state "\$STATE" >/var/lib/vigo-fakefleet/create.log 2>&1 </dev/null &
   tail -f /var/lib/vigo-fakefleet/create.log   # wait for enroll to finish

2. ${C_YELLOW}distribute the manifest${C_RST} to harness-2..${COUNT} (from your laptop, VM→local→VM):
   gcloud compute scp $(vm_name 1):/var/lib/vigo-fakefleet/state.json /tmp/state.json --zone=${ZONE} --tunnel-through-iap
   for i in \$(seq 2 ${COUNT}); do
     gcloud compute scp /tmp/state.json vigo-harness-\${i}:/var/lib/vigo-fakefleet/state.json --zone=${ZONE} --tunnel-through-iap
   done

3. ${C_YELLOW}run with a disjoint --slice on each VM${C_RST} (i/N), same cadence everywhere:
   # on vigo-harness-i:
   ulimit -n 1048576
   vigo-fakefleet run --slice i/${COUNT} --server ${SRV_IP:-SRV_IP}:1530 \\
     --state /var/lib/vigo-fakefleet/state.json --cadence 5m --report-cadence 10m

Monitor on the SERVER (never loop docker logs/stats under load):
  watch -n2 'curl -sk https://127.0.0.1:8443/metrics | grep -E "vigo_checkins_total|vigo_streams_active"'
  cat /proc/\$(pgrep -f vigosrv)/status | grep VmRSS
EOF
