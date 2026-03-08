-- name: CreatePackageAudit :one
INSERT INTO package_audit (node_id, package_name, installed_version, desired_version)
VALUES ($1, $2, $3, $4)
RETURNING id, node_id, package_name, installed_version, desired_version, detected_at;

-- name: ListPackageAuditByNode :many
SELECT pa.id, pa.node_id, pa.package_name, pa.installed_version, pa.desired_version, pa.detected_at,
       n.hostname
FROM package_audit pa
JOIN nodes n ON n.id = pa.node_id
WHERE pa.node_id = $1
ORDER BY pa.detected_at DESC;

-- name: ListPackageDrift :many
SELECT pa.id, pa.node_id, pa.package_name, pa.installed_version, pa.desired_version, pa.detected_at,
       n.hostname
FROM package_audit pa
JOIN nodes n ON n.id = pa.node_id
WHERE pa.installed_version IS DISTINCT FROM pa.desired_version
ORDER BY pa.detected_at DESC;

-- name: ListAllPackageAudit :many
SELECT pa.id, pa.node_id, pa.package_name, pa.installed_version, pa.desired_version, pa.detected_at,
       n.hostname
FROM package_audit pa
JOIN nodes n ON n.id = pa.node_id
ORDER BY pa.detected_at DESC
LIMIT $1;
