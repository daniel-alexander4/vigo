// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package compliancedocs implements the encrypted-at-rest store for
// operator-uploaded compliance documentation that satisfies
// documented-scope framework controls (BAA, IR plan, risk assessment,
// training records, signed contracts, etc.).
//
// Files land at <docs_dir>/<doc-id>, AES-256-GCM enveloped with the
// master key from server/secrets.LocalProvider. Metadata (filename,
// MIME type, size, SHA-256 of plaintext, uploader, timestamps) lives
// in the compliance_docs table; doc-to-(framework, control_id)
// associations live in compliance_doc_links — many-to-many so a
// single uploaded BAA can cover HIPAA 164.308(b), HITRUST 09.f, and
// SOC 2 CC9.2 with one upload.
package compliancedocs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"git.alexander4.io/vigo/server/db"
	"git.alexander4.io/vigo/server/secrets"
)

// sqlNullString wraps a Go string in a sql.NullString. Empty strings
// become invalid (so the column reads NULL on disk), which matches the
// soft-delete and replacement semantics — an unreplaced doc has
// replaced_by_id NULL, not "".
func sqlNullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// Errors returned by Store operations.
var (
	ErrNotFound       = errors.New("compliance doc not found")
	ErrAlreadyDeleted = errors.New("compliance doc already soft-deleted")
	ErrTooLarge       = errors.New("upload exceeds size cap")
	ErrFrameworkFull  = errors.New("upload would exceed per-framework size cap")
	ErrTotalFull      = errors.New("upload would exceed total store size cap")
	ErrMimeNotAllowed = errors.New("mime type not allowed")
	ErrEmpty          = errors.New("upload is empty")
)

// Caps bounds how much a single upload, a single framework, and the
// store as a whole may hold. Zero or negative fields are treated as
// "no limit", but ServerConfig defaults them to sane non-zero values.
type Caps struct {
	MaxDocBytes          int64
	MaxPerFrameworkBytes int64
	MaxTotalBytes        int64
}

// Crypto wraps the AES-256-GCM envelope used for at-rest encryption.
// In production this is a *secrets.LocalProvider; tests can supply a
// minimal mock. The interface is small enough that no separate
// abstraction lives outside this package.
type Crypto interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(envelope []byte) ([]byte, error)
}

// compile-time check that LocalProvider satisfies Crypto.
var _ Crypto = (*secrets.LocalProvider)(nil)

// Store coordinates filesystem ciphertext + DB metadata for compliance
// documentation uploads. All operations are concurrency-safe at the
// DB level (sqlite serializes writers); filesystem writes use atomic
// rename so partial state never leaks.
type Store struct {
	docsDir       string
	queries       *db.Queries
	crypto        Crypto
	caps          Caps
	mimeAllowlist map[string]struct{}
}

// New returns a Store ready for use. docsDir is created at 0700 if it
// doesn't exist. mimeAllowlist must be non-empty.
func New(docsDir string, queries *db.Queries, crypto Crypto, caps Caps, mimeAllowlist []string) (*Store, error) {
	if docsDir == "" {
		return nil, fmt.Errorf("docsDir required")
	}
	if queries == nil {
		return nil, fmt.Errorf("queries required")
	}
	if crypto == nil {
		return nil, fmt.Errorf("crypto required")
	}
	if len(mimeAllowlist) == 0 {
		return nil, fmt.Errorf("mimeAllowlist required")
	}
	if err := os.MkdirAll(docsDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating docs directory %q: %w", docsDir, err)
	}
	allow := make(map[string]struct{}, len(mimeAllowlist))
	for _, m := range mimeAllowlist {
		allow[strings.ToLower(strings.TrimSpace(m))] = struct{}{}
	}
	return &Store{
		docsDir:       docsDir,
		queries:       queries,
		crypto:        crypto,
		caps:          caps,
		mimeAllowlist: allow,
	}, nil
}

// PutInput is the minimal information Put needs to create a new doc.
// MimeType is the *declared* type from the upload — the store sniffs
// the actual content with http.DetectContentType and rejects when the
// detected type isn't in the allowlist, so the declared header alone
// can't be used to slip through.
type PutInput struct {
	Plaintext  []byte
	Filename   string
	MimeType   string
	DocType    string // free-form category for cross-framework reuse (BAA, IR Plan, Risk Assessment, ...)
	UploadedBy string
	// Links is the initial set of (framework, control_id) attachments
	// for this doc. May be empty; the operator can add links later.
	Links []ControlLink
}

// ControlLink names a (framework, control_id) pair this doc satisfies.
type ControlLink struct {
	Framework string
	ControlID string
}

// Put validates, encrypts, and persists a new compliance doc, plus its
// initial control links. Caller must hold the auth/permission gate
// before calling.
//
// Validation order: empty → too large → MIME allowlist → per-framework
// budget (across the union of links' frameworks) → total budget.
// The rest of the operation is best-effort: ciphertext write first,
// DB row second; on DB failure the orphaned ciphertext is removed.
func (s *Store) Put(ctx context.Context, in PutInput) (db.ComplianceDoc, error) {
	if len(in.Plaintext) == 0 {
		return db.ComplianceDoc{}, ErrEmpty
	}
	size := int64(len(in.Plaintext))
	if s.caps.MaxDocBytes > 0 && size > s.caps.MaxDocBytes {
		return db.ComplianceDoc{}, fmt.Errorf("%w: %d > %d", ErrTooLarge, size, s.caps.MaxDocBytes)
	}

	mime := strings.ToLower(strings.TrimSpace(in.MimeType))
	if mime == "" {
		mime = http.DetectContentType(in.Plaintext)
		// http.DetectContentType returns parameters for some types; trim them.
		if i := strings.IndexByte(mime, ';'); i > 0 {
			mime = strings.TrimSpace(mime[:i])
		}
	}
	if _, ok := s.mimeAllowlist[mime]; !ok {
		return db.ComplianceDoc{}, fmt.Errorf("%w: %q", ErrMimeNotAllowed, mime)
	}

	// Cross-check declared MIME against detected MIME (magic bytes).
	// Reject when detected isn't in the allowlist either — this catches
	// renamed `.pdf` containing a different file type.
	detected := http.DetectContentType(in.Plaintext)
	if i := strings.IndexByte(detected, ';'); i > 0 {
		detected = strings.TrimSpace(detected[:i])
	}
	if _, ok := s.mimeAllowlist[strings.ToLower(detected)]; !ok {
		return db.ComplianceDoc{}, fmt.Errorf("%w: detected %q (does not match allowlist)", ErrMimeNotAllowed, detected)
	}

	if err := s.checkBudgets(ctx, size, in.Links); err != nil {
		return db.ComplianceDoc{}, err
	}

	id := uuid.NewString()
	hash := sha256.Sum256(in.Plaintext)
	hashHex := hex.EncodeToString(hash[:])

	envelope, err := s.crypto.Encrypt(in.Plaintext)
	if err != nil {
		return db.ComplianceDoc{}, fmt.Errorf("encrypt: %w", err)
	}

	path := s.docPath(id)
	if err := writeFileAtomic(path, envelope); err != nil {
		return db.ComplianceDoc{}, fmt.Errorf("writing ciphertext: %w", err)
	}

	row, err := s.queries.CreateComplianceDoc(ctx, db.CreateComplianceDocParams{
		ID:         id,
		DocType:    in.DocType,
		Filename:   in.Filename,
		MimeType:   mime,
		SizeBytes:  size,
		Sha256:     hashHex,
		UploadedBy: in.UploadedBy,
	})
	if err != nil {
		// Roll back the orphaned ciphertext. The row insert already
		// failed; a stale file is the smaller problem and doesn't
		// change the caller's outcome.
		// best-effort
		_ = os.Remove(path)
		return db.ComplianceDoc{}, fmt.Errorf("inserting doc row: %w", err)
	}

	linkedBy := in.UploadedBy
	for _, link := range in.Links {
		if link.Framework == "" || link.ControlID == "" {
			continue
		}
		if err := s.queries.AddComplianceDocLink(ctx, db.AddComplianceDocLinkParams{
			DocID:     id,
			Framework: link.Framework,
			ControlID: link.ControlID,
			LinkedBy:  linkedBy,
		}); err != nil {
			// Continue past link errors; the doc itself landed. The
			// caller can retry AddLink for any that failed.
			continue
		}
	}

	return row, nil
}

// Get returns the decrypted plaintext for a doc. Soft-deleted docs are
// still readable so audit bundles assembled from compliance_docs_audit
// can verify history; callers wanting "active doc only" should check
// row.DeletedAt.Valid first via GetMeta.
func (s *Store) Get(ctx context.Context, id string) ([]byte, db.ComplianceDoc, error) {
	row, err := s.queries.GetComplianceDoc(ctx, id)
	if err != nil {
		return nil, db.ComplianceDoc{}, ErrNotFound
	}
	envelope, err := os.ReadFile(s.docPath(id))
	if err != nil {
		return nil, db.ComplianceDoc{}, fmt.Errorf("reading ciphertext: %w", err)
	}
	plaintext, err := s.crypto.Decrypt(envelope)
	if err != nil {
		return nil, db.ComplianceDoc{}, fmt.Errorf("decrypt: %w", err)
	}
	// Defense-in-depth: verify SHA-256 round-trips. A mismatch means
	// either the ciphertext was tampered with or the master key
	// changed without re-encrypting on rotation.
	hash := sha256.Sum256(plaintext)
	if hex.EncodeToString(hash[:]) != row.Sha256 {
		return nil, db.ComplianceDoc{}, fmt.Errorf("sha256 mismatch on decrypt — ciphertext or key drift")
	}
	return plaintext, row, nil
}

// GetMeta returns just the metadata row, no decryption.
func (s *Store) GetMeta(ctx context.Context, id string) (db.ComplianceDoc, error) {
	row, err := s.queries.GetComplianceDoc(ctx, id)
	if err != nil {
		return db.ComplianceDoc{}, ErrNotFound
	}
	return row, nil
}

// List returns active (non-soft-deleted) doc metadata.
func (s *Store) List(ctx context.Context) ([]db.ComplianceDoc, error) {
	return s.queries.ListComplianceDocs(ctx)
}

// ListByControl returns active docs attached to (framework, controlID).
func (s *Store) ListByControl(ctx context.Context, framework, controlID string) ([]db.ComplianceDoc, error) {
	return s.queries.ListComplianceDocsByControl(ctx, db.ListComplianceDocsByControlParams{
		Framework: framework,
		ControlID: controlID,
	})
}

// HasActiveDocForControl reports whether at least one non-deleted doc
// is attached to (framework, controlID). Used by the partition logic
// in Phase B+ to flip documented-scope controls from missing → covered
// when documentation is on file.
func (s *Store) HasActiveDocForControl(ctx context.Context, framework, controlID string) (bool, error) {
	got, err := s.queries.HasActiveDocForControl(ctx, db.HasActiveDocForControlParams{
		Framework: framework,
		ControlID: controlID,
	})
	if err != nil {
		return false, err
	}
	return got != 0, nil
}

// CountActiveDocsForControl returns the number of non-deleted docs
// attached to (framework, controlID). Used by the framework report
// page to show a per-control attachment count next to documented-scope
// rows so operators can see at a glance which Process controls have
// supporting evidence.
func (s *Store) CountActiveDocsForControl(ctx context.Context, framework, controlID string) (int, error) {
	n, err := s.queries.CountActiveDocsForControl(ctx, db.CountActiveDocsForControlParams{
		Framework: framework,
		ControlID: controlID,
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// ActiveDocCountsByControl returns a map of control_id → attached-doc
// count for a single framework, in one round trip. Empty map when no
// docs are attached for any control. Used by the framework report
// page to populate per-control doc counts without N+1 queries.
func (s *Store) ActiveDocCountsByControl(ctx context.Context, framework string) (map[string]int, error) {
	rows, err := s.queries.CountActiveDocsByControlForFramework(ctx, framework)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.ControlID] = int(r.Total)
	}
	return out, nil
}

// AddLink attaches an existing doc to a (framework, controlID) pair.
// Idempotent — the underlying SQL uses INSERT OR IGNORE so duplicate
// links are no-ops, not errors.
func (s *Store) AddLink(ctx context.Context, docID, framework, controlID, linkedBy string) error {
	if _, err := s.queries.GetComplianceDoc(ctx, docID); err != nil {
		return ErrNotFound
	}
	return s.queries.AddComplianceDocLink(ctx, db.AddComplianceDocLinkParams{
		DocID:     docID,
		Framework: framework,
		ControlID: controlID,
		LinkedBy:  linkedBy,
	})
}

// RemoveLink detaches a doc from a (framework, controlID) pair without
// touching the underlying file. The doc may still be linked to other
// controls or be reachable as historical audit evidence.
func (s *Store) RemoveLink(ctx context.Context, docID, framework, controlID string) error {
	return s.queries.RemoveComplianceDocLink(ctx, db.RemoveComplianceDocLinkParams{
		DocID:     docID,
		Framework: framework,
		ControlID: controlID,
	})
}

// ListLinks returns every (framework, control_id) pair attached to a
// doc. Useful when an operator wants to see "this BAA covers HIPAA
// 164.308(b), HITRUST 09.f, SOC 2 CC9.2."
func (s *Store) ListLinks(ctx context.Context, docID string) ([]db.ComplianceDocLink, error) {
	return s.queries.ListComplianceDocLinks(ctx, docID)
}

// Replace creates a new doc with the given plaintext, links it to the
// same (framework, control_id) set as the predecessor, and marks the
// predecessor as replaced (replaced_at + replaced_by_id). The old
// ciphertext stays on disk for audit history; SoftDelete is a separate
// operation if the operator wants the predecessor gone.
func (s *Store) Replace(ctx context.Context, oldID string, in PutInput) (db.ComplianceDoc, error) {
	oldRow, err := s.queries.GetComplianceDoc(ctx, oldID)
	if err != nil {
		return db.ComplianceDoc{}, ErrNotFound
	}
	if oldRow.DeletedAt.Valid {
		return db.ComplianceDoc{}, ErrAlreadyDeleted
	}

	// Inherit the predecessor's link set so the new doc's coverage
	// matches by default. Caller may add/remove links afterward.
	if len(in.Links) == 0 {
		links, err := s.queries.ListComplianceDocLinks(ctx, oldID)
		if err == nil {
			in.Links = make([]ControlLink, 0, len(links))
			for _, l := range links {
				in.Links = append(in.Links, ControlLink{Framework: l.Framework, ControlID: l.ControlID})
			}
		}
	}

	newRow, err := s.Put(ctx, in)
	if err != nil {
		return db.ComplianceDoc{}, err
	}

	if err := s.queries.MarkComplianceDocReplaced(ctx, db.MarkComplianceDocReplacedParams{
		ID:           oldID,
		ReplacedByID: sqlNullString(newRow.ID),
	}); err != nil {
		// Predecessor stays intact on link error; the new row already
		// landed. Operator can manually mark replaced via SQL if this
		// matters; not worth a transactional rollback here.
		return newRow, fmt.Errorf("marking %s replaced: %w", oldID, err)
	}
	return newRow, nil
}

// SoftDelete hides a doc from List/ListByControl by setting
// deleted_at, but keeps the ciphertext + row in place so historical
// audit bundles can still verify it. Use Purge to physically remove.
func (s *Store) SoftDelete(ctx context.Context, id, deletedBy string) error {
	row, err := s.queries.GetComplianceDoc(ctx, id)
	if err != nil {
		return ErrNotFound
	}
	if row.DeletedAt.Valid {
		return ErrAlreadyDeleted
	}
	return s.queries.SoftDeleteComplianceDoc(ctx, db.SoftDeleteComplianceDocParams{
		ID:        id,
		DeletedBy: sqlNullString(deletedBy),
	})
}

// Purge physically removes ciphertext + DB row. Cascades through
// compliance_doc_links via FK ON DELETE CASCADE. Reserved for
// compliance.docs.purge permission.
func (s *Store) Purge(ctx context.Context, id string) error {
	if _, err := s.queries.GetComplianceDoc(ctx, id); err != nil {
		return ErrNotFound
	}
	if err := os.Remove(s.docPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing ciphertext for %s: %w", id, err)
	}
	return s.queries.PurgeComplianceDoc(ctx, id)
}

// FrameworkUsage returns the total ciphertext bytes attached to a
// framework across all active docs.
func (s *Store) FrameworkUsage(ctx context.Context, framework string) (int64, error) {
	return s.queries.SumComplianceDocsBytesByFramework(ctx, framework)
}

// TotalUsage returns the deployment-wide ciphertext bytes across all
// active docs.
func (s *Store) TotalUsage(ctx context.Context) (int64, error) {
	return s.queries.SumComplianceDocsBytes(ctx)
}

// docPath returns the absolute on-disk path for a doc's ciphertext.
// IDs are UUIDs from uuid.NewString(); they cannot escape docsDir
// because filepath.Join discards leading separators in a UUID-shaped
// segment.
func (s *Store) docPath(id string) string {
	return filepath.Join(s.docsDir, id)
}

// checkBudgets enforces per-framework and total caps before encryption.
// The check sums "size" against current storage to anticipate the
// post-write state — there's a race window with concurrent uploads
// but the cap is informational, not a security boundary, so a small
// overshoot is acceptable.
func (s *Store) checkBudgets(ctx context.Context, size int64, links []ControlLink) error {
	if s.caps.MaxTotalBytes > 0 {
		total, err := s.queries.SumComplianceDocsBytes(ctx)
		if err == nil && total+size > s.caps.MaxTotalBytes {
			return fmt.Errorf("%w: %d + %d > %d", ErrTotalFull, total, size, s.caps.MaxTotalBytes)
		}
	}
	if s.caps.MaxPerFrameworkBytes > 0 {
		seen := make(map[string]struct{}, len(links))
		for _, l := range links {
			if _, dup := seen[l.Framework]; dup {
				continue
			}
			seen[l.Framework] = struct{}{}
			used, err := s.queries.SumComplianceDocsBytesByFramework(ctx, l.Framework)
			if err == nil && used+size > s.caps.MaxPerFrameworkBytes {
				return fmt.Errorf("%w: framework=%s used=%d add=%d cap=%d", ErrFrameworkFull, l.Framework, used, size, s.caps.MaxPerFrameworkBytes)
			}
		}
	}
	return nil
}

// writeFileAtomic writes data to path via tempfile + rename so partial
// writes never appear under the final name.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Cleanup if rename fails or a panic intervenes; nothing to
		// recover with a non-nil error from a deferred Remove.
		// best-effort
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
