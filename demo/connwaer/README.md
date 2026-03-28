# Connwaer Demo Environment

A Docker Compose environment demonstrating Connwaer's compliance verification capabilities.

## Architecture

```
+------------------+     +------------------+     +------------------+
| connwaer-node-1  |     | connwaer-node-2  |     |   vigo server    |
| (8 capabilities) |     | (4 capabilities) |     |   (UI: :8443)    |
+--------+---------+     +--------+---------+     +------------------+
         |                         |
         +-------+--------+-------+
                 |        |
         +-------+--+ +---+--------+ +----------------+
         | ntp-server| | snmp-target| | worm-simulator |
         +-----------+ +------------+ +----------------+
```

**Node 1** runs 8 capabilities: time_sync, capacity_monitor, worm_verify, snmp_collector, network_segmentation, session_timeout, password_policy, privilege_escalation.

**Node 2** runs 4 capabilities: time_sync, capacity_monitor, session_timeout, snmp_collector.

## Quick Start

```bash
cd demo/connwaer
docker compose up --build
```

## Observe Results

Check capability status on node 1:

```bash
docker exec connwaer-demo-node-1 connwaer status
```

Run a single capability interactively:

```bash
docker exec connwaer-demo-node-1 connwaer verify time_sync
```

View a result file:

```bash
docker exec connwaer-demo-node-1 cat /var/lib/connwaer/results/time_sync.json
```

Query the trends database:

```bash
docker exec connwaer-demo-node-1 sqlite3 /var/lib/connwaer/trends.db \
  "SELECT capability, status, timestamp FROM runs ORDER BY timestamp DESC LIMIT 20"
```

## Capability Notes

Some capabilities produce meaningful results in containers, others are limited:

| Capability | Demo Behavior |
|------------|---------------|
| time_sync | Measures offset against the demo NTP server |
| capacity_monitor | Reports container disk and memory usage with benchmarks |
| worm_verify | Tests write to read-only mounted WORM volume (should pass) |
| snmp_collector | Polls the demo SNMP target for sysDescr, sysUpTime, sysName |
| network_segmentation | Verifies SNMP port 161 is reachable, SSH port 22 is not |
| session_timeout | Reads SSH config (may return warn in container -- no sshd) |
| password_policy | Reads PAM config (may return warn in container -- minimal PAM) |
| privilege_escalation | Scans for SUID binaries and capabilities |

Capabilities not in the demo (require real infrastructure): backup_restore, bcp_test, hsm_lifecycle, rng_validation, kill_switch, encryption_at_rest, app_allowlisting, sandboxing, log_review, integrity_monitoring.

## Cleanup

```bash
docker compose down -v
```
