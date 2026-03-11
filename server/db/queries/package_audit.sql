-- name: CreatePackageAudit :one
INSERT INTO package_audit (thrall_id, package_name, installed_version, desired_version)
VALUES (?, ?, ?, ?)
RETURNING id, thrall_id, package_name, installed_version, desired_version, detected_at;

-- name: ListPackageAuditByThrall :many
SELECT pa.id, pa.thrall_id, pa.package_name, pa.installed_version, pa.desired_version, pa.detected_at,
       t.hostname
FROM package_audit pa
JOIN thralls t ON t.id = pa.thrall_id
WHERE pa.thrall_id = ?
ORDER BY pa.detected_at DESC;

-- name: ListPackageDrift :many
SELECT pa.id, pa.thrall_id, pa.package_name, pa.installed_version, pa.desired_version, pa.detected_at,
       t.hostname
FROM package_audit pa
JOIN thralls t ON t.id = pa.thrall_id
WHERE (pa.installed_version != pa.desired_version OR (pa.installed_version IS NULL AND pa.desired_version IS NOT NULL) OR (pa.installed_version IS NOT NULL AND pa.desired_version IS NULL))
ORDER BY pa.detected_at DESC;

-- name: ListAllPackageAudit :many
SELECT pa.id, pa.thrall_id, pa.package_name, pa.installed_version, pa.desired_version, pa.detected_at,
       t.hostname
FROM package_audit pa
JOIN thralls t ON t.id = pa.thrall_id
ORDER BY pa.detected_at DESC
LIMIT ?;
