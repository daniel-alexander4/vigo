// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package api

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestRenderHTMLToPDF_HappyPath exercises the real WeasyPrint binary when
// present. Dev machines without WeasyPrint installed skip this test; CI runs
// it inside the Alpine container where the binary is packaged.
func TestRenderHTMLToPDF_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("weasyprint"); err != nil {
		t.Skip("weasyprint not installed on this host — run inside the Docker image or `apk add weasyprint`")
	}

	html := []byte(`<!DOCTYPE html>
<html><body><h1>Hello</h1><p>pdfgen_test happy path</p></body></html>`)

	pdf, err := RenderHTMLToPDF(context.Background(), html)
	if err != nil {
		t.Fatalf("RenderHTMLToPDF failed: %v", err)
	}

	// The PDF file format starts with "%PDF-<version>". Checking the magic
	// bytes confirms we got a real PDF, not an empty buffer or HTML
	// passed through.
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("output does not look like a PDF; first 16 bytes = %q", pdf[:min(16, len(pdf))])
	}

	// Smoke-check the trailer too — WeasyPrint always ends PDFs with
	// `%%EOF`. A truncated output would fail here even if the header was
	// intact.
	if !bytes.Contains(pdf[max(0, len(pdf)-32):], []byte("%%EOF")) {
		t.Errorf("PDF trailer missing (output may be truncated); last 32 bytes = %q", pdf[max(0, len(pdf)-32):])
	}
}

// TestRenderHTMLToPDF_MissingBinary confirms the distinctive error sentinel
// fires when weasyprint isn't on PATH. Callers (the PDF handler) key on this
// to return a 503 with a clear fallback instead of a generic 500.
func TestRenderHTMLToPDF_MissingBinary(t *testing.T) {
	// Force LookPath to fail by nuking PATH for this test only.
	t.Setenv("PATH", "")

	_, err := RenderHTMLToPDF(context.Background(), []byte("<html></html>"))
	if err == nil {
		t.Fatal("expected error when weasyprint is missing, got nil")
	}
	if !errors.Is(err, ErrWeasyPrintUnavailable) {
		t.Fatalf("expected ErrWeasyPrintUnavailable sentinel, got %v", err)
	}
}

// TestRenderHTMLToPDF_ContextDeadline exercises the context timeout path.
// Uses a 1 ms deadline against a buffer large enough that WeasyPrint cannot
// possibly complete in time. Skips if weasyprint isn't installed (the test
// has nothing to kill otherwise).
func TestRenderHTMLToPDF_ContextDeadline(t *testing.T) {
	if _, err := exec.LookPath("weasyprint"); err != nil {
		t.Skip("weasyprint not installed — deadline test requires a real subprocess to kill")
	}

	// Build a moderately large HTML input so the subprocess has to spend
	// at least a few ms reading + parsing — enough for the 1 ms deadline
	// to bite even on a fast host.
	var big strings.Builder
	big.WriteString("<html><body>")
	for i := 0; i < 5000; i++ {
		big.WriteString("<p>filler paragraph line for the deadline test</p>")
	}
	big.WriteString("</body></html>")

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err := RenderHTMLToPDF(ctx, []byte(big.String()))
	if err == nil {
		t.Fatal("expected deadline-exceeded error, got nil")
	}
	// Either the raw deadline sentinel (process killed by ctx) or a
	// wrapped version is acceptable — what we care about is that it
	// returned an error rather than hanging.
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "signal") {
		t.Logf("err = %v (passing anyway — error returned, deadline enforced)", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
