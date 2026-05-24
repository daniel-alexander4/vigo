-- name: GetUserByID :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: GetUserByUsername :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE username = ? AND deleted_at IS NULL;

-- name: GetUserByOIDCSubject :one
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE oidc_subject = ? AND deleted_at IS NULL;

-- name: ListUsers :many
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE deleted_at IS NULL ORDER BY username;

-- name: ListUsersForReplication :many
-- ADR-030 Slice 2-iii-c: snapshot endpoint source. Returns every
-- row (live AND soft-deleted) so peers see tombstones too, filtered
-- to signed rows only -- legacy unsigned rows (origin_bolt='',
-- signature NULL) don't replicate until Slice 2-iii-f's first-startup
-- re-signing pass marks them eligible.
SELECT id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at_ms, origin_bolt, deleted_at, signature
FROM users WHERE signature IS NOT NULL ORDER BY id;

-- name: InsertUser :one
INSERT INTO users (username, display_name, email, role, ssh_public_key, oidc_subject, updated_at_ms, origin_bolt, signature)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, username, display_name, email, role, permissions, ssh_public_key, oidc_subject, totp_enabled, totp_recovery, created_at, updated_at_ms, origin_bolt, deleted_at, signature;

-- name: UpdateUser :exec
UPDATE users SET
    display_name = ?,
    email = ?,
    role = ?,
    permissions = ?,
    ssh_public_key = ?,
    oidc_subject = ?,
    updated_at_ms = ?,
    origin_bolt = ?,
    signature = ?
WHERE id = ?;

-- name: SoftDeleteUser :exec
UPDATE users SET
    deleted_at = ?,
    updated_at_ms = ?,
    origin_bolt = ?,
    signature = ?
WHERE id = ? AND deleted_at IS NULL;

-- name: CountUsers :one
SELECT COUNT(*) FROM users WHERE deleted_at IS NULL;

-- name: CountAdminUsers :one
SELECT COUNT(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL;

-- name: EnableTOTP :exec
-- ADR-030 Slice 2-iii-c: TOTP fields don't replicate (direction call
-- A -- bolt-local enrollment). DOES NOT touch updated_at_ms or
-- origin_bolt/signature -- a TOTP change must not trigger peer
-- replication of a row whose replicated content hasn't changed.
UPDATE users SET totp_enabled = 1, totp_recovery = ?
WHERE id = ?;

-- name: DisableTOTP :exec
-- See EnableTOTP -- TOTP is bolt-local; no CRDT comparator bump.
UPDATE users SET totp_enabled = 0, totp_recovery = ''
WHERE id = ?;

-- name: UpdateTOTPRecovery :exec
-- See EnableTOTP -- TOTP is bolt-local; no CRDT comparator bump.
UPDATE users SET totp_recovery = ?
WHERE id = ?;

-- name: GetUserSSHPublicKey :one
SELECT ssh_public_key FROM users WHERE id = ? AND deleted_at IS NULL;

-- name: MergeUserLWW :execrows
-- ADR-030 LWW merge: incoming row wins if its updated_at_ms is
-- strictly newer, or -- same-ms tiebreaker -- its origin_bolt sorts
-- lex-min against the local row's. Used by Slice 2-iii-d's MergeUsers.
--
-- TOTP fields (totp_enabled, totp_recovery) are deliberately absent
-- from BOTH the INSERT column list and the UPDATE SET -- TOTP is
-- bolt-local per direction call A. On INSERT, the column defaults
-- (0, '') apply; on UPDATE, SQLite leaves the existing values
-- untouched. A merge can NEVER overwrite a peer's local TOTP state.
INSERT INTO users (
    id, username, display_name, email, role, permissions, ssh_public_key,
    oidc_subject, updated_at_ms,
    origin_bolt, deleted_at, signature
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    username       = excluded.username,
    display_name   = excluded.display_name,
    email          = excluded.email,
    role           = excluded.role,
    permissions    = excluded.permissions,
    ssh_public_key = excluded.ssh_public_key,
    oidc_subject   = excluded.oidc_subject,
    updated_at_ms  = excluded.updated_at_ms,
    origin_bolt    = excluded.origin_bolt,
    deleted_at     = excluded.deleted_at,
    signature      = excluded.signature
WHERE excluded.updated_at_ms > users.updated_at_ms
   OR (excluded.updated_at_ms = users.updated_at_ms
       AND excluded.origin_bolt < users.origin_bolt);

-- name: CountUnsignedUsers :one
-- ADR-030 Slice 2-iii-f: count of live user rows that have not yet
-- been signed by a bolt identity. Used by the doctor check that
-- surfaces pending first-replication; non-zero only briefly after
-- a spanner-mode upgrade (the startup re-sign pass clears them).
SELECT COUNT(*) FROM users WHERE signature IS NULL AND deleted_at IS NULL;

-- name: ResignUserLegacy :exec
-- ADR-030 Slice 2-iii-f: stamps origin_bolt + signature on a legacy
-- unsigned row WITHOUT touching updated_at_ms. The flat
-- updated_at_ms (still 0 from migration 000013's default) means
-- concurrent re-signs across bolts tie on the LWW comparator and
-- the lex-min-origin tiebreaker picks a deterministic winner --
-- exactly what ADR-030's "lex-min-pubkey wins on first-merge
-- collision" promise needs. The signature IS NULL guard prevents
-- accidental re-stamp of an already-signed row.
UPDATE users SET origin_bolt = ?, signature = ?
WHERE id = ? AND signature IS NULL;
