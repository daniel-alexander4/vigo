// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// ErrWeasyPrintUnavailable is returned by RenderHTMLToPDF when the weasyprint
// binary is not on the server's PATH. Handlers that surface PDF generation to
// end users should map this sentinel to a 503 with a clear fallback message
// (e.g., "PDF generation is not available on this server; use the HTML
// endpoint instead") rather than a generic 500.
var ErrWeasyPrintUnavailable = errors.New("weasyprint binary not installed")

// weasyPrintDefaultTimeout bounds a single PDF render. WeasyPrint typically
// renders an attestation-sized document in under 2 seconds; 30 seconds is
// deliberately generous so first-render font-cache warm-up doesn't trip the
// timeout.
const weasyPrintDefaultTimeout = 30 * time.Second

// RenderHTMLToPDF pipes the given HTML bytes to WeasyPrint via its stdin/stdout
// mode and returns the resulting PDF bytes. WeasyPrint does not execute
// JavaScript, which is why it's a safer rendering target than headless
// chromium for documents built from server-assembled HTML.
//
// If ctx has no deadline, a 30-second default timeout is applied so a stuck
// subprocess can't leak. Callers that want a different bound should pass a
// context with their own deadline.
//
// Returns ErrWeasyPrintUnavailable (wrapped) when the binary is missing;
// otherwise wraps the subprocess error with stderr content for log triage.
func RenderHTMLToPDF(ctx context.Context, html []byte) ([]byte, error) {
	if _, err := exec.LookPath("weasyprint"); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWeasyPrintUnavailable, err)
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, weasyPrintDefaultTimeout)
		defer cancel()
	}

	// "-" "-" means stdin → stdout. --encoding pins the stdin encoding so
	// WeasyPrint doesn't have to sniff.
	cmd := exec.CommandContext(ctx, "weasyprint", "-", "-", "--encoding=utf-8")
	cmd.Stdin = bytes.NewReader(html)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		// WeasyPrint writes rendering warnings + errors to stderr; surface
		// the tail for operator triage without logging the full HTML input.
		slog.Warn("weasyprint render failed",
			"error", err,
			"stderr", stderr.String(),
			"html_bytes", len(html),
			"duration", time.Since(start).Round(time.Millisecond),
		)
		return nil, fmt.Errorf("weasyprint: %w", err)
	}

	slog.Info("weasyprint render ok",
		"html_bytes", len(html),
		"pdf_bytes", stdout.Len(),
		"duration", time.Since(start).Round(time.Millisecond),
	)

	return stdout.Bytes(), nil
}
