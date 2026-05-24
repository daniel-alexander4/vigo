-- name: GetUserByID :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: GetUserByUsername :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE username = ? AND deleted_at IS NULL;

-- name: GetUserByOIDCSubject :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE oidc_subject = ? AND deleted_at IS NULL;

-- name: ListUsers :many
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE deleted_at IS NULL ORDER BY username;

-- name: InsertUser :one
INSERT INTO users (username, display_name, email, role, ssh_public_key, oidc_subject, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at, updated_at_ms, origin_bolt, deleted_at, signature;

-- name: UpdateUser :exec
UPDATE users SET
    display_name = ?,
    email = ?,
    role = ?,
    permissions = ?,
    ssh_public_key = ?,
    oidc_subject = ?,
    updated_at = datetime('now'),
    updated_at_ms = ?
WHERE id = ?;

-- name: SoftDeleteUser :exec
UPDATE users SET
    deleted_at = ?,
    updated_at_ms = ?,
    updated_at = datetime('now')
WHERE id = ? AND deleted_at IS NULL;

-- name: CountUsers :one
SELECT COUNT(*) FROM users WHERE deleted_at IS NULL;

-- name: CountAdminUsers :one
SELECT COUNT(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL;

-- name: EnableTOTP :exec
UPDATE users SET totp_enabled = 1, totp_recovery = ?, updated_at = datetime('now'), updated_at_ms = ?
WHERE id = ?;

-- name: DisableTOTP :exec
UPDATE users SET totp_enabled = 0, totp_recovery = '', updated_at = datetime('now'), updated_at_ms = ?
WHERE id = ?;

-- name: UpdateTOTPRecovery :exec
UPDATE users SET totp_recovery = ?, updated_at = datetime('now'), updated_at_ms = ?
WHERE id = ?;

-- name: GetUserSSHPublicKey :one
SELECT ssh_public_key FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: MergeUserLWW :execrows
-- ADR-030 LWW merge: an incoming row wins if its updated_at_ms is
-- strictly newer, or — same-ms tiebreaker — its origin_bolt sorts
-- lex-min against the local row's. Used by Slice 2-iii-d's MergeUsers.
-- All replicatable columns are written; updated_at is set to current
-- wall-clock so the display projection stays meaningful even after a
-- foreign-bolt merge.
INSERT INTO users (
    id, username, display_name, email, role, permissions, ssh_public_key,
    oidc_subject, totp_enabled, totp_recovery, updated_at, updated_at_ms,
    origin_bolt, deleted_at, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    username       = excluded.username,
    display_name   = excluded.display_name,
    email          = excluded.email,
    role           = excluded.role,
    permissions    = excluded.permissions,
    ssh_public_key = excluded.ssh_public_key,
    oidc_subject   = excluded.oidc_subject,
    totp_enabled   = excluded.totp_enabled,
    totp_recovery  = excluded.totp_recovery,
    updated_at     = datetime('now'),
    updated_at_ms  = excluded.updated_at_ms,
    origin_bolt    = excluded.origin_bolt,
    deleted_at     = excluded.deleted_at,
    signature      = excluded.signature
WHERE excluded.updated_at_ms > users.updated_at_ms
   OR (excluded.updated_at_ms = users.updated_at_ms
       AND excluded.origin_bolt < users.origin_bolt);
