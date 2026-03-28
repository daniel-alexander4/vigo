
# Connwaer

Connwaer is an active compliance verification agent. It runs alongside the Vigo agent on managed nodes, performing 18 scheduled verification capabilities that prove compliance controls are working -- not just configured.

## Getting Started

- [Overview](overview.md) -- what Connwaer is and how it works
- [Getting Started](getting-started.md) -- installation and first run
- [Configuration](configuration.md) -- full YAML config reference
- [CLI Reference](cli-reference.md) -- commands and flags

## Capabilities

- [Capability Index](capabilities/_index.md) -- all 18 capabilities at a glance
- [App Allowlisting](capabilities/app-allowlisting.md) -- fapolicyd / AppLocker verification
- [Sandboxing](capabilities/sandboxing.md) -- SELinux, AppArmor, Credential Guard
- [Log Review](capabilities/log-review.md) -- auditd / Event Log anomaly detection
- [Capacity Monitor](capabilities/capacity-monitor.md) -- disk, memory, CPU benchmarks with trend prediction
- [Backup Restore](capabilities/backup-restore.md) -- RTO measurement and verification
- [BCP Test](capabilities/bcp-test.md) -- multi-step business continuity plan testing
- [WORM Verify](capabilities/worm-verify.md) -- immutable storage verification
- [Kill Switch](capabilities/kill-switch.md) -- emergency shutdown readiness
- [RNG Validation](capabilities/rng-validation.md) -- NIST SP 800-22 statistical test suite
- [HSM Lifecycle](capabilities/hsm-lifecycle.md) -- PKCS#11 key attribute auditing
- [SNMP Collector](capabilities/snmp-collector.md) -- SNMPv2c device polling
- [Encryption at Rest](capabilities/encryption-at-rest.md) -- LUKS / BitLocker verification
- [Password Policy](capabilities/password-policy.md) -- policy enforcement testing
- [Network Segmentation](capabilities/network-segmentation.md) -- TCP reachability tests
- [Privilege Escalation](capabilities/privilege-escalation.md) -- SUID/capability scanning
- [Time Sync](capabilities/time-sync.md) -- NTP offset measurement
- [Integrity Monitoring](capabilities/integrity-monitoring.md) -- AIDE / OSSEC / Wazuh detection
- [Session Timeout](capabilities/session-timeout.md) -- SSH / RDP idle timeout validation

## Operations

- [Scheduling](scheduling.md) -- cron expressions, intervals, dependency ordering
- [Alerting](alerting.md) -- webhooks, syslog, command alerts
- [Trends](trends.md) -- historical metrics and exhaustion prediction
- [Troubleshooting](troubleshooting.md) -- common issues and debugging

## Reference

- [Integration](integration.md) -- Vigo module deployment, webhook/SIEM integration
- [Licensing](licensing.md) -- license format and evaluation mode
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../legal/documentation-license.md).*
