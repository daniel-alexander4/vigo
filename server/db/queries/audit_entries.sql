-- name: GetLastAuditEntry :one
SELECT id, prev_hash, hash, timestamp, event_type, actor, summary, payload
FROM audit_entries
ORDER BY id DESC
LIMIT 1;

-- name: InsertAuditEntry :one
INSERT INTO audit_entries (prev_hash, hash, timestamp, event_type, actor, summary, payload)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, prev_hash, hash, timestamp, event_type, actor, summary, payload;

-- name: ListAuditEntries :many
SELECT id, prev_hash, hash, timestamp, event_type, actor, summary, payload
FROM audit_entries
ORDER BY id DESC
LIMIT ?;

-- name: ListAuditEntriesAsc :many
SELECT id, prev_hash, hash, timestamp, event_type, actor, summary, payload
FROM audit_entries
ORDER BY id ASC;

-- name: CountAuditEntries :one
SELECT count(*) FROM audit_entries;

-- name: GetAuditEntry :one
SELECT id, prev_hash, hash, timestamp, event_type, actor, summary, payload
FROM audit_entries
WHERE id = ?;
