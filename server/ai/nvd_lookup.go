// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package ai

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// CVEDetail is the AI-package-owned projection of an NVD record — the
// authoritative facts the assistant cites when an operator clicks
// Ask-AI on a security-drawer CVE card. Sourced from the shared
// risk-package NVD enricher; the ai package never imports risk so the
// fields are copied across the wiring boundary in main.go.
type CVEDetail struct {
	CVEID       string
	CVSSScore   float64
	CVSSVector  string
	Severity    string
	Description string
	CWE         []string
	References  []string
	PublishedAt string
}

// CVELookup returns the NVD record for a CVE ID — cache-first with an
// on-miss synchronous fetch on the implementation side. Implementations
// MUST return (nil, nil) when the CVE is unknown or NVD enrichment is
// disabled; only a transport-level error returns a non-nil error.
type CVELookup interface {
	LookupCVE(ctx context.Context, cveID string) (*CVEDetail, error)
}

// SetCVELookup wires the on-demand NVD lookup. Without it, the prefetch
// path renders only the structured question line — no CVE enrichment.
func (a *Assistant) SetCVELookup(l CVELookup) {
	a.cveLookup = l
}

// cveIDRegex captures the canonical CVE-YYYY-NNNN+ form per MITRE
// (year then a suffix of at least 4 digits, no fixed upper bound —
// MITRE has issued >7-digit IDs). Case-insensitive. Used to lift the
// CVE ID out of structured Ask-AI questions emitted by the security
// drawer's helpers.
var cveIDRegex = regexp.MustCompile(`(?i)CVE-\d{4}-\d{4,}`)

// extractCVEID returns the uppercased CVE ID embedded in question, or
// the empty string if none. The structured Ask-AI line for a cached or
// per-scanner CVE finding always carries one.
func extractCVEID(question string) string {
	m := cveIDRegex.FindString(question)
	return strings.ToUpper(m)
}

// formatCVEDetailMarkdown renders the prefetch block injected before
// the assistant turn. Marked "NVD authoritative" so the system prompt
// instruction in prompt.go can refer to it by name.
func formatCVEDetailMarkdown(d *CVEDetail) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## CVE Detail (NVD authoritative)\n\n")
	b.WriteString("- **ID**: " + d.CVEID + "\n")
	if d.CVSSScore > 0 {
		b.WriteString(fmt.Sprintf("- **CVSS**: %.1f %s — `%s`\n", d.CVSSScore, strings.ToUpper(d.Severity), d.CVSSVector))
	}
	if len(d.CWE) > 0 {
		b.WriteString("- **CWE**: " + strings.Join(d.CWE, ", ") + "\n")
	}
	if d.PublishedAt != "" {
		b.WriteString("- **Published**: " + d.PublishedAt + "\n")
	}
	if d.Description != "" {
		// Fence the vendor-provided text: NVD descriptions can contain
		// backticks, bracket-link syntax, or other markdown punctuation
		// that would mis-render in the assistant's reply or — worse —
		// land in the LLM context as something that looks like an
		// instruction. Verbatim code block kills both: no markdown
		// interpretation, and the LLM treats fenced content as data.
		b.WriteString("- **Description**:\n\n```text\n")
		b.WriteString(d.Description)
		b.WriteString("\n```\n")
	}
	if len(d.References) > 0 {
		b.WriteString("- **References**:\n")
		for _, r := range d.References {
			b.WriteString("  - " + r + "\n")
		}
	}
	return b.String()
}

// lookupCVEDetail safely fetches and formats CVE enrichment. Returns an
// empty string on disabled lookup, unknown CVE, or fetch error so the
// caller can treat the absence uniformly as "no enrichment".
func (a *Assistant) lookupCVEDetail(ctx context.Context, cveID string) string {
	if a.cveLookup == nil || cveID == "" {
		return ""
	}
	detail, err := a.cveLookup.LookupCVE(ctx, cveID)
	if err != nil || detail == nil {
		return ""
	}
	return formatCVEDetailMarkdown(detail)
}
