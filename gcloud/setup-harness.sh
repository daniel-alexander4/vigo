#!/usr/bin/env bash
# Set up vigo-test-harness: install the fakefleet + vigocli binaries and tune
# the OS for ~60k outbound held connections to the server.
#
# Normally run by provision-gcp.sh, which builds the binaries on danlap, scp's
# them to /tmp/ here, and invokes this over IAP. To run standalone, ensure
# /tmp/vigo-fakefleet and /tmp/vigocli are present (scp them from danlap), then:
#   sudo bash /tmp/setup-harness.sh
set -euo pipefail

SRC_DIR="${SRC_DIR:-/tmp}"   # where the scp'd binaries landed

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo)" >&2; exit 1; }

echo "== Install binaries =="
for b in vigo-fakefleet vigocli; do
  if [ ! -f "$SRC_DIR/$b" ]; then
    echo "missing $SRC_DIR/$b — build + scp it from danlap (see header)" >&2
    exit 1
  fi
  install -m755 "$SRC_DIR/$b" "/usr/local/bin/$b"
done

echo "== OS tuning for ~60k outbound connections to a single server:port =="
# A single client to one dst ip:port is bounded by source ports. The default
# range (~28k) caps the run well short of 60k; widen to ~64k. NOTE: this is
# also the single-box ceiling — 120k against one port is impossible from one
# source IP (64k-tuple limit); for the 64 GiB follow-on use a second harness.
cat >/etc/sysctl.d/99-vigo-harness.conf <<'EOF'
net.ipv4.ip_local_port_range = 1024 65535
net.core.somaxconn = 65535
net.ipv4.tcp_tw_reuse = 1
fs.file-max = 2097152
EOF
sysctl --system >/dev/null

cat >/etc/security/limits.d/99-vigo-harness.conf <<'EOF'
* soft nofile 1048576
* hard nofile 1048576
EOF

# Persistent state dir: the per-envoy keys (the manifest) MUST survive a reboot,
# else the server's enrolled envoys are orphaned (no keys here to re-stream them).
# /tmp is cleared on reboot — do NOT put the manifest there. World-writable so the
# (non-root) run user can write it.
install -d -m1777 /var/lib/vigo-fakefleet

cat <<'EOF'

== Done ==
Re-ssh so the nofile limit applies, then (SRV_IP = vigo-srv internal IP).
State manifest lives at /var/lib/vigo-fakefleet/state.json (survives reboot):

  ulimit -n 1048576
  STATE=/var/lib/vigo-fakefleet/state.json

  # --server is the gRPC address (host:1530). --skip-publish is REQUIRED off-box:
  # publish writes the server's local /srv/vigo, unreachable from here — envoys
  # enroll over gRPC and converge against the server's published .live.
  #
  # create DETACHED — a force-backgrounded foreground create cancels its enroll
  # context mid-publish and wedges the SQLite writer.
  setsid vigo-fakefleet create --count 60000 --skip-publish --server SRV_IP:1530 \
    --api https://SRV_IP:8443 --state "$STATE" >/var/lib/vigo-fakefleet/create.log 2>&1 < /dev/null &
  tail -f /var/lib/vigo-fakefleet/create.log     # wait for enroll to finish

  # hold the streams:
  vigo-fakefleet run --stream --server SRV_IP:1530 --state "$STATE" --cadence 1s --report-cadence 1m

  # teardown (needs the admin token; deletes lag a few minutes):
  vigo-fakefleet backout --server SRV_IP:1530 --api https://SRV_IP:8443 --state "$STATE" --admin-token "$(token)"

Monitor on the SERVER (never loop docker logs/stats under load):
  watch -n2 'curl -sk https://127.0.0.1:8443/metrics | grep -E "vigo_streams_active|stream_connect|status=\"shed\""'
EOF
