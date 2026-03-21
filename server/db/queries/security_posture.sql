-- name: InsertSecurityPostureSnapshot :exec
INSERT OR REPLACE INTO security_posture_history (
    id, snapshot_date, cve_critical, cve_high, cve_medium, cve_low,
    avg_hardening, integrity_incidents, rootkit_warnings, malware_detections,
    scanned_hosts, total_hosts
) VALUES (
    lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)),2) || '-' || substr('89ab', abs(random()) % 4 + 1, 1) || substr(hex(randomblob(2)),2) || '-' || hex(randomblob(6))),
    date('now'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
);

-- name: ListSecurityPostureHistory :many
SELECT snapshot_date, cve_critical, cve_high, cve_medium, cve_low,
       avg_hardening, integrity_incidents, rootkit_warnings, malware_detections,
       scanned_hosts, total_hosts
FROM security_posture_history
ORDER BY snapshot_date DESC
LIMIT ?;
