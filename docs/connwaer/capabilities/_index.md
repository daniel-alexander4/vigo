
# Capabilities

Connwaer includes 18 verification capabilities. Each runs on a configurable schedule, produces structured JSON evidence, and maps to one or more compliance framework controls.

## Capability Index

| Name | Machine Name | Controls | Platforms | Timeout |
|------|-------------|----------|-----------|---------|
| [App Allowlisting](app-allowlisting.md) | `app_allowlisting` | CE-MW-4 | Linux, Windows | 5m |
| [Sandboxing](sandboxing.md) | `sandboxing` | CE-MW-5 | Linux, Windows | 5m |
| [Log Review](log-review.md) | `log_review` | PCI-10.4.1 | Linux, Windows | 5m |
| [Capacity Monitor](capacity-monitor.md) | `capacity_monitor` | RTS7-Art11-1, RTS7-Art11-2 | All | 5m |
| [Backup Restore](backup-restore.md) | `backup_restore` | FINRA-BCP-1, FINRA-BCP-2 | All | 1h |
| [BCP Test](bcp-test.md) | `bcp_test` | FINRA-BCP-3, MGA-RGR-5.2, UKGC-RTS-5B | All | 2h |
| [WORM Verify](worm-verify.md) | `worm_verify` | SEC-17a4-1, SEC-17a4-2 | All | 5m |
| [Kill Switch](kill-switch.md) | `kill_switch` | RTS7-Art14-1 | All | 5m |
| [RNG Validation](rng-validation.md) | `rng_validation` | NGC-TS-4.2, MGA-RGR-6.1 | All | 10m |
| [HSM Lifecycle](hsm-lifecycle.md) | `hsm_lifecycle` | CCSS-9.1 | All | 5m |
| [SNMP Collector](snmp-collector.md) | `snmp_collector` | (monitoring) | All | 5m |
| [Encryption at Rest](encryption-at-rest.md) | `encryption_at_rest` | HIPAA-164.312(a)(2)(iv), PCI-DSS-3.4 | Linux, Windows | 5m |
| [Password Policy](password-policy.md) | `password_policy` | NIST-800-63B, PCI-DSS-8.3 | Linux, Windows | 5m |
| [Network Segmentation](network-segmentation.md) | `network_segmentation` | PCI-DSS-1.3, NIST-AC-4 | All | 5m |
| [Privilege Escalation](privilege-escalation.md) | `privilege_escalation` | NIST-AC-6, CIS-6.1 | Linux, Windows | 10m |
| [Time Sync](time-sync.md) | `time_sync` | PCI-DSS-10.4, NIST-AU-8 | Linux, Windows | 5m |
| [Integrity Monitoring](integrity-monitoring.md) | `integrity_monitoring` | PCI-DSS-11.5, NIST-SI-7 | Linux, Windows | 10m |
| [Session Timeout](session-timeout.md) | `session_timeout` | HIPAA-164.312(a)(2)(iii), PCI-DSS-8.1.8 | Linux, Windows | 5m |
---
*Confidential -- Alexander4, LLC. Not for redistribution. See [documentation-license](../../legal/documentation-license.md).*
