#!/usr/bin/env bash
# Stand up a 2-bolt SPANNER test infra (ADR-026) within the 16-vCPU quota:
#   - vigo-bolt-1   (e2-medium 2/4)  patterns *.a.test
#   - vigo-bolt-2   (e2-medium 2/4)  patterns *.b.test
#   - vigo-harness-1, vigo-harness-2 (e2-small 2/2)  fakefleet drivers
# All --no-address (egress via Cloud NAT). Servers run vigosrv in spanner mode;
# the federation ceremony (init/invite/join) + harness enroll are printed as
# next-steps (token passing needs to be driven). Reuses the surviving vigo-vpc.
#
#   ./gcloud/provision-spanner.sh                 # create + provision
#   ./gcloud/provision-spanner.sh --no-setup      # create + copy only
set -euo pipefail

PROJECT=project-69f2499e-5082-48f0-b19
REGION=us-west1 ZONE=us-west1-a NETWORK=vigo-vpc SUBNET=vigo-subnet
IMAGE_FAMILY=ubuntu-2404-lts-amd64 IMAGE_PROJECT=ubuntu-os-cloud
SPANNER_ID=alexander4-test
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
RUN_SETUP=1
[ "${1:-}" = "--no-setup" ] && RUN_SETUP=0

command -v gcloud >/dev/null || { echo "gcloud not in PATH" >&2; exit 1; }
command -v go     >/dev/null || { echo "go not in PATH" >&2; exit 1; }
g() { gcloud --project="$PROJECT" "$@"; }
scp_iap() { local s=$1 vm=$2 t=0; until g compute scp "$s" "${vm}:/tmp/" --zone="$ZONE" --tunnel-through-iap --quiet 2>/dev/null; do t=$((t+1)); [ "$t" -ge 12 ] && { echo "  ! scp $(basename "$s") -> $vm failed" >&2; return 1; }; echo "  ... $vm not ready (try $t)"; sleep 15; done; echo "  ok: $(basename "$s") -> $vm"; }
ssh_iap() { g compute ssh "$1" --zone="$ZONE" --tunnel-through-iap --quiet --command="$2"; }

VERSION="$(tr -d '[:space:]' < "${REPO_ROOT}/VERSION")"
IMAGE="${REGION}-docker.pkg.dev/${PROJECT}/vigo/vigo:${VERSION}"
echo "== Version ${VERSION} (image ${IMAGE}) =="
g artifacts docker images describe "${IMAGE}" >/dev/null 2>&1 || { echo "  ! ${IMAGE} not in AR — run ./push2AR.sh first" >&2; exit 1; }

echo "== Build harness binaries (incl. --hostname-suffix fakefleet) =="
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o /tmp/vigo-fakefleet ./test/fakefleet )
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o /tmp/vigocli        ./cli )
echo "$VERSION" > /tmp/vigo-version

echo "== Firewall + NAT (idempotent; vigo-vpc survives) =="
g compute firewall-rules describe vigo-allow-internal >/dev/null 2>&1 || \
  g compute firewall-rules create vigo-allow-internal --network="$NETWORK" --direction=INGRESS --action=ALLOW --rules=tcp,udp,icmp --source-ranges=10.17.17.0/24
g compute firewall-rules describe vigo-allow-iap >/dev/null 2>&1 || \
  g compute firewall-rules create vigo-allow-iap --network="$NETWORK" --direction=INGRESS --action=ALLOW --rules=tcp:22 --source-ranges=35.235.240.0/20
g compute routers describe vigo-router --region="$REGION" >/dev/null 2>&1 || \
  g compute routers create vigo-router --network="$NETWORK" --region="$REGION"
g compute routers nats describe vigo-nat --router=vigo-router --region="$REGION" >/dev/null 2>&1 || \
  g compute routers nats create vigo-nat --router=vigo-router --region="$REGION" --auto-allocate-nat-external-ips --nat-all-subnet-ip-ranges

mk() { # name machine-type disk
  g compute instances describe "$1" --zone="$ZONE" >/dev/null 2>&1 && { echo "  $1 exists — skip"; return; }
  echo "  creating $1 ($2)"
  g compute instances create "$1" --zone="$ZONE" --machine-type="$2" \
    --network="$NETWORK" --subnet="$SUBNET" --no-address \
    --image-family="$IMAGE_FAMILY" --image-project="$IMAGE_PROJECT" \
    --boot-disk-size="$3" --boot-disk-type=pd-balanced --quiet >/dev/null &
}
echo "== Create VMs (2 bolts e2-medium, 2 harnesses e2-small) =="
mk vigo-bolt-1 e2-medium 20GB
mk vigo-bolt-2 e2-medium 20GB
mk vigo-harness-1 e2-small 15GB
mk vigo-harness-2 e2-small 15GB
wait
echo "  all VMs present"

echo "== Copy to bolts =="
for b in vigo-bolt-1 vigo-bolt-2; do
  scp_iap "${SCRIPT_DIR}/setup-bolt.sh" "$b"
  scp_iap "/tmp/vigo-version" "$b"
  scp_iap "${SCRIPT_DIR}/loadtest-unlimited.lic" "$b"
  scp_iap "/tmp/vigocli" "$b"
done
echo "== Copy to harnesses =="
for h in vigo-harness-1 vigo-harness-2; do
  scp_iap "${SCRIPT_DIR}/setup-harness.sh" "$h"
  scp_iap "/tmp/vigo-fakefleet" "$h"
  scp_iap "/tmp/vigocli" "$h"
done

if [ "$RUN_SETUP" -eq 1 ]; then
  echo "== Provision bolt-1 (patterns *.a.test) =="
  ssh_iap vigo-bolt-1 "sudo SPANNER_ID=${SPANNER_ID} BOLT_PATTERNS='*.a.test' bash /tmp/setup-bolt.sh"
  echo "== Provision bolt-2 (patterns *.b.test) =="
  ssh_iap vigo-bolt-2 "sudo SPANNER_ID=${SPANNER_ID} BOLT_PATTERNS='*.b.test' bash /tmp/setup-bolt.sh"
  echo "== Provision harnesses =="
  ssh_iap vigo-harness-1 "sudo bash /tmp/setup-harness.sh"
  ssh_iap vigo-harness-2 "sudo bash /tmp/setup-harness.sh"
fi

B1=$(g compute instances describe vigo-bolt-1 --zone="$ZONE" --format='get(networkInterfaces[0].networkIP)')
B2=$(g compute instances describe vigo-bolt-2 --zone="$ZONE" --format='get(networkInterfaces[0].networkIP)')
cat <<EOF

== Done — bolts up in spanner mode (spanner_id=${SPANNER_ID}) ==
  vigo-bolt-1  ${B1}  patterns *.a.test
  vigo-bolt-2  ${B2}  patterns *.b.test
  vigo-harness-1 / vigo-harness-2  (fakefleet installed)

Next — found + join the spanner (token passing, driven):
  # bolt-1: found, then mint an invite
  gcloud compute ssh vigo-bolt-1 --zone=${ZONE} --tunnel-through-iap --command='sudo vigocli spanner init'
  CODE=\$(gcloud compute ssh vigo-bolt-1 --zone=${ZONE} --tunnel-through-iap --command='sudo vigocli spanner bolt invite --ttl 24h' | tail -1)
  # bolt-2: join bolt-1 over the internal subnet
  gcloud compute ssh vigo-bolt-2 --zone=${ZONE} --tunnel-through-iap --command="sudo vigocli spanner bolt join --from https://${B1}:8443 --code \$CODE"
  # verify roster on either bolt
  gcloud compute ssh vigo-bolt-1 --zone=${ZONE} --tunnel-through-iap --command='sudo vigocli spanner status; sudo vigocli spanner bolt list'

Then enroll partitioned envoys (routing test):
  gcloud compute ssh vigo-harness-1 --zone=${ZONE} --tunnel-through-iap --command='ulimit -n 1048576; vigo-fakefleet create --count 40 --named 40 --skip-publish --hostname-suffix .a.test --server ${B1}:1530 --api https://${B1}:8443 --state /var/lib/vigo-fakefleet/a.json'
  gcloud compute ssh vigo-harness-2 --zone=${ZONE} --tunnel-through-iap --command='ulimit -n 1048576; vigo-fakefleet create --count 40 --named 40 --skip-publish --hostname-suffix .b.test --server ${B1}:1530 --api https://${B1}:8443 --state /var/lib/vigo-fakefleet/b.json'
  # both enroll against bolt-1; *.b.test envoys should route to bolt-2. Verify split:
  gcloud compute ssh vigo-bolt-1 --zone=${ZONE} --tunnel-through-iap --command='sudo vigocli spanner bolt list'   # then 'spanner bolt envoys <pubkey>' per bolt

Teardown: gcloud compute instances delete vigo-bolt-1 vigo-bolt-2 vigo-harness-1 vigo-harness-2 --zone=${ZONE}
EOF
