// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: AGPL-3.0-only

package grpc

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateTraceparentWellFormed(t *testing.T) {
	tp, tid := GenerateTraceparent()
	if len(tp) != traceparentLen {
		t.Fatalf("traceparent length = %d, want %d (%q)", len(tp), traceparentLen, tp)
	}
	if !traceparentRe.MatchString(tp) {
		t.Fatalf("traceparent does not match W3C format: %q", tp)
	}
	if len(tid) != 32 {
		t.Fatalf("trace_id length = %d, want 32 (%q)", len(tid), tid)
	}
	if !strings.Contains(tp, tid) {
		t.Fatalf("traceparent %q does not embed trace_id %q", tp, tid)
	}
}

func TestGenerateTraceparentUnique(t *testing.T) {
	_, a := GenerateTraceparent()
	_, b := GenerateTraceparent()
	if a == b {
		t.Fatalf("two generated trace_ids collided: %q", a)
	}
}

func TestParseOrGenerateRoundTrip(t *testing.T) {
	orig, origTid := GenerateTraceparent()
	out, tid := ParseOrGenerate(orig)
	if out != orig {
		t.Fatalf("valid traceparent altered: got %q, want %q", out, orig)
	}
	if tid != origTid {
		t.Fatalf("trace_id mismatch on round-trip: got %q, want %q", tid, origTid)
	}
}

func TestParseOrGenerateRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"too short":     "00-deadbeef-01",
		"too long":      strings.Repeat("a", 100),
		"upper-case":    "00-DEADBEEFDEADBEEFDEADBEEFDEADBEEF-0000000000000000-01",
		"wrong format":  "garbage-not-a-traceparent-at-all-no-no-no-no-no-yo",
		"missing dash":  "00.deadbeefdeadbeefdeadbeefdeadbeef.0000000000000000.01",
		"non-hex chars": "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-0000000000000000-01",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			out, tid := ParseOrGenerate(in)
			if out == in {
				t.Fatalf("malformed input was preserved: %q", in)
			}
			if !traceparentRe.MatchString(out) {
				t.Fatalf("generated replacement is not well-formed: %q", out)
			}
			if len(tid) != 32 {
				t.Fatalf("generated trace_id has wrong length: %q", tid)
			}
		})
	}
}

func TestWithAndFromCtx(t *testing.T) {
	ctx := context.Background()
	if got := TraceIDFromCtx(ctx); got != "" {
		t.Fatalf("empty ctx returned non-empty trace_id: %q", got)
	}
	ctx = WithTraceID(ctx, "abcd1234")
	if got := TraceIDFromCtx(ctx); got != "abcd1234" {
		t.Fatalf("ctx trace_id round-trip failed: got %q, want %q", got, "abcd1234")
	}
}

func TestEnsureTraceIDPreservesExisting(t *testing.T) {
	// If ctx already has a trace_id, EnsureTraceID must not generate a new
	// one — otherwise outer-dispatcher / inner-handler double-extraction
	// would produce mismatched values.
	ctx := WithTraceID(context.Background(), "outer-value")
	_, tid := EnsureTraceID(ctx, "")
	if tid != "outer-value" {
		t.Fatalf("EnsureTraceID overwrote existing ctx value: got %q", tid)
	}
}

func TestEnsureTraceIDGeneratesWhenAbsent(t *testing.T) {
	ctx, tid := EnsureTraceID(context.Background(), "")
	if tid == "" {
		t.Fatal("EnsureTraceID returned empty trace_id for empty ctx")
	}
	if got := TraceIDFromCtx(ctx); got != tid {
		t.Fatalf("returned trace_id not attached to ctx: ctx=%q, returned=%q", got, tid)
	}
}

func TestEnsureTraceIDParsesValidInput(t *testing.T) {
	tp, want := GenerateTraceparent()
	_, got := EnsureTraceID(context.Background(), tp)
	if got != want {
		t.Fatalf("EnsureTraceID failed to parse valid traceparent: got %q, want %q", got, want)
	}
}
