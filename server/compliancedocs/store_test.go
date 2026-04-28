// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package compliancedocs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"git.alexander4.io/vigo/server/db"
)

// fakeCrypto is a deterministic identity-cipher for tests — keeps the
// test independent of the real AES-256-GCM envelope and lets us
// inspect on-disk ciphertext directly to verify the round-trip.
type fakeCrypto struct{}

func (fakeCrypto) Encrypt(p []byte) ([]byte, error) {
	out := make([]byte, len(p))
	for i, b := range p {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

func (fakeCrypto) Decrypt(c []byte) ([]byte, error) {
	out := make([]byte, len(c))
	for i, b := range c {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

// pdfHeader is a minimal PDF magic-byte prefix accepted by
// http.DetectContentType as application/pdf. The store rejects
// uploads where the detected MIME isn't in the allowlist, so tests
// must prefix payloads with a real signature.
var pdfHeader = []byte("%PDF-1.4\n")

func newTestStore(t *testing.T, caps Caps) (*Store, func()) {
	t.Helper()
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "test.db")
	sqlDB, err := sql.Open("sqlite3", "file:"+dbFile+"?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Apply migration manually since this is unit-scoped — no
	// dependency on the migrate runner for a couple of tables.
	schema := `
CREATE TABLE compliance_docs (
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
    deleted_by      TEXT
);
CREATE TABLE compliance_doc_links (
    doc_id      TEXT NOT NULL,
    framework   TEXT NOT NULL,
    control_id  TEXT NOT NULL,
    linked_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    linked_by   TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (doc_id, framework, control_id),
    FOREIGN KEY (doc_id) REFERENCES compliance_docs(id) ON DELETE CASCADE
);`
	if _, err := sqlDB.Exec(schema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store, err := New(filepath.Join(dir, "docs"), db.New(sqlDB), fakeCrypto{}, caps, []string{"application/pdf", "text/plain"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, func() { _ = sqlDB.Close() }
}

func TestPut_RoundTrip(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()

	plain := append(pdfHeader, []byte("body bytes")...)
	row, err := store.Put(context.Background(), PutInput{
		Plaintext:  plain,
		Filename:   "baa.pdf",
		MimeType:   "application/pdf",
		DocType:    "BAA",
		UploadedBy: "alice",
		Links: []ControlLink{
			{Framework: "hipaa", ControlID: "164.308(b)(1)"},
		},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if row.ID == "" {
		t.Fatal("missing ID")
	}
	if row.Filename != "baa.pdf" || row.SizeBytes != int64(len(plain)) {
		t.Errorf("metadata wrong: %+v", row)
	}

	got, _, err := store.Get(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("plaintext drift: got %q want %q", string(got), string(plain))
	}

	// Verify the on-disk file is the encrypted (XORed) form, not plaintext.
	raw, err := os.ReadFile(filepath.Join(store.docsDir, row.ID))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if string(raw) == string(plain) {
		t.Fatal("file landed as plaintext — encryption skipped")
	}
}

func TestPut_RejectsEmptyAndOversized(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 16})
	defer cleanup()

	_, err := store.Put(context.Background(), PutInput{Filename: "x.pdf", MimeType: "application/pdf"})
	if !errors.Is(err, ErrEmpty) {
		t.Errorf("empty upload: got %v, want ErrEmpty", err)
	}

	big := append(pdfHeader, make([]byte, 32)...)
	_, err = store.Put(context.Background(), PutInput{Plaintext: big, Filename: "big.pdf", MimeType: "application/pdf"})
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized: got %v, want ErrTooLarge", err)
	}
}

func TestPut_RejectsDisallowedMime(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()
	// PNG payload — magic bytes detect as image/png, which isn't in
	// our test allowlist (application/pdf, text/plain).
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0}
	_, err := store.Put(context.Background(), PutInput{Plaintext: png, Filename: "x.png", MimeType: "image/png"})
	if !errors.Is(err, ErrMimeNotAllowed) {
		t.Errorf("disallowed mime: got %v, want ErrMimeNotAllowed", err)
	}
}

func TestPut_RejectsRenamedFileType(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()
	// Caller declares application/pdf but the bytes are plain text.
	// Detection sees text/plain (in allowlist), but the declared MIME
	// passes too. We still want this to be allowed — magic-byte detection
	// landing in the allowlist counts. Construct a real reject case:
	// declared application/pdf, bytes detected as application/octet-stream
	// (not in allowlist).
	binary := []byte{0xFF, 0xD8, 0xFF, 0xE0} // JPEG magic — not in allowlist
	_, err := store.Put(context.Background(), PutInput{Plaintext: binary, Filename: "fake.pdf", MimeType: "application/pdf"})
	if !errors.Is(err, ErrMimeNotAllowed) {
		t.Errorf("bytes-vs-declared mismatch: got %v, want ErrMimeNotAllowed", err)
	}
}

func TestSoftDelete_HidesFromList(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()
	row, err := store.Put(context.Background(), PutInput{
		Plaintext: append(pdfHeader, []byte("body")...), Filename: "p.pdf", MimeType: "application/pdf", UploadedBy: "u",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if rows, _ := store.List(context.Background()); len(rows) != 1 {
		t.Fatalf("List before delete: %d, want 1", len(rows))
	}
	if err := store.SoftDelete(context.Background(), row.ID, "u"); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if rows, _ := store.List(context.Background()); len(rows) != 0 {
		t.Fatalf("List after delete: %d, want 0", len(rows))
	}
	// Second SoftDelete is idempotent-ish — caller sees ErrAlreadyDeleted.
	if err := store.SoftDelete(context.Background(), row.ID, "u"); !errors.Is(err, ErrAlreadyDeleted) {
		t.Errorf("second delete: got %v, want ErrAlreadyDeleted", err)
	}
}

func TestPurge_RemovesEverything(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()
	row, err := store.Put(context.Background(), PutInput{
		Plaintext: append(pdfHeader, []byte("body")...), Filename: "p.pdf", MimeType: "application/pdf", UploadedBy: "u",
		Links: []ControlLink{{Framework: "hipaa", ControlID: "164.308(b)(1)"}},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Purge(context.Background(), row.ID); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := store.GetMeta(context.Background(), row.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-purge GetMeta: got %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(store.docsDir, row.ID)); !os.IsNotExist(err) {
		t.Errorf("post-purge file still on disk: %v", err)
	}
}

func TestLinks_AddRemoveList(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()
	row, err := store.Put(context.Background(), PutInput{
		Plaintext: append(pdfHeader, []byte("body")...), Filename: "baa.pdf", MimeType: "application/pdf", UploadedBy: "u",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	// One BAA covering three frameworks.
	for _, l := range []ControlLink{
		{Framework: "hipaa", ControlID: "164.308(b)(1)"},
		{Framework: "hitrust", ControlID: "09.f"},
		{Framework: "soc2", ControlID: "CC9.2"},
	} {
		if err := store.AddLink(context.Background(), row.ID, l.Framework, l.ControlID, "u"); err != nil {
			t.Fatalf("AddLink %v: %v", l, err)
		}
	}
	links, err := store.ListLinks(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(links) != 3 {
		t.Errorf("links: got %d, want 3", len(links))
	}

	// HasActiveDocForControl should answer true for any of the three.
	for _, l := range []ControlLink{
		{Framework: "hipaa", ControlID: "164.308(b)(1)"},
		{Framework: "hitrust", ControlID: "09.f"},
	} {
		got, err := store.HasActiveDocForControl(context.Background(), l.Framework, l.ControlID)
		if err != nil || !got {
			t.Errorf("HasActiveDocForControl(%v): %v %v, want true nil", l, got, err)
		}
	}

	// Detach one link; the doc itself stays.
	if err := store.RemoveLink(context.Background(), row.ID, "soc2", "CC9.2"); err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	got, err := store.HasActiveDocForControl(context.Background(), "soc2", "CC9.2")
	if err != nil {
		t.Fatalf("Has post-remove: %v", err)
	}
	if got {
		t.Error("HasActiveDocForControl still true after RemoveLink")
	}
}

func TestReplace_PreservesLinkSet(t *testing.T) {
	store, cleanup := newTestStore(t, Caps{MaxDocBytes: 1 << 20})
	defer cleanup()
	original, err := store.Put(context.Background(), PutInput{
		Plaintext: append(pdfHeader, []byte("v1")...), Filename: "ir.pdf", MimeType: "application/pdf", UploadedBy: "u",
		Links: []ControlLink{
			{Framework: "hipaa", ControlID: "164.308(a)(6)(i)"},
			{Framework: "soc2", ControlID: "CC7.4"},
		},
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	replacement, err := store.Replace(context.Background(), original.ID, PutInput{
		Plaintext: append(pdfHeader, []byte("v2 contents")...), Filename: "ir-v2.pdf", MimeType: "application/pdf", UploadedBy: "u",
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if replacement.ID == original.ID {
		t.Fatal("replacement reused original ID")
	}
	links, err := store.ListLinks(context.Background(), replacement.ID)
	if err != nil {
		t.Fatalf("ListLinks(new): %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("link inheritance: got %d, want 2", len(links))
	}
	// Predecessor row stays in place but is marked replaced_by_id.
	old, err := store.GetMeta(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("GetMeta(old): %v", err)
	}
	if !old.ReplacedAt.Valid || !old.ReplacedByID.Valid || old.ReplacedByID.String != replacement.ID {
		t.Errorf("predecessor not marked replaced: %+v", old)
	}
}
