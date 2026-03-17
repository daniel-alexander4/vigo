-- name: CreateConfigChange :one
INSERT INTO config_changes (requested_by, expires_at, diff_summary)
VALUES (?, ?, ?)
RETURNING id, status, requested_by, approved_by, requested_at, resolved_at, expires_at, diff_summary, reject_reason;

-- name: GetConfigChange :one
SELECT id, status, requested_by, approved_by, requested_at, resolved_at, expires_at, diff_summary, reject_reason
FROM config_changes
WHERE id = ?;

-- name: ListPendingConfigChanges :many
SELECT id, status, requested_by, approved_by, requested_at, resolved_at, expires_at, diff_summary, reject_reason
FROM config_changes
WHERE status = 'pending'
ORDER BY requested_at DESC;

-- name: ApproveConfigChange :exec
UPDATE config_changes
SET status = 'approved', approved_by = ?, resolved_at = datetime('now')
WHERE id = ? AND status = 'pending';

-- name: RejectConfigChange :exec
UPDATE config_changes
SET status = 'rejected', approved_by = ?, resolved_at = datetime('now'), reject_reason = ?
WHERE id = ? AND status = 'pending';

-- name: ExpirePendingConfigChanges :exec
UPDATE config_changes
SET status = 'expired', resolved_at = datetime('now')
WHERE status = 'pending' AND expires_at <= datetime('now');

-- name: CancelPendingConfigChanges :exec
UPDATE config_changes
SET status = 'expired', resolved_at = datetime('now')
WHERE status = 'pending';
