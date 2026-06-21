#!/usr/bin/env bash
# Stand up the GCP held-stream load-test environment: two VMs (vigo-srv +
# vigo-test-harness) on the existing vigo-vpc/vigo-subnet, plus a Cloud NAT so
# the no-external-IP VMs can reach apt/Docker. Artifact Registry is reachable
# via Private Google Access regardless.
#
# Run from a host with gcloud authed (e.g. danlap). Idempotent: re-running
# skips resources that already exist.
#
#   ./provision-gcp.sh
#
# Tear down after the test:
#   gcloud compute instances delete vigo-srv vigo-test-harness --zone=us-west1-a
set -euo pipefail

PROJECT=project-69f2499e-5082-48f0-b19
REGION=us-west1
ZONE=us-west1-a
NETWORK=vigo-vpc
SUBNET=vigo-subnet
IMAGE_FAMILY=ubuntu-2404-lts-amd64
IMAGE_PROJECT=ubuntu-os-cloud

g() { gcloud --project="$PROJECT" "$@"; }

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

SRV_IP=$(g compute instances describe vigo-srv --zone="$ZONE" \
  --format="value(networkInterfaces[0].networkIP)")

cat <<EOF

== Done ==
Server internal IP: ${SRV_IP}   (harness dials this on :1530 / :8443)

SSH (via IAP — no external IPs):
  gcloud compute ssh vigo-srv          --zone=${ZONE} --tunnel-through-iap
  gcloud compute ssh vigo-test-harness --zone=${ZONE} --tunnel-through-iap

Next:
  1. scp setup-server.sh to vigo-srv, run with sudo (set IMAGE=...:<current-tag>).
  2. Build + scp the harness binaries and setup-harness.sh to vigo-test-harness.
EOF
