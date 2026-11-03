// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package modlint

import (
	"fmt"
	"path/filepath"
	"strings"
)

// FormatSource ensures an operator's configcrate/usercrate carries modlint's
// `#~` standard labels, IN PLACE — it manages only the `#~` lines (module
// label + per-resource labels) and leaves every other byte untouched: the
// operator's hand-written `#` comments (prose AND commented-out attributes
// like `#owner: 0644`), `content:` scalar data, attribute formatting, and key
// order all survive verbatim. This is the source-facing counterpart to the
// stripped `.live` render (Lint with Options.StripComments): source stays
// human-readable, `.live` stays clean.
//
// Returns the formatted bytes and the validation Result. Invalid files (and
// scaffolding/compliance files, which carry no resource labels) are returned
// unchanged — only a valid configcrate/usercrate is relabeled. Idempotent:
// running it on already-labeled source reproduces the same bytes.
func FormatSource(relPath string, data []byte, opts Options) ([]byte, *Result) {
	opts.IsUsercrate = opts.IsUsercrate || IsUsercratePath(relPath)

	switch filepath.Base(relPath) {
	case "compliance.vgo", "envoys.vgo", "roles.vgo", "waivers.vgo", "environments.vgo":
		// Not resource-bearing configcrates — validate, don't relabel.
		return data, LintFile(relPath, data, opts)
	}

	r := &Result{}
	repaired := repair(data, r)
	mod, err := parse(repaired)
	if err != nil {
		r.Findings = append(r.Findings, Finding{
			Severity: Error,
			RuleID:   "yaml-parse",
			Message:  fmt.Sprintf("YAML parse error after repair: %s", err),
		})
		finalizeFindings(r)
		return data, r
	}
	r.Findings = append(r.Findings, applyRules(mod, opts)...)
	finalizeFindings(r)
	if !r.OK() {
		// Never relabel a file that doesn't pass validation.
		return data, r
	}

	return []byte(labelInPlace(string(repaired), mod)), r
}

// labelInPlace inserts/refreshes the `#~` label lines in `text` using the
// parsed module's source positions, touching nothing else.
func labelInPlace(text string, mod *configcrate) string {
	lines := strings.Split(text, "\n")

	// Resources first, bottom-up, so each splice leaves the line indices of
	// the resources above it unchanged.
	for i := len(mod.Resources) - 1; i >= 0; i-- {
		res := mod.Resources[i]
		if res.Line < 1 || res.Line > len(lines) {
			continue
		}
		nameIdx := res.Line - 1
		indent := leadingWhitespace(lines[nameIdx])
		label := indent + "#~ " + generateResourceComment(res) + reversalNote(res.Type)
		lines = spliceLabel(lines, nameIdx, label)
	}

	// Module label: above the top-level `name:` line (re-found, since the
	// resource splices above shifted line numbers).
	if mod.Name != "" {
		for i, l := range lines {
			if strings.HasPrefix(l, "name:") {
				lines = spliceLabel(lines, i, "#~ "+generateConfigcrateComment(mod))
				break
			}
		}
	}

	return strings.Join(lines, "\n")
}

// spliceLabel ensures `label` (a `#~ …` line) sits immediately above the line
// at anchorIdx, refreshing a stale `#~` line and preserving the operator's
// hand `#` comments and blank lines in the comment block above the anchor.
func spliceLabel(lines []string, anchorIdx int, label string) []string {
	// Walk up over the contiguous block of comments and blanks above the
	// anchor (the resource's `- name:` or the module's `name:`).
	start := anchorIdx
	for start-1 >= 0 && isCommentOrBlank(lines[start-1]) {
		start--
	}
	// Rebuild the block: drop any existing `#~` label (ours, possibly stale),
	// keep every hand `#` comment — including commented-out attributes like
	// `#owner: 0644` — and every blank line, in their original order.
	block := make([]string, 0, anchorIdx-start+1)
	for j := start; j < anchorIdx; j++ {
		if isTildeComment(lines[j]) {
			continue
		}
		block = append(block, lines[j])
	}
	// The fresh label goes immediately above the anchor.
	block = append(block, label)

	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:start]...)
	out = append(out, block...)
	out = append(out, lines[anchorIdx:]...)
	return out
}

// leadingWhitespace returns the indentation prefix of a line.
func leadingWhitespace(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// isCommentOrBlank reports whether a line is blank or a `#` comment of any
// kind. Lines inside `content:` block scalars never reach here — labelInPlace
// only inspects the comment block directly above a resource/module name line.
func isCommentOrBlank(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

// isTildeComment reports whether a line is one of modlint's `#~` labels (which
// FormatSource owns and refreshes). A `#owner: 0644` or `# prose` line is NOT
// a tilde comment and is preserved.
func isTildeComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#~")
}
