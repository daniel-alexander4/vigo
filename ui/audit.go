// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ui

import (
	"context"
	"log/slog"
)

// auditRecorder is the interface for the tamper-evident audit writer.
// Using an interface lets callers pass nil safely and avoids tight coupling.
// Mirrors the same interface in server/api/config.go — kept package-local so
// ui doesn't take a hard dependency on server/audit's concrete Writer type.
type auditRecorder interface {
	Record(ctx context.Context, eventType, actor, source, summary, payload string) error
}

// recordAudit is a nil-safe helper that records an audit event. If w is nil
// (audit not configured), it's a no-op.
func recordAudit(ctx context.Context, w auditRecorder, eventType, actor, source, summary, payload string) {
	if w == nil {
		return
	}
	if err := w.Record(ctx, eventType, actor, source, summary, payload); err != nil {
		slog.Error("audit record failed", "type", eventType, "error", err)
	}
}
