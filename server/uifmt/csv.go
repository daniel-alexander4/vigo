// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

package uifmt

// CSVSafeCell neutralizes a CSV cell value that a spreadsheet would
// evaluate as a formula (CSV/formula injection). Values beginning with
// '=', '+', '-', '@', TAB or CR are prefixed with a single quote so Excel,
// LibreOffice and Sheets treat them as text. Hostnames and trait values are
// envoy-controlled, so every CSV export that carries them routes through
// here. Quoting for commas/quotes/newlines is the writer's job
// (encoding/csv, or the UI's csvEscape) and happens after this.
func CSVSafeCell(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}
