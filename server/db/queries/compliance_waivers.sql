-- name: CreateComplianceWaiver :one
INSERT INTO compliance_waivers (framework, control_id, reason, approver, expires_at)
VALUES (?1, ?2, ?3, ?4, ?5)
RETURNING id, framework, control_id, reason, approver, created_at, expires_at, active;

-- name: ListComplianceWaivers :many
SELECT id, framework, control_id, reason, approver, created_at, expires_at, active
FROM compliance_waivers
WHERE active = 1
ORDER BY framework, control_id;

-- name: ListComplianceWaiversByFramework :many
SELECT id, framework, control_id, reason, approver, created_at, expires_at, active
FROM compliance_waivers
WHERE framework = ?1 AND active = 1
ORDER BY control_id;

-- name: DeactivateComplianceWaiver :exec
UPDATE compliance_waivers SET active = 0 WHERE id = ?1;

-- name: ExpireComplianceWaivers :exec
UPDATE compliance_waivers SET active = 0
WHERE active = 1 AND expires_at IS NOT NULL AND expires_at < datetime('now');
