-- Operator-uploaded documentation that satisfies framework controls
-- with scope=documented (BAA, IR plan, risk assessment, training records,
-- signed contracts, etc.). Files live envelope-encrypted on the
-- filesystem at <docs_dir>/<id>; metadata only is stored here.
--
-- Soft-delete is the default: SoftDelete sets deleted_at but keeps the
-- row + ciphertext on disk so audit history remains verifiable. Purge
-- is a separate admin-only op that removes both the row and the file.

CREATE TABLE IF NOT EXISTS compliance_docs (
    id              TEXT PRIMARY KEY,
    doc_type        TEXT NOT NULL DEFAULT '',
    filename        TEXT NOT NULL,
    mime_type       TEXT NOT NULL,
    size_bytes      INTEGER NOT NULL,
    sha256          TEXT NOT NULL,
    uploaded_by     TEXT NOT NULL,
    uploaded_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    replaced_at     DATETIME,
    replaced_by_id  TEXT,
    deleted_at      DATETIME,
    deleted_by      TEXT,
    FOREIGN KEY (replaced_by_id) REFERENCES compliance_docs(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_compliance_docs_doc_type ON compliance_docs(doc_type);
CREATE INDEX IF NOT EXISTS idx_compliance_docs_sha256 ON compliance_docs(sha256);

-- Many-to-many: one uploaded artifact (e.g., a BAA) may satisfy
-- controls in multiple frameworks (HIPAA 164.308(b), HITRUST 09.f,
-- SOC 2 CC9.2). Operators link the same doc to every (framework,
-- control_id) it covers.
CREATE TABLE IF NOT EXISTS compliance_doc_links (
    doc_id      TEXT NOT NULL,
    framework   TEXT NOT NULL,
    control_id  TEXT NOT NULL,
    linked_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    linked_by   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (doc_id, framework, control_id),
    FOREIGN KEY (doc_id) REFERENCES compliance_docs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_compliance_doc_links_framework_control
    ON compliance_doc_links(framework, control_id);
