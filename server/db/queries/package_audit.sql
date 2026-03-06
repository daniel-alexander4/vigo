-- name: CreatePackageAudit :one
INSERT INTO package_audit (client_id, package_name, installed_version, desired_version)
VALUES ($1, $2, $3, $4)
RETURNING id, client_id, package_name, installed_version, desired_version, detected_at;

-- name: ListPackageAuditByClient :many
SELECT id, client_id, package_name, installed_version, desired_version, detected_at
FROM package_audit
WHERE client_id = $1
ORDER BY detected_at DESC;

-- name: ListPackageDrift :many
SELECT id, client_id, package_name, installed_version, desired_version, detected_at
FROM package_audit
WHERE installed_version IS DISTINCT FROM desired_version
ORDER BY detected_at DESC;

-- name: ListAllPackageAudit :many
SELECT id, client_id, package_name, installed_version, desired_version, detected_at
FROM package_audit
ORDER BY detected_at DESC
LIMIT $1;
