-- name: GetUserByID :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at
FROM users WHERE id = ?;

-- name: GetUserByUsername :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at
FROM users WHERE username = ?;

-- name: GetUserByOIDCSubject :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at
FROM users WHERE oidc_subject = ?;

-- name: ListUsers :many
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at
FROM users ORDER BY username;

-- name: InsertUser :one
INSERT INTO users (username, display_name, email, role, ssh_public_key, oidc_subject)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at;

-- name: UpdateUser :exec
UPDATE users SET
    display_name = ?,
    email = ?,
    role = ?,
    permissions = ?,
    ssh_public_key = ?,
    oidc_subject = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteUser :exec
DELETE FROM users WHERE id = ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: CountAdminUsers :one
SELECT COUNT(*) FROM users WHERE role = 'admin';

-- name: EnableTOTP :exec
UPDATE users SET totp_enabled = 1, totp_recovery = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: DisableTOTP :exec
UPDATE users SET totp_enabled = 0, totp_recovery = '', updated_at = datetime('now')
WHERE id = ?;

-- name: UpdateTOTPRecovery :exec
UPDATE users SET totp_recovery = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: SetUserSSHPublicKey :exec
UPDATE users SET ssh_public_key = ?, updated_at = datetime('now')
WHERE id = ?;
