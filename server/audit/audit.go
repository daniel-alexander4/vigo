// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package audit provides a tamper-evident audit trail using a SHA-256 hash
// chain. Each entry includes the hash of the previous entry, making
// modifications, deletions, or out-of-order insertions detectable.
package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// auditLoadTimeout bounds the startup-time DB lookup of the previous
// chain hash. A wedged SQLite WAL checkpoint or slow-mount NFS path
// must not pin server boot — chain reset is recoverable (logged at
// Error level), startup hang is not.
const auditLoadTimeout = 5 * time.Second

// Writer appends tamper-evident entries to the audit trail. It maintains
// the hash chain by tracking the previous entry's hash in memory.
// All writes are serialized through a mutex.
type Writer struct {
	queries  *db.Queries
	mu       sync.Mutex
	prevHash string // hash of the last entry (empty string for first entry)

	// OnRecord, when set, is invoked after a successful chain-write with the
	// same fields that were persisted. The server wires this to the
	// integrations dispatcher so audit rows surface as `audit.<eventType>`
	// events to Loki / Splunk / Datadog / Sentinel / etc. alongside the
	// existing event stream (envoy.offline, run.failure, ...). Nil-tolerant.
	OnRecord func(eventType, actor, source, summary, payload string)
}

// NewWriter creates a Writer and loads the latest hash from the DB
// to continue the chain. Bounded by auditLoadTimeout; on timeout or
// any non-ErrNoRows error, the chain is reset to empty prevHash and
// the error is logged so audit-trail reviewers see the boundary.
func NewWriter(queries *db.Queries) *Writer {
	w := &Writer{queries: queries}

	ctx, cancel := context.WithTimeout(context.Background(), auditLoadTimeout)
	defer cancel()

	last, err := queries.GetLastAuditEntry(ctx)
	switch {
	case err == nil:
		w.prevHash = last.Hash
	case errors.Is(err, sql.ErrNoRows):
		// First boot — no prior entries; prevHash stays empty.
	default:
		// Timeout, DB corruption, schema mismatch, etc. — fail-open with
		// a fresh chain so server startup isn't pinned, but surface the
		// boundary loudly so the operator (and future audit-trail review)
		// can bridge old-chain vs new-chain.
		slog.Error("audit: failed to load last hash, starting fresh chain",
			"error", err,
			"timeout", auditLoadTimeout,
		)
	}

	return w
}

// timestampFormat is the layout used for hash computation. It must match
// what Verify reads back from the DB after round-tripping through SQLite.
// We use second-precision UTC to avoid any driver-dependent nanosecond
// truncation.
const timestampFormat = time.RFC3339

// Record appends an audit entry to the hash chain. The entry's hash is
// computed as SHA256(prevHash || timestamp || eventType || actor || source || summary || payload).
func (w *Writer) Record(ctx context.Context, eventType, actor, source, summary, payload string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now().UTC().Truncate(time.Second)
	tsStr := now.Format(timestampFormat)
	hash := computeHash(w.prevHash, tsStr, eventType, actor, source, summary, payload)

	entry, err := w.queries.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
		PrevHash:  w.prevHash,
		Hash:      hash,
		Timestamp: now,
		EventType: eventType,
		Actor:     actor,
		Source:    source,
		Summary:   summary,
		Payload:   payload,
	})
	if err != nil {
		return fmt.Errorf("inserting audit entry: %w", err)
	}

	w.prevHash = entry.Hash

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

// computeHash produces the SHA-256 hex digest for a chain entry.
func computeHash(prevHash, timestamp, eventType, actor, source, summary, payload string) string {
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
		expected := computeHash(prevHash, e.Timestamp.UTC().Format(timestampFormat), e.EventType, e.Actor, e.Source, e.Summary, e.Payload)
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
