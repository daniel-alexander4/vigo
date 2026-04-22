// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

// Package audit provides a tamper-evident audit trail using a SHA-256 hash
// chain. Each entry includes the hash of the previous entry, making
// modifications, deletions, or out-of-order insertions detectable.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"git.alexander4.io/vigo/server/db"
)

// Writer appends tamper-evident entries to the audit trail. It maintains
// the hash chain by tracking the previous entry's hash in memory.
// All writes are serialized through a mutex.
type Writer struct {
	queries  *db.Queries
	mu       sync.Mutex
	prevHash string // hash of the last entry (empty string for first entry)
}

// NewWriter creates a Writer and loads the latest hash from the DB
// to continue the chain.
func NewWriter(queries *db.Queries) *Writer {
	w := &Writer{queries: queries}

	// Load the last entry's hash to continue the chain.
	last, err := queries.GetLastAuditEntry(context.Background())
	if err == nil {
		w.prevHash = last.Hash
	}
	// If no entries exist (sql.ErrNoRows), prevHash stays empty — that's correct.

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

// Verify walks the entire audit chain and recomputes each hash. If any
// entry's stored hash does not match the recomputed hash, the chain is
// broken and the first mismatched entry ID is returned.
func Verify(ctx context.Context, queries *db.Queries) (*VerifyResult, error) {
	count, err := queries.CountAuditEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("counting audit entries: %w", err)
	}

	if count == 0 {
		return &VerifyResult{Valid: true, EntryCount: 0}, nil
	}

	entries, err := queries.ListAuditEntriesAsc(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing audit entries: %w", err)
	}

	prevHash := ""
	for _, e := range entries {
		expected := computeHash(prevHash, e.Timestamp.UTC().Format(timestampFormat), e.EventType, e.Actor, e.Source, e.Summary, e.Payload)

		if e.Hash != expected {
			return &VerifyResult{
				Valid:       false,
				EntryCount:  count,
				FirstBroken: e.ID,
				Error:       fmt.Sprintf("hash mismatch at entry %d: expected %s, got %s", e.ID, truncHash(expected), truncHash(e.Hash)),
			}, nil
		}

		// Verify chain linkage.
		if e.PrevHash != prevHash {
			return &VerifyResult{
				Valid:       false,
				EntryCount:  count,
				FirstBroken: e.ID,
				Error:       fmt.Sprintf("chain break at entry %d: prev_hash does not match previous entry", e.ID),
			}, nil
		}

		prevHash = e.Hash
	}

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

