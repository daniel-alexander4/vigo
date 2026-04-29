// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package modlint

// Severity classifies how a Finding affects publish.
//
// `Error` blocks publish; everything else is advisory. The string form is
// stable for JSON output and CI integration — do not rename without bumping
// any consumer that filters on these values.
type Severity string

const (
	// Error blocks publish. The author must fix it before the module can land.
	Error Severity = "error"
	// Warning surfaces a likely-unintended pattern that the linter either
	// auto-fixed (FixApplied=true) or wants the author to acknowledge.
	Warning Severity = "warning"
	// Info is style/idiom guidance — non-actionable for correctness.
	Info Severity = "info"
	// Hint suggests a tightening the author may want, never required.
	Hint Severity = "hint"
)

// Finding is one lint result emitted by a Rule.
//
// Line and Column are 1-indexed source positions; both are zero when the
// finding is module-level (e.g. "no resources") or has no source-locatable
// origin. Coordinates refer to the file as-parsed AFTER repair, which for
// every repair operation in this package preserves line numbering relative
// to the original input (BOM strip, tab expansion, trailing-whitespace
// trim, over-indent strip — none of these split or merge lines). The one
// edge case is bare-CR line endings (`foo\rbar`) being normalized to LF,
// which would shift line counts; in practice operators never produce that.
//
// RuleID is the stable identifier of the rule that emitted this finding
// — used by JSON consumers to filter, by tests to pin specific checks,
// and by future "ignore this rule" file annotations.
//
// FixApplied=true means the rule mutated the module to repair the issue;
// the finding is informational about what changed. FixApplied=false means
// the author still needs to act.
type Finding struct {
	Severity   Severity
	Line       int
	Column     int
	RuleID     string
	Message    string
	FixApplied bool
}

// String renders a Finding for human display in the same shape as the
// existing Errors/Warnings string slices, so callers that print findings
// inline don't need a format change. Includes line:column when available.
func (f Finding) String() string {
	if f.Line > 0 {
		if f.Column > 0 {
			return formatLineCol(f.Line, f.Column) + " " + f.Message
		}
		return formatLine(f.Line) + " " + f.Message
	}
	return f.Message
}

func formatLine(line int) string {
	return "line " + itoa(line) + ":"
}

func formatLineCol(line, col int) string {
	return "line " + itoa(line) + ":" + itoa(col) + ":"
}

// itoa avoids pulling in fmt for a hot tight conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
