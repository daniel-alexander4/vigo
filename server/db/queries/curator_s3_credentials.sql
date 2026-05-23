-- name: CreateS3Credential :exec
INSERT INTO curator_s3_credentials (access_key_id, scope, name)
VALUES (?, ?, ?);

-- name: GetS3Credential :one
SELECT access_key_id, scope, name, created_at
FROM curator_s3_credentials
WHERE access_key_id = ?;

-- name: ListS3Credentials :many
SELECT access_key_id, scope, name, created_at
FROM curator_s3_credentials
ORDER BY created_at DESC;

-- name: DeleteS3Credential :exec
DELETE FROM curator_s3_credentials WHERE access_key_id = ?;
