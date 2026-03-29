-- name: CreateSandgorgonAsset :one
INSERT INTO sandgorgon_assets (hostname, bmc_address, bmc_username, bmc_password, serial_number, manufacturer, model, bios_version, asset_tag, rack_location, lifecycle_state, envoy_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSandgorgonAsset :one
SELECT * FROM sandgorgon_assets WHERE id = ?;

-- name: GetSandgorgonAssetByBMC :one
SELECT * FROM sandgorgon_assets WHERE bmc_address = ?;

-- name: GetSandgorgonAssetByHostname :one
SELECT * FROM sandgorgon_assets WHERE hostname = ? LIMIT 1;

-- name: GetSandgorgonAssetByEnvoyID :one
SELECT * FROM sandgorgon_assets WHERE envoy_id = ? LIMIT 1;

-- name: ListSandgorgonAssets :many
SELECT * FROM sandgorgon_assets ORDER BY hostname ASC;

-- name: ListSandgorgonAssetsByState :many
SELECT * FROM sandgorgon_assets WHERE lifecycle_state = ? ORDER BY hostname ASC;

-- name: UpdateSandgorgonAssetState :exec
UPDATE sandgorgon_assets SET lifecycle_state = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateSandgorgonAssetCommissioned :exec
UPDATE sandgorgon_assets SET lifecycle_state = 'commissioned', commissioned_at = datetime('now'), envoy_id = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateSandgorgonAssetDecommissioned :exec
UPDATE sandgorgon_assets SET lifecycle_state = 'retired', decommissioned_at = datetime('now'), updated_at = datetime('now') WHERE id = ?;

-- name: UpdateSandgorgonAssetHardware :exec
UPDATE sandgorgon_assets SET serial_number = ?, manufacturer = ?, model = ?, bios_version = ?, updated_at = datetime('now') WHERE id = ?;

-- name: UpdateSandgorgonAssetReturnToAvailable :exec
UPDATE sandgorgon_assets SET lifecycle_state = 'available', envoy_id = NULL, decommissioned_at = NULL, updated_at = datetime('now') WHERE id = ?;

-- name: SearchSandgorgonAssets :many
SELECT * FROM sandgorgon_assets WHERE hostname LIKE ?1 OR serial_number LIKE ?1 OR asset_tag LIKE ?1 ORDER BY hostname ASC;

-- name: DeleteSandgorgonAsset :exec
DELETE FROM sandgorgon_assets WHERE id = ?;

-- name: CreateSandgorgonCertificate :one
INSERT INTO sandgorgon_certificates (asset_id, envoy_id, hostname, serial_number, model, asset_tag, sanitization_method, media_type, verification_method, verification_result, verification_limitations, technician, started_at, completed_at, lifecycle_summary)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSandgorgonCertificate :one
SELECT * FROM sandgorgon_certificates WHERE id = ?;

-- name: ListSandgorgonCertificates :many
SELECT * FROM sandgorgon_certificates ORDER BY created_at DESC;

-- name: ListSandgorgonCertificatesByAsset :many
SELECT * FROM sandgorgon_certificates WHERE asset_id = ? ORDER BY created_at DESC;

-- name: CreateSandgorgonEvent :exec
INSERT INTO sandgorgon_events (asset_id, event_type, detail) VALUES (?, ?, ?);

-- name: ListSandgorgonEventsByAsset :many
SELECT * FROM sandgorgon_events WHERE asset_id = ? ORDER BY created_at DESC LIMIT ?;
