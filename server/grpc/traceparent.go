// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// W3C traceparent: "00-<trace_id_32hex>-<span_id_16hex>-<flags_2hex>".
// ADR-028.
const traceparentLen = 55

var traceparentRe = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

// traceCtxKey scopes the trace_id stored on context.
type traceCtxKey struct{}

// ParseOrGenerate returns (traceparent, trace_id). If the input is a valid
// W3C traceparent the caller's value is preserved; if it is empty or
// malformed, a fresh value is generated (16 random bytes → trace_id, zero
// span_id, sampled flag). The trace_id portion is the 32-hex middle field
// — the value surfaced to slog and the integrations payload.
func ParseOrGenerate(traceparent string) (string, string) {
	if len(traceparent) == traceparentLen && traceparentRe.MatchString(traceparent) {
		return traceparent, traceparent[3:35]
	}
	return GenerateTraceparent()
}

// GenerateTraceparent mints a fresh W3C traceparent with a random trace_id.
// Returns (traceparent, trace_id).
func GenerateTraceparent() (string, string) {
	var tid [16]byte
	_, _ = rand.Read(tid[:])
	traceID := hex.EncodeToString(tid[:])
	// Zero span_id is fine for trace-id-only correlation; the sampled
	// flag (01) signals downstream OTel collectors to record if/when the
	// full-OTel follow-on lands.
	return "00-" + traceID + "-0000000000000000-01", traceID
}

// WithTraceID returns ctx carrying trace_id. Stored as a typed key to keep
// it private to this package; handlers read it back via TraceIDFromCtx.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceCtxKey{}, traceID)
}

// TraceIDFromCtx returns the trace_id stored on ctx, or "" if none. The
// empty case is safe to pass to slog — the attribute is still emitted; it
// just carries an empty value, which is a strong signal in itself.
func TraceIDFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(traceCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// EnsureTraceID returns (ctx, trace_id), reusing any trace_id already stored
// on ctx (set by an outer dispatcher) and otherwise parsing or generating
// from the supplied traceparent. Idempotent: handlers can call it
// unconditionally regardless of whether their caller already attached one.
func EnsureTraceID(ctx context.Context, traceparent string) (context.Context, string) {
	if existing := TraceIDFromCtx(ctx); existing != "" {
		return ctx, existing
	}
	_, traceID := ParseOrGenerate(traceparent)
	return WithTraceID(ctx, traceID), traceID
}
