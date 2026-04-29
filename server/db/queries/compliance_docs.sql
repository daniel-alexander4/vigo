-- name: CreateComplianceDoc :one
INSERT INTO compliance_docs (id, doc_type, filename, mime_type, size_bytes, sha256, uploaded_by)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)
RETURNING id, doc_type, filename, mime_type, size_bytes, sha256, uploaded_by, uploaded_at, replaced_at, replaced_by_id, deleted_at, deleted_by;

-- name: GetComplianceDoc :one
SELECT id, doc_type, filename, mime_type, size_bytes, sha256, uploaded_by, uploaded_at, replaced_at, replaced_by_id, deleted_at, deleted_by
FROM compliance_docs
WHERE id = ?1;

-- name: ListComplianceDocs :many
SELECT id, doc_type, filename, mime_type, size_bytes, sha256, uploaded_by, uploaded_at, replaced_at, replaced_by_id, deleted_at, deleted_by
FROM compliance_docs
WHERE deleted_at IS NULL
ORDER BY uploaded_at DESC;

-- name: ListComplianceDocsByDocType :many
SELECT id, doc_type, filename, mime_type, size_bytes, sha256, uploaded_by, uploaded_at, replaced_at, replaced_by_id, deleted_at, deleted_by
FROM compliance_docs
WHERE deleted_at IS NULL AND doc_type = ?1
ORDER BY uploaded_at DESC;

-- name: ListComplianceDocsByControl :many
SELECT d.id, d.doc_type, d.filename, d.mime_type, d.size_bytes, d.sha256, d.uploaded_by, d.uploaded_at, d.replaced_at, d.replaced_by_id, d.deleted_at, d.deleted_by
FROM compliance_docs d
INNER JOIN compliance_doc_links l ON l.doc_id = d.id
WHERE d.deleted_at IS NULL AND l.framework = ?1 AND l.control_id = ?2
ORDER BY d.uploaded_at DESC;

-- name: HasActiveDocForControl :one
SELECT EXISTS(
    SELECT 1
    FROM compliance_docs d
    INNER JOIN compliance_doc_links l ON l.doc_id = d.id
    WHERE d.deleted_at IS NULL AND l.framework = ?1 AND l.control_id = ?2
) AS present;

-- name: CountActiveDocsForControl :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM compliance_docs d
INNER JOIN compliance_doc_links l ON l.doc_id = d.id
WHERE d.deleted_at IS NULL AND l.framework = ?1 AND l.control_id = ?2;

-- name: CountActiveDocsByControlForFramework :many
SELECT l.control_id, CAST(COUNT(*) AS INTEGER) AS total
FROM compliance_docs d
INNER JOIN compliance_doc_links l ON l.doc_id = d.id
WHERE d.deleted_at IS NULL AND l.framework = ?1
GROUP BY l.control_id;

-- name: SumComplianceDocsBytes :one
SELECT CAST(COALESCE(SUM(size_bytes), 0) AS INTEGER) AS total
FROM compliance_docs
WHERE deleted_at IS NULL;

-- name: SumComplianceDocsBytesByFramework :one
SELECT CAST(COALESCE(SUM(d.size_bytes), 0) AS INTEGER) AS total
FROM compliance_docs d
INNER JOIN compliance_doc_links l ON l.doc_id = d.id
WHERE d.deleted_at IS NULL AND l.framework = ?1;

-- name: MarkComplianceDocReplaced :exec
UPDATE compliance_docs
SET replaced_at = CURRENT_TIMESTAMP, replaced_by_id = ?2
WHERE id = ?1;

-- name: SoftDeleteComplianceDoc :exec
UPDATE compliance_docs
SET deleted_at = CURRENT_TIMESTAMP, deleted_by = ?2
WHERE id = ?1 AND deleted_at IS NULL;

-- name: PurgeComplianceDoc :exec
DELETE FROM compliance_docs WHERE id = ?1;

-- name: AddComplianceDocLink :exec
INSERT OR IGNORE INTO compliance_doc_links (doc_id, framework, control_id, linked_by)
VALUES (?1, ?2, ?3, ?4);

-- name: RemoveComplianceDocLink :exec
DELETE FROM compliance_doc_links
WHERE doc_id = ?1 AND framework = ?2 AND control_id = ?3;

-- name: ListComplianceDocLinks :many
SELECT doc_id, framework, control_id, linked_at, linked_by
FROM compliance_doc_links
WHERE doc_id = ?1
ORDER BY framework, control_id;
