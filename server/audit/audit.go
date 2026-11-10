// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

// Package audit provides an append-only audit trail linked by a SHA-256 hash
// chain. Each entry includes the hash of the previous entry, so an edit to a
// stored row, a deletion from the middle of the log, or an out-of-order insert
// breaks the chain and Verify reports it. Deleting entries from the TAIL is
// caught by the head anchor: the Writer records the newest entry's id and hash
// in a file outside the database (headAnchorFile) and Verify checks that entry
// still exists unchanged.
//
// What this does NOT detect: the chain is unkeyed, so anyone who can write the
// database AND the head-anchor file can rewrite history and recompute every
// hash. It detects accidental damage and database-only tampering (an edited or
// restored DB file), not a root-level attacker on the server host. Ship the
// audit stream off-host (the integrations dispatcher fans every entry out to
// SIEM targets) for evidence an attacker on the box cannot rewrite.
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// Audit hash-scheme versions. The scheme is recorded per-row (audit_entries.
// hash_version) so the chain can evolve forward without rehashing — and thus
// invalidating — existing entries.
//
//   - V1: legacy bare concatenation of fields. Ambiguous, because adjacent
//     fields run together with no delimiter (actor="ab",source="c" hashes
//     identically to actor="a",source="bc"). Frozen byte-for-byte: old rows
//     are stored with this scheme and must keep verifying.
//   - V2: length-prefixed scheme — each field is preceded by its big-endian
//     uint64 byte length, so no field boundary is ambiguous. All new rows.
const (
	hashVersionV1 int64 = 1
	hashVersionV2 int64 = 2
)

// auditLoadTimeout bounds each DB lookup of the chain head. A wedged SQLite
// WAL checkpoint or slow-mount NFS path must not pin server boot: NewWriter
// gives up after this long and the first Record retries the lookup instead.
const auditLoadTimeout = 5 * time.Second

// HeadAnchorFile is the file name, under the server root, where the Writer
// records the id and hash of the newest entry after every write. It lives
// outside the database so deleting rows from the tail of the log (or restoring
// an older database file) is detected: Verify requires the anchored entry to
// still exist with the anchored hash.
const HeadAnchorFile = "audit-head.json"

// headAnchor is the newest entry the Writer knows it wrote.
type headAnchor struct {
	ID   int64  `json:"id"`
	Hash string `json:"hash"`
}

// anchors maps a *db.Queries to the head anchor of the Writer that writes
// through it, so the package-level VerifyFull (called by handlers holding only
// the queries) can check for tail truncation. Values are *headAnchor; a nil
// value or absent key means no anchor (no Writer, or an empty log).
var anchors sync.Map

func currentAnchor(q *db.Queries) *headAnchor {
	if v, ok := anchors.Load(q); ok {
		if a, _ := v.(*headAnchor); a != nil {
			c := *a
			return &c
		}
	}
	return nil
}

// Writer appends entries to the audit hash chain. It tracks the previous
// entry's hash in memory; all writes are serialized through a mutex.
type Writer struct {
	queries  *db.Queries
	headPath string // "" = no persisted anchor; the in-memory anchor is still kept
	mu       sync.Mutex
	prevHash string // hash of the last entry (empty string for first entry)

	// headLoaded is false until the chain head has been read from the DB.
	// NewWriter tries once under auditLoadTimeout; if that fails, Record
	// retries before every write and refuses to write until it succeeds.
	// Writing without the head would start a second chain with an empty
	// prev_hash, leaving VerifyFull invalid forever.
	headLoaded bool

	// pendingMarker is an in-band entry written, linked into the chain, ahead
	// of the next event: the boot-time head load failed and was recovered
	// (audit.head_load_deferred), or the persisted head anchor is missing from
	// the DB (audit.head_mismatch — the tail of the log was truncated or an
	// older DB was restored).
	pendingMarkerType    string
	pendingMarkerSummary string

	// OnRecord, when set, is invoked after a successful chain-write with the
	// same fields that were persisted. The server wires this to the
	// integrations dispatcher so audit rows surface as `audit.<eventType>`
	// events to Loki / Splunk / Datadog / Sentinel / etc. alongside the
	// existing event stream (envoy.offline, run.failure, ...). Nil-tolerant.
	OnRecord func(eventType, actor, source, summary, payload string)
}

// WriterOption configures a Writer.
type WriterOption func(*Writer)

// WithHeadFile persists the head anchor at path (normally
// <serverRoot>/audit-head.json) so tail truncation is detected across
// restarts. Without it the anchor is kept in memory only.
func WithHeadFile(path string) WriterOption {
	return func(w *Writer) { w.headPath = path }
}

// NewWriter creates a Writer and loads the latest hash from the DB to continue
// the chain. The DB lookup is bounded by auditLoadTimeout; on failure the
// first Record retries it rather than pinning server startup.
func NewWriter(queries *db.Queries, opts ...WriterOption) *Writer {
	w := &Writer{queries: queries}
	for _, o := range opts {
		o(w)
	}

	ctx, cancel := context.WithTimeout(context.Background(), auditLoadTimeout)
	defer cancel()
	if err := w.loadHeadLocked(ctx); err != nil {
		slog.Error("audit: failed to load chain head at boot; the first audit write will retry it",
			"error", err, "timeout", auditLoadTimeout)
		w.pendingMarkerType = "audit.head_load_deferred"
		w.pendingMarkerSummary = "audit chain head could not be read at boot (" + err.Error() + "); recovered on first write, chain continues"
	}
	return w
}

// loadHeadLocked reads the newest DB entry into prevHash and reconciles it
// with the persisted head anchor. The caller holds w.mu (or owns w exclusively).
func (w *Writer) loadHeadLocked(ctx context.Context) error {
	var dbHead *headAnchor
	last, err := w.queries.GetLastAuditEntry(ctx)
	switch {
	case err == nil:
		dbHead = &headAnchor{ID: last.ID, Hash: last.Hash}
	case errors.Is(err, sql.ErrNoRows):
		// Empty log.
	default:
		return err
	}

	fileHead, ferr := w.readHeadFile()
	if ferr != nil {
		slog.Error("audit: unreadable head-anchor file; treating as absent", "path", w.headPath, "error", ferr)
	}

	anchor := dbHead
	if fileHead != nil {
		got, gerr := w.queries.GetAuditEntry(ctx, fileHead.ID)
		switch {
		case gerr == nil && got.Hash == fileHead.Hash:
			// Anchored entry intact; the DB head is at or past it.
		case gerr == nil || errors.Is(gerr, sql.ErrNoRows):
			// The anchored entry is gone or changed: the tail was truncated or
			// an older database was restored. Keep the stale anchor so Verify
			// reports the mismatch, and record the event in-band ahead of the
			// next entry, after which the anchor advances.
			dbDesc := "empty log"
			if dbHead != nil {
				dbDesc = fmt.Sprintf("entry %d (%s)", dbHead.ID, dbHead.Hash)
			}
			slog.Error("audit: head anchor not found in database — log truncated or older DB restored",
				"anchored_id", fileHead.ID, "anchored_hash", truncHash(fileHead.Hash), "db_head", dbDesc)
			anchor = fileHead
			w.pendingMarkerType = "audit.head_mismatch"
			w.pendingMarkerSummary = fmt.Sprintf("audit head anchor entry %d (%s) is missing or altered; database head is %s",
				fileHead.ID, fileHead.Hash, dbDesc)
		default:
			return gerr
		}
	}

	w.prevHash = ""
	if dbHead != nil {
		w.prevHash = dbHead.Hash
	}
	w.headLoaded = true
	anchors.Store(w.queries, anchor)
	return nil
}

func (w *Writer) readHeadFile() (*headAnchor, error) {
	if w.headPath == "" {
		return nil, nil
	}
	b, err := os.ReadFile(w.headPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a headAnchor
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, err
	}
	if a.ID <= 0 || a.Hash == "" {
		return nil, errors.New("head anchor has no id/hash")
	}
	return &a, nil
}

// writeHeadFile persists the anchor atomically (temp file + rename, 0600).
func (w *Writer) writeHeadFile(a headAnchor) error {
	if w.headPath == "" {
		return nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(w.headPath), ".audit-head-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, w.headPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// timestampFormat is the layout used for hash computation. It must match
// what Verify reads back from the DB after round-tripping through SQLite.
// We use second-precision UTC to avoid any driver-dependent nanosecond
// truncation.
const timestampFormat = time.RFC3339

// Record appends an audit entry to the hash chain. New entries are written
// with the V2 (length-prefixed) hash scheme; see computeHashV2.
func (w *Writer) Record(ctx context.Context, eventType, actor, source, summary, payload string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// The boot-time head load failed: retry it now. Writing without the head
	// would fork the chain, so refuse the write until the head is known.
	if !w.headLoaded {
		lctx, cancel := context.WithTimeout(ctx, auditLoadTimeout)
		err := w.loadHeadLocked(lctx)
		cancel()
		if err != nil {
			return fmt.Errorf("loading audit chain head: %w", err)
		}
	}

	// In-band marker for a recovered head load or a detected head mismatch,
	// linked into the chain like any other entry.
	if w.pendingMarkerType != "" {
		if err := w.recordLocked(ctx, w.pendingMarkerType, "system", "audit", w.pendingMarkerSummary, ""); err != nil {
			return fmt.Errorf("writing %s marker: %w", w.pendingMarkerType, err)
		}
		w.pendingMarkerType, w.pendingMarkerSummary = "", ""
	}

	return w.recordLocked(ctx, eventType, actor, source, summary, payload)
}

// recordLocked appends a single entry to the chain. The caller must hold w.mu.
func (w *Writer) recordLocked(ctx context.Context, eventType, actor, source, summary, payload string) error {
	now := time.Now().UTC().Truncate(time.Second)
	tsStr := now.Format(timestampFormat)
	// All new entries use V2 (length-prefixed); old rows keep their stored V1.
	hash := computeHashV2(w.prevHash, tsStr, eventType, actor, source, summary, payload)

	entry, err := w.queries.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
		PrevHash:    w.prevHash,
		Hash:        hash,
		Timestamp:   now,
		EventType:   eventType,
		Actor:       actor,
		Source:      source,
		Summary:     summary,
		Payload:     payload,
		HashVersion: hashVersionV2,
	})
	if err != nil {
		return fmt.Errorf("inserting audit entry: %w", err)
	}

	w.prevHash = entry.Hash
	anchor := headAnchor{ID: entry.ID, Hash: entry.Hash}
	anchors.Store(w.queries, &anchor)
	if err := w.writeHeadFile(anchor); err != nil {
		// The entry is committed; a stale anchor file only weakens truncation
		// detection across a restart until the next successful write.
		slog.Error("audit: failed to persist head anchor", "path", w.headPath, "error", err)
	}

	slog.Debug("audit entry recorded",
		"id", entry.ID,
		"type", eventType,
		"actor", actor,
	)

	if w.OnRecord != nil {
		w.OnRecord(eventType, actor, source, summary, payload)
	}

	return nil
}

// computeHashForVersion dispatches to the hash scheme a row was recorded with.
// An unknown version is an error: verification refuses it rather than guessing
// a scheme.
func computeHashForVersion(version int64, prevHash, timestamp, eventType, actor, source, summary, payload string) (string, error) {
	switch version {
	case hashVersionV2:
		return computeHashV2(prevHash, timestamp, eventType, actor, source, summary, payload), nil
	case hashVersionV1:
		return computeHashV1(prevHash, timestamp, eventType, actor, source, summary, payload), nil
	default:
		return "", fmt.Errorf("unknown hash_version %d", version)
	}
}

// computeHashV1 is the legacy bare-concatenation scheme. FROZEN: it must remain
// byte-for-byte identical to the original computeHash so rows recorded before
// the version column (defaulted to hash_version=1) still verify against their
// stored hash. Do not add delimiters or change field order here.
func computeHashV1(prevHash, timestamp, eventType, actor, source, summary, payload string) string {
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte(timestamp))
	h.Write([]byte(eventType))
	h.Write([]byte(actor))
	h.Write([]byte(source))
	h.Write([]byte(summary))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}

// computeHashV2 is the collision-resistant scheme: each field is preceded by
// its big-endian uint64 byte length, so field boundaries are unambiguous and no
// two distinct field tuples can hash alike. Used for all new entries.
func computeHashV2(prevHash, timestamp, eventType, actor, source, summary, payload string) string {
	h := sha256.New()
	for _, field := range []string{prevHash, timestamp, eventType, actor, source, summary, payload} {
		var lenPrefix [8]byte
		binary.BigEndian.PutUint64(lenPrefix[:], uint64(len(field)))
		h.Write(lenPrefix[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyResult holds the outcome of a chain verification.
type VerifyResult struct {
	Valid       bool   `json:"valid"`
	EntryCount  int64  `json:"entry_count"`
	FirstBroken int64  `json:"first_broken,omitempty"` // ID of first broken entry (0 if valid)
	Error       string `json:"error,omitempty"`
}

// verifyChainSegment walks entries in id order, recomputing each hash and
// checking chain linkage, starting from startPrevHash (the hash the first
// entry's prev_hash must equal — "" for the head of the chain, or the
// checkpoint hash for an incremental segment). Returns the last entry's hash
// and, on the first break, that entry's ID + a message (firstBroken == 0 means
// the whole segment verified).
func verifyChainSegment(entries []db.AuditEntry, startPrevHash string) (lastHash string, firstBroken int64, errMsg string) {
	prevHash := startPrevHash
	for _, e := range entries {
		// Recompute with the scheme this row was recorded under. The stored
		// hash never changes, so V1 rows verify with V1 and V2 rows with V2 —
		// the chain stays continuous across the format bump.
		expected, err := computeHashForVersion(e.HashVersion, prevHash, e.Timestamp.UTC().Format(timestampFormat), e.EventType, e.Actor, e.Source, e.Summary, e.Payload)
		if err != nil {
			return prevHash, e.ID, fmt.Sprintf("entry %d: %v", e.ID, err)
		}
		if e.Hash != expected {
			return prevHash, e.ID, fmt.Sprintf("hash mismatch at entry %d: expected %s, got %s", e.ID, truncHash(expected), truncHash(e.Hash))
		}
		if e.PrevHash != prevHash {
			return prevHash, e.ID, fmt.Sprintf("chain break at entry %d: prev_hash does not match previous entry", e.ID)
		}
		prevHash = e.Hash
	}
	return prevHash, 0, ""
}

// VerifyFull walks the entire audit chain fresh (no checkpoint) and recomputes
// every hash — the stateless, on-demand integrity check. Equivalent to a
// throwaway Verifier with no checkpoint.
func VerifyFull(ctx context.Context, queries *db.Queries) (*VerifyResult, error) {
	return NewVerifier(queries).VerifyFull(ctx)
}

// Verifier verifies the audit chain incrementally against an in-memory
// checkpoint, so a routine re-verify costs O(entries appended since the last
// check) rather than O(whole log). The checkpoint is deliberately NOT
// persisted: a process restart rebuilds it with a full walk, which re-verifies
// the entire chain at the moment integrity matters most (after downtime, when
// offline tampering is possible). Safe for concurrent use.
type Verifier struct {
	queries *db.Queries
	mu      sync.Mutex
	// The chain has been verified valid through entry ckptID, whose hash is
	// ckptHash; ckptCount is the entry count seen then. ckptID == 0 means no
	// checkpoint yet (cold) → full walk. A count below ckptCount means rows
	// were deleted → fall back to a full walk to re-establish linkage.
	ckptID    int64
	ckptHash  string
	ckptCount int64
}

// NewVerifier returns a Verifier with no checkpoint; its first Verify (or any
// VerifyFull) walks the whole chain and anchors the checkpoint.
func NewVerifier(queries *db.Queries) *Verifier { return &Verifier{queries: queries} }

// Verify re-verifies the chain incrementally from the checkpoint, walking only
// entries newer than it. With no checkpoint (cold start) or a shrunken log it
// falls back to a full walk. A clean verify advances the checkpoint; a detected
// break leaves it unchanged and reports the break.
func (v *Verifier) Verify(ctx context.Context) (*VerifyResult, error) {
	return v.verify(ctx, false)
}

// VerifyFull forces a whole-chain walk regardless of the checkpoint and
// re-anchors from the result — used by the periodic re-verify and boot warm-up
// to catch tampering below the current checkpoint.
func (v *Verifier) VerifyFull(ctx context.Context) (*VerifyResult, error) {
	return v.verify(ctx, true)
}

func (v *Verifier) verify(ctx context.Context, forceFull bool) (*VerifyResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	count, err := v.queries.CountAuditEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting audit entries: %w", err)
	}
	if count == 0 {
		v.ckptID, v.ckptHash, v.ckptCount = 0, "", 0
		return &VerifyResult{Valid: true, EntryCount: 0}, nil
	}

	var entries []db.AuditEntry
	startPrev := ""
	if forceFull || v.ckptID == 0 || count < v.ckptCount {
		if entries, err = v.queries.ListAuditEntriesAsc(ctx); err != nil {
			return nil, fmt.Errorf("listing audit entries: %w", err)
		}
	} else {
		if entries, err = v.queries.ListAuditEntriesAfterID(ctx, v.ckptID); err != nil {
			return nil, fmt.Errorf("listing audit entries: %w", err)
		}
		startPrev = v.ckptHash
	}

	lastHash, firstBroken, errMsg := verifyChainSegment(entries, startPrev)
	if firstBroken != 0 {
		return &VerifyResult{Valid: false, EntryCount: count, FirstBroken: firstBroken, Error: errMsg}, nil
	}
	// Tail truncation: the Writer's anchored head entry must still exist with
	// the anchored hash. A clean walk cannot see rows that are simply gone.
	if a := currentAnchor(v.queries); a != nil {
		got, gerr := v.queries.GetAuditEntry(ctx, a.ID)
		switch {
		case errors.Is(gerr, sql.ErrNoRows):
			return &VerifyResult{Valid: false, EntryCount: count, FirstBroken: a.ID,
				Error: fmt.Sprintf("head anchor entry %d is missing: the log was truncated or an older database restored", a.ID)}, nil
		case gerr != nil:
			return nil, fmt.Errorf("reading head anchor entry: %w", gerr)
		case got.Hash != a.Hash:
			return &VerifyResult{Valid: false, EntryCount: count, FirstBroken: a.ID,
				Error: fmt.Sprintf("head anchor entry %d hash changed: expected %s, got %s", a.ID, truncHash(a.Hash), truncHash(got.Hash))}, nil
		}
	}
	if len(entries) > 0 {
		v.ckptID = entries[len(entries)-1].ID
		v.ckptHash = lastHash
	}
	v.ckptCount = count
	return &VerifyResult{Valid: true, EntryCount: count}, nil
}

// truncHash truncates a hash string for display, avoiding panics on short strings.
func truncHash(h string) string {
	if len(h) > 16 {
		return h[:16] + "..."
	}
	return h
}

// RecordIfWriter is a convenience for callers that may have a nil Writer.
// If w is nil, the call is a no-op.
func RecordIfWriter(w *Writer, ctx context.Context, eventType, actor, source, summary, payload string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, source, summary, payload); err != nil {
		slog.Error("audit record failed", "type", eventType, "error", err)
	}
}
